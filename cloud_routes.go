package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type WaypointEdit struct {
	Name           string  `json:"name"`
	Lat            float64 `json:"lat"`
	Lon            float64 `json:"lon"`
	RadiusNM       float64 `json:"radiusNM"`
	PortsideXTDNM  float64 `json:"portsideXTDNM"`
	StarboardXTDNM float64 `json:"starboardXTDNM"`
	WheelOverNM    float64 `json:"wheelOverNM"`
	SpeedKn        float64 `json:"speedKn"`
	GeometryType   string  `json:"geometryType"`
	Remarks        string  `json:"remarks"`
}

type RouteUpdateRequest struct {
	ID        string         `json:"id"`
	RouteName string         `json:"routeName"`
	Waypoints []WaypointEdit `json:"waypoints"`
}

func (a *App) plansPath() string { return filepath.Join(a.uploadDir, "saved_route_plans.json") }

func (a *App) loadSavedPlans() {
	b, err := os.ReadFile(a.plansPath())
	if err != nil {
		return
	}
	var plans map[string]*RoutePlan
	if json.Unmarshal(b, &plans) != nil || plans == nil {
		return
	}
	now := time.Now().UTC()
	for _, p := range plans {
		if p.CreatedUTC.IsZero() {
			p.CreatedUTC = now
		}
		if p.UpdatedUTC.IsZero() {
			p.UpdatedUTC = p.CreatedUTC
		}
		if p.Revision < 1 {
			p.Revision = 1
		}
		for i := range p.Waypoints {
			if strings.TrimSpace(p.Waypoints[i].GeometryType) == "" {
				p.Waypoints[i].GeometryType = "Loxodrome"
			}
		}
	}
	a.plansMu.Lock()
	a.plans = plans
	a.plansMu.Unlock()
}

func (a *App) savePlans() error {
	a.plansSaveMu.Lock()
	defer a.plansSaveMu.Unlock()
	a.plansMu.RLock()
	copyMap := make(map[string]*RoutePlan, len(a.plans))
	for k, v := range a.plans {
		copyMap[k] = v
	}
	a.plansMu.RUnlock()
	if err := os.MkdirAll(a.uploadDir, 0750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(copyMap, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.plansPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0640); err != nil {
		return err
	}
	return os.Rename(tmp, a.plansPath())
}

func cloneRoutePlan(p *RoutePlan) (*RoutePlan, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var out RoutePlan
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func normalizeGeometryType(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "orthodrome", "great circle", "great-circle":
		return "Orthodrome"
	default:
		return "Loxodrome"
	}
}

func validateWaypointEdit(i int, x WaypointEdit) error {
	if math.IsNaN(x.Lat) || math.IsInf(x.Lat, 0) || math.IsNaN(x.Lon) || math.IsInf(x.Lon, 0) || x.Lat < -90 || x.Lat > 90 || x.Lon < -180 || x.Lon > 180 {
		return fmt.Errorf("waypoint %d has invalid coordinates", i+1)
	}
	if x.RadiusNM < 0 || x.RadiusNM > 20 {
		return fmt.Errorf("waypoint %d has an invalid turn radius", i+1)
	}
	if x.PortsideXTDNM < 0 || x.PortsideXTDNM > 20 || x.StarboardXTDNM < 0 || x.StarboardXTDNM > 20 {
		return fmt.Errorf("waypoint %d has invalid XTD limits", i+1)
	}
	if x.WheelOverNM < 0 || x.WheelOverNM > 20 {
		return fmt.Errorf("waypoint %d has an invalid wheel-over distance", i+1)
	}
	if x.SpeedKn < 0 || x.SpeedKn > 60 {
		return fmt.Errorf("waypoint %d has an invalid leg speed", i+1)
	}
	if len([]rune(x.Name)) > 128 {
		return fmt.Errorf("waypoint %d name is too long", i+1)
	}
	if len([]rune(x.Remarks)) > 2000 {
		return fmt.Errorf("waypoint %d remarks are too long", i+1)
	}
	return nil
}

func (a *App) recalculateEditedPlan(p *RoutePlan) error {
	if len(p.Waypoints) < 2 {
		return fmt.Errorf("a route requires at least two waypoints")
	}
	targetHours := p.ArrivalUTC.Sub(p.DepartureUTC).Hours()
	if targetHours <= 0 {
		return fmt.Errorf("arrival must be after departure")
	}
	total := 0.0
	for i := 0; i < len(p.Waypoints)-1; i++ {
		total += HaversineNM(p.Waypoints[i].Lat, p.Waypoints[i].Lon, p.Waypoints[i+1].Lat, p.Waypoints[i+1].Lon)
	}
	baseSpeed := total / targetHours
	var eng *EngineConfig
	planner := a.plannerSnapshot()
	for i := range planner.Configs {
		if baseSpeed <= planner.Configs[i].SpeedKn+.05 {
			c := planner.Configs[i]
			eng = &c
			break
		}
	}
	p.DistanceNM = total
	p.WaypointCount = len(p.Waypoints)
	p.RequiredSpeedKn = baseSpeed
	p.Engine = eng

	minLat, maxLat, minLon, maxLon := 90.0, -90.0, 180.0, -180.0
	for _, w := range p.Waypoints {
		if w.Lat < minLat {
			minLat = w.Lat
		}
		if w.Lat > maxLat {
			maxLat = w.Lat
		}
		if w.Lon < minLon {
			minLon = w.Lon
		}
		if w.Lon > maxLon {
			maxLon = w.Lon
		}
	}
	comp, _ := a.olex.CompositeForCorridor(minLat, minLon, maxLat, maxLon)
	supported, review, unsupported, centered := 0.0, 0.0, 0.0, 0.0
	contributions := map[string]float64{}
	trackCounts := []float64{}
	elapsed := 0.0
	for i := range p.Waypoints {
		p.Waypoints[i].Leg = nil
	}
	for i := 0; i < len(p.Waypoints)-1; i++ {
		w1, w2 := p.Waypoints[i], p.Waypoints[i+1]
		d := HaversineNM(w1.Lat, w1.Lon, w2.Lat, w2.Lon)
		course := InitialBearing(w1.Lat, w1.Lon, w2.Lat, w2.Lon)
		legSpeed := w1.SpeedKn
		if legSpeed <= 0 {
			legSpeed = baseSpeed
		}
		elapsed += d / math.Max(legSpeed, .01)
		assess := OlexAssessment{Status: "OFFICER CHECK", ReviewFraction: 1, Segments: []OlexSupportSegment{{StartFraction: 0, EndFraction: 1, Status: "OFFICER CHECK"}}}
		if comp != nil {
			assess = comp.AssessSegment(w1.Lat, w1.Lon, w2.Lat, w2.Lon, 5.7)
		}
		supported += d * assess.SupportedFraction
		review += d * assess.ReviewFraction
		unsupported += d * assess.UnsupportedFraction
		for n, f := range assess.AreaFractions {
			contributions[n] += d * f
		}
		ml, mo := InterpolateGC(w1.Lat, w1.Lon, w2.Lat, w2.Lon, .5)
		ci := planner.SegmentIndex.CorridorAt(ml, mo, course, 1.2, 0)
		if ci.Centered {
			centered += d
		}
		if ci.Consensus > 0 {
			trackCounts = append(trackCounts, float64(ci.Consensus))
		}
		comment := corridorComment(ci)
		if strings.TrimSpace(w1.Remarks) != "" {
			if comment != "" {
				comment += " "
			}
			comment += strings.TrimSpace(w1.Remarks)
		}
		if assess.Comment != "" {
			if comment != "" {
				comment += " "
			}
			comment += assess.Comment
		}
		p.Waypoints[i].Leg = &Leg{Status: assess.Status, CourseDeg: course, DistanceNM: d, SpeedKn: legSpeed, ETAUTC: p.DepartureUTC.Add(time.Duration(elapsed * float64(time.Hour))), Comment: comment, OlexAreaName: assess.PrimaryArea, SupportSegments: assess.Segments}
	}
	if total > 0 {
		p.SupportedPct = 100 * supported / total
		p.ReviewPct = 100 * review / total
		p.UnsupportedPct = 100 * unsupported / total
		p.CorridorCenteredPct = 100 * centered / total
	}
	p.EstimatedDurationHours = elapsed
	p.PlannedArrivalUTC = p.DepartureUTC.Add(time.Duration(elapsed * float64(time.Hour)))
	p.FuelMT = 0
	if eng != nil {
		p.FuelMT = eng.ConsumptionMTDay * elapsed / 24
	}
	p.MedianCorridorTracks = 0
	if len(trackCounts) > 0 {
		sort.Float64s(trackCounts)
		p.MedianCorridorTracks = median(trackCounts)
	}
	p.OlexAreaNames = nil
	p.OlexContributions = nil
	for n, nm := range contributions {
		if nm > 0 {
			p.OlexAreaNames = append(p.OlexAreaNames, n)
			p.OlexContributions = append(p.OlexContributions, OlexContribution{Name: n, Percent: 100 * nm / math.Max(total, .001)})
		}
	}
	sort.Strings(p.OlexAreaNames)
	sort.Slice(p.OlexContributions, func(i, j int) bool { return p.OlexContributions[i].Percent > p.OlexContributions[j].Percent })
	p.UpdatedUTC = time.Now().UTC()
	if p.CreatedUTC.IsZero() {
		p.CreatedUTC = p.UpdatedUTC
	}
	if p.Revision < 1 {
		p.Revision = 1
	} else {
		p.Revision++
	}
	return nil
}

func (a *App) handleRouteUpdate(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var q RouteUpdateRequest
	if !decodeJSON(w, r, &q) {
		return
	}
	q.ID = strings.TrimSpace(q.ID)
	a.plansMu.RLock()
	existing, ok := a.plans[q.ID]
	a.plansMu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "Route not found")
		return
	}
	if len(q.Waypoints) < 2 || len(q.Waypoints) > 2000 {
		writeError(w, http.StatusBadRequest, "Route must contain 2 to 2000 waypoints")
		return
	}
	for i, x := range q.Waypoints {
		if err := validateWaypointEdit(i, x); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	p, err := cloneRoutePlan(existing)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not prepare route update")
		return
	}
	if strings.TrimSpace(q.RouteName) != "" {
		p.RouteName = strings.TrimSpace(q.RouteName)
	}
	p.Waypoints = make([]Waypoint, len(q.Waypoints))
	for i, x := range q.Waypoints {
		name := strings.TrimSpace(x.Name)
		if name == "" {
			name = fmt.Sprintf("WP%03d", i+1)
		}
		p.Waypoints[i] = Waypoint{
			Name: name, Lat: x.Lat, Lon: x.Lon, RadiusNM: x.RadiusNM,
			PortsideXTDNM: x.PortsideXTDNM, StarboardXTDNM: x.StarboardXTDNM,
			WheelOverNM: x.WheelOverNM, SpeedKn: x.SpeedKn,
			GeometryType: normalizeGeometryType(x.GeometryType), Remarks: strings.TrimSpace(x.Remarks),
		}
	}
	if err := a.recalculateEditedPlan(p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.plansMu.Lock()
	a.plans[p.ID] = p
	a.plansMu.Unlock()
	if err := a.savePlans(); err != nil {
		a.log.Printf("save route plans: %v", err)
		writeError(w, http.StatusInternalServerError, "Route changed but could not be persisted")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (a *App) handleRouteList(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	a.plansMu.RLock()
	out := make([]*RoutePlan, 0, len(a.plans))
	for _, p := range a.plans {
		out = append(out, p)
	}
	a.plansMu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedUTC.Equal(out[j].UpdatedUTC) {
			return out[i].ID > out[j].ID
		}
		return out[i].UpdatedUTC.After(out[j].UpdatedUTC)
	})
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleRouteGet(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	p, ok := a.getPlan(r)
	if !ok {
		writeError(w, http.StatusNotFound, "Route not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (a *App) handleRouteDelete(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodDelete) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	a.plansMu.Lock()
	_, ok := a.plans[id]
	if ok {
		delete(a.plans, id)
	}
	a.plansMu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "Route not found")
		return
	}
	if err := a.savePlans(); err != nil {
		a.log.Printf("save route plans after delete: %v", err)
		writeError(w, http.StatusInternalServerError, "Route was removed from memory but persistence failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleRouteClone(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var q struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &q) {
		return
	}
	a.plansMu.RLock()
	existing, ok := a.plans[strings.TrimSpace(q.ID)]
	a.plansMu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "Route not found")
		return
	}
	p, err := cloneRoutePlan(existing)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not clone route")
		return
	}
	now := time.Now().UTC()
	p.ID = fmt.Sprintf("%d", now.UnixNano())
	p.CreatedUTC = now
	p.UpdatedUTC = now
	p.Revision = 1
	if strings.TrimSpace(q.Name) != "" {
		p.RouteName = strings.TrimSpace(q.Name)
	} else {
		p.RouteName = strings.TrimSpace(p.RouteName) + " copy"
	}
	a.plansMu.Lock()
	a.plans[p.ID] = p
	a.plansMu.Unlock()
	if err := a.savePlans(); err != nil {
		writeError(w, http.StatusInternalServerError, "Cloned route could not be persisted")
		return
	}
	writeJSON(w, http.StatusOK, p)
}
