package main

import (
	"container/heap"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type RouteRequest struct {
	Start            string
	End              string
	RouteName        string
	DepartureDate    string
	DepartureTime    string
	DepartureZone    string
	ArrivalDate      string
	ArrivalTime      string
	ArrivalZone      string
	MinimumWaypoints bool
	AddComments      bool
}
type Position struct {
	Name string  `json:"name"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
}
type RoutePlan struct {
	ID                     string             `json:"id"`
	RouteName              string             `json:"routeName"`
	CreatedUTC             time.Time          `json:"createdUTC"`
	UpdatedUTC             time.Time          `json:"updatedUTC"`
	Revision               int                `json:"revision"`
	DistanceNM             float64            `json:"distanceNM"`
	WaypointCount          int                `json:"waypointCount"`
	SupportedPct           float64            `json:"supportedPct"`
	ReviewPct              float64            `json:"reviewPct"`
	UnsupportedPct         float64            `json:"unsupportedPct"`
	CorridorCenteredPct    float64            `json:"corridorCenteredPct"`
	MedianCorridorTracks   float64            `json:"medianCorridorTracks"`
	RequiredSpeedKn        float64            `json:"requiredSpeedKn"`
	Engine                 *EngineConfig      `json:"engine,omitempty"`
	FuelMT                 float64            `json:"fuelMT"`
	DepartureUTC           time.Time          `json:"departureUTC"`
	ArrivalUTC             time.Time          `json:"arrivalUTC"`
	PlannedArrivalUTC      time.Time          `json:"plannedArrivalUTC"`
	EstimatedDurationHours float64            `json:"estimatedDurationHours"`
	OlexAreaNames          []string           `json:"olexAreaNames,omitempty"`
	OlexContributions      []OlexContribution `json:"olexContributions,omitempty"`
	Waypoints              []Waypoint         `json:"waypoints"`
}
type Waypoint struct {
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
	Leg            *Leg    `json:"leg,omitempty"`
}
type Leg struct {
	Status          string               `json:"status"`
	CourseDeg       float64              `json:"courseDeg"`
	DistanceNM      float64              `json:"distanceNM"`
	SpeedKn         float64              `json:"speedKn"`
	ETAUTC          time.Time            `json:"etaUTC"`
	Comment         string               `json:"comment"`
	OlexAreaName    string               `json:"olexAreaName,omitempty"`
	SupportSegments []OlexSupportSegment `json:"supportSegments,omitempty"`
}
type routePoint struct {
	Lat, Lon float64
	Name     string
	RouteID  uint32
	Endpoint bool
	Corr     CorridorInfo
}

type endpointCandidate struct {
	node int
	dist float64
}

func (p *PlannerData) endpointCandidates(lat, lon float64) []endpointCandidate {
	return p.endpointCandidatesExcluding(lat, lon, 0)
}

func (p *PlannerData) endpointCandidatesExcluding(lat, lon float64, excludeRoute uint32) []endpointCandidate {
	all := make([]endpointCandidate, len(p.Nodes))
	all = all[:0]
	for i, n := range p.Nodes {
		if excludeRoute != 0 && n.RouteID == excludeRoute {
			continue
		}
		all = append(all, endpointCandidate{i, HaversineNM(lat, lon, n.Lat, n.Lon)})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].dist < all[j].dist })
	if len(all) == 0 {
		return nil
	}
	nearest := all[0].dist
	limit := nearest + 4
	if limit < 3 {
		limit = 3
	}
	if limit > 45 {
		limit = 45
	}
	out := make([]endpointCandidate, 0, 24)
	perRoute := map[uint32]int{}
	for _, c := range all {
		if c.dist > limit || len(out) >= 24 {
			break
		}
		rid := p.Nodes[c.node].RouteID
		if perRoute[rid] >= 2 {
			continue
		}
		perRoute[rid]++
		out = append(out, c)
	}
	return out
}

type pqItem struct {
	node  int
	g, f  float64
	index int
}
type routePQ []*pqItem

func (q routePQ) Len() int           { return len(q) }
func (q routePQ) Less(i, j int) bool { return q[i].f < q[j].f }
func (q routePQ) Swap(i, j int)      { q[i], q[j] = q[j], q[i]; q[i].index = i; q[j].index = j }
func (q *routePQ) Push(x any)        { it := x.(*pqItem); it.index = len(*q); *q = append(*q, it) }
func (q *routePQ) Pop() any {
	old := *q
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	*q = old[:n-1]
	return it
}

func (p *PlannerData) findGraphPath(start, end Position) ([]int, error) {
	return p.findGraphPathExcluding(start, end, 0)
}

func (p *PlannerData) findGraphPathExcluding(start, end Position, excludeRoute uint32) ([]int, error) {
	starts := p.endpointCandidatesExcluding(start.Lat, start.Lon, excludeRoute)
	ends := p.endpointCandidatesExcluding(end.Lat, end.Lon, excludeRoute)
	if len(starts) == 0 || len(ends) == 0 || starts[0].dist > 45 || ends[0].dist > 45 {
		return nil, fmt.Errorf("no historical route network was found close enough to one or both positions")
	}
	endCost := map[int]float64{}
	for _, c := range ends {
		endCost[c.node] = c.dist * 1.65
	}
	inf := math.Inf(1)
	gScore := make([]float64, len(p.Nodes))
	came := make([]int, len(p.Nodes))
	for i := range gScore {
		gScore[i] = inf
		came[i] = -1
	}
	q := &routePQ{}
	heap.Init(q)
	for _, c := range starts {
		g := c.dist * 1.65
		if g < gScore[c.node] {
			gScore[c.node] = g
			heap.Push(q, &pqItem{node: c.node, g: g, f: g + .72*HaversineNM(p.Nodes[c.node].Lat, p.Nodes[c.node].Lon, end.Lat, end.Lon)})
		}
	}
	best := inf
	bestNode := -1
	for q.Len() > 0 {
		it := heap.Pop(q).(*pqItem)
		if it.g != gScore[it.node] {
			continue
		}
		if it.f >= best {
			break
		}
		if tail, ok := endCost[it.node]; ok && it.g+tail < best {
			best = it.g + tail
			bestNode = it.node
		}
		for _, e := range p.Adj[it.node] {
			if excludeRoute != 0 && p.Nodes[e.To].RouteID == excludeRoute {
				continue
			}
			cost := e.DistanceNM
			if e.Connector {
				cost = cost*3.5 + .24
			} else {
				cost *= math.Max(.72, 1+e.CenterPenalty)
				if e.Count > 1 {
					cost /= 1 + math.Min(.12, .025*math.Log1p(float64(e.Count)))
				}
			}
			ng := it.g + cost
			if ng >= gScore[e.To] || ng >= best {
				continue
			}
			gScore[e.To] = ng
			came[e.To] = it.node
			h := .72 * HaversineNM(p.Nodes[e.To].Lat, p.Nodes[e.To].Lon, end.Lat, end.Lon)
			heap.Push(q, &pqItem{node: e.To, g: ng, f: ng + h})
		}
	}
	if bestNode < 0 {
		return nil, fmt.Errorf("the historical route corridors do not form a connected passage between these positions")
	}
	path := []int{}
	for n := bestNode; n >= 0; n = came[n] {
		path = append(path, n)
		if came[n] < 0 {
			break
		}
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	// Reject extreme network detours rather than presenting a convincing but irrelevant answer.
	network := 0.0
	for i := 1; i < len(path); i++ {
		network += HaversineNM(p.Nodes[path[i-1]].Lat, p.Nodes[path[i-1]].Lon, p.Nodes[path[i]].Lat, p.Nodes[path[i]].Lon)
	}
	direct := HaversineNM(start.Lat, start.Lon, end.Lat, end.Lon)
	if direct > 5 && network > direct*2.6+80 {
		return nil, fmt.Errorf("historical network solution is an excessive detour; no reliable route was generated")
	}
	return path, nil
}

func (p *PlannerData) centerPath(start, end Position, path []int, land *LandMask) []routePoint {
	pts := make([]routePoint, 0, len(path)+2)
	pts = append(pts, routePoint{Lat: start.Lat, Lon: start.Lon, Name: start.Name, Endpoint: true})
	for _, id := range path {
		n := p.Nodes[id]
		if len(pts) > 0 && HaversineNM(pts[len(pts)-1].Lat, pts[len(pts)-1].Lon, n.Lat, n.Lon) < .015 {
			if pts[len(pts)-1].Name == "" {
				pts[len(pts)-1].Name = n.Name
			}
			continue
		}
		pts = append(pts, routePoint{Lat: n.Lat, Lon: n.Lon, Name: n.Name, RouteID: n.RouteID, Endpoint: n.Endpoint})
	}
	if HaversineNM(pts[len(pts)-1].Lat, pts[len(pts)-1].Lon, end.Lat, end.Lon) > .015 {
		pts = append(pts, routePoint{Lat: end.Lat, Lon: end.Lon, Name: end.Name, Endpoint: true})
	} else {
		pts[len(pts)-1].Name = end.Name
		pts[len(pts)-1].Endpoint = true
	}
	original := append([]routePoint(nil), pts...)
	for i := 1; i+1 < len(pts); i++ {
		heading := InitialBearing(original[i-1].Lat, original[i-1].Lon, original[i+1].Lat, original[i+1].Lon)
		info := p.SegmentIndex.CorridorAt(original[i].Lat, original[i].Lon, heading, 1.2, original[i].RouteID)
		pts[i].Corr = info
		if !info.Centered || math.Abs(info.ShiftNM) < .008 || original[i].Endpoint {
			continue
		}
		b := heading * math.Pi / 180
		x := -math.Cos(b) * info.ShiftNM
		y := math.Sin(b) * info.ShiftNM
		lat, lon := FromLocalXYNM(x, y, original[i].Lat, original[i].Lon)
		if land != nil && (land.Contains(lat, lon) || land.SegmentTouchesLand(pts[i-1].Lat, pts[i-1].Lon, lat, lon, .08)) {
			pts[i].Corr.Centered = false
			continue
		}
		// Stay close to an actual same-direction track after movement.
		verify := p.SegmentIndex.CorridorAt(lat, lon, heading, math.Max(.3, info.WidthNM), 0)
		if verify.Consensus < 3 {
			pts[i].Corr.Centered = false
			continue
		}
		pts[i].Lat, pts[i].Lon = lat, lon
	}
	return simplifyRoutePoints(pts)
}

func simplifyRoutePoints(pts []routePoint) []routePoint {
	if len(pts) < 3 {
		return pts
	}
	// Remove connector duplicates and very small zig-zags first.
	out := []routePoint{pts[0]}
	for i := 1; i < len(pts); i++ {
		if HaversineNM(out[len(out)-1].Lat, out[len(out)-1].Lon, pts[i].Lat, pts[i].Lon) < .025 && !pts[i].Endpoint {
			if out[len(out)-1].Name == "" {
				out[len(out)-1].Name = pts[i].Name
			}
			continue
		}
		out = append(out, pts[i])
	}
	changed := true
	for changed && len(out) > 2 {
		changed = false
		next := []routePoint{out[0]}
		for i := 1; i+1 < len(out); i++ {
			a, b, c := out[i-1], out[i], out[i+1]
			d, _, _ := CrossTrackPointToSegmentNM(b.Lat, b.Lon, a.Lat, a.Lon, c.Lat, c.Lon)
			turn := AngleDiff(InitialBearing(a.Lat, a.Lon, b.Lat, b.Lon), InitialBearing(b.Lat, b.Lon, c.Lat, c.Lon))
			span := HaversineNM(a.Lat, a.Lon, c.Lat, c.Lon)
			narrow := b.Corr.Consensus >= 3 && b.Corr.WidthNM > 0 && b.Corr.WidthNM < .8
			removable := !b.Endpoint && b.Name == "" && d < .045 && turn < 2.5 && (!narrow || span < 1.0)
			if removable {
				changed = true
				continue
			}
			next = append(next, b)
		}
		next = append(next, out[len(out)-1])
		out = next
	}
	return out
}

func parseUTC(date, timeStr, zone string) (time.Time, error) {
	if date == "" || timeStr == "" {
		return time.Time{}, fmt.Errorf("date and time are required")
	}
	off := 0
	z := strings.TrimSpace(zone)
	if z != "" && z != "UTC" {
		if !strings.HasPrefix(z, "UTC") || len(z) < 6 {
			return time.Time{}, fmt.Errorf("unknown UTC offset: %s", zone)
		}
		sign := 1
		if z[3] == '-' {
			sign = -1
		}
		var h, m int
		if _, err := fmt.Sscanf(z[4:], "%d:%d", &h, &m); err != nil {
			return time.Time{}, err
		}
		off = sign * (h*3600 + m*60)
	}
	loc := time.FixedZone(z, off)
	t, err := time.ParseInLocation("2006-01-02 15:04", date+" "+timeStr, loc)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func (p *PlannerData) Generate(req RouteRequest, olex *CompositeOlex, land *LandMask) (*RoutePlan, error) {
	sd, err := p.Resolve(req.Start)
	if err != nil {
		return nil, err
	}
	ed, err := p.Resolve(req.End)
	if err != nil {
		return nil, err
	}
	start := Position{sd.Name, sd.Lat, sd.Lon}
	end := Position{ed.Name, ed.Lat, ed.Lon}
	dep, err := parseUTC(req.DepartureDate, req.DepartureTime, req.DepartureZone)
	if err != nil {
		return nil, fmt.Errorf("departure: %w", err)
	}
	arr, err := parseUTC(req.ArrivalDate, req.ArrivalTime, req.ArrivalZone)
	if err != nil {
		return nil, fmt.Errorf("arrival: %w", err)
	}
	if !arr.After(dep) {
		return nil, fmt.Errorf("arrival must be after departure")
	}
	path, err := p.findGraphPath(start, end)
	if err != nil {
		return nil, err
	}
	pts := p.centerPath(start, end, path, land)
	if len(pts) < 2 {
		return nil, fmt.Errorf("route generation produced too few waypoints")
	}
	total := 0.0
	for i := 1; i < len(pts); i++ {
		total += HaversineNM(pts[i-1].Lat, pts[i-1].Lon, pts[i].Lat, pts[i].Lon)
	}
	hours := arr.Sub(dep).Hours()
	speed := total / hours
	var eng *EngineConfig
	for i := range p.Configs {
		if speed <= p.Configs[i].SpeedKn+.05 {
			cfg := p.Configs[i]
			eng = &cfg
			break
		}
	}
	fuel := 0.0
	if eng != nil {
		fuel = eng.ConsumptionMTDay * hours / 24
	}
	radius := 1.0
	if speed < 7 {
		radius = .3
	} else if speed <= 12 {
		radius = .5
	}
	now := time.Now().UTC()
	plan := &RoutePlan{ID: fmt.Sprintf("%d", now.UnixNano()), RouteName: strings.TrimSpace(req.RouteName), CreatedUTC: now, UpdatedUTC: now, Revision: 1, DistanceNM: total, WaypointCount: len(pts), RequiredSpeedKn: speed, Engine: eng, FuelMT: fuel, DepartureUTC: dep, ArrivalUTC: arr, PlannedArrivalUTC: arr, EstimatedDurationHours: hours}
	if plan.RouteName == "" {
		plan.RouteName = start.Name + " - " + end.Name
	}
	elapsed := 0.0
	supportedNM, reviewNM, unsupportedNM, centeredNM := 0.0, 0.0, 0.0, 0.0
	trackCounts := []float64{}
	contributions := map[string]float64{}
	plan.Waypoints = make([]Waypoint, len(pts))
	for i := range pts {
		wp := Waypoint{Name: pts[i].Name, Lat: pts[i].Lat, Lon: pts[i].Lon, RadiusNM: radius, PortsideXTDNM: 0.1, StarboardXTDNM: 0.1, GeometryType: "Loxodrome"}
		if wp.Name == "" {
			wp.Name = fmt.Sprintf("WP%03d", i+1)
		}
		if i+1 < len(pts) {
			d := HaversineNM(pts[i].Lat, pts[i].Lon, pts[i+1].Lat, pts[i+1].Lon)
			course := InitialBearing(pts[i].Lat, pts[i].Lon, pts[i+1].Lat, pts[i+1].Lon)
			elapsed += d / speed
			assess := OlexAssessment{Status: "OFFICER CHECK", ReviewFraction: 1, Segments: []OlexSupportSegment{{StartFraction: 0, EndFraction: 1, Status: "OFFICER CHECK"}}}
			if olex != nil {
				assess = olex.AssessSegment(pts[i].Lat, pts[i].Lon, pts[i+1].Lat, pts[i+1].Lon, 5.7)
			}
			supportedNM += d * assess.SupportedFraction
			reviewNM += d * assess.ReviewFraction
			unsupportedNM += d * assess.UnsupportedFraction
			for name, f := range assess.AreaFractions {
				contributions[name] += d * f
			}
			midLat, midLon := InterpolateGC(pts[i].Lat, pts[i].Lon, pts[i+1].Lat, pts[i+1].Lon, .5)
			ci := p.SegmentIndex.CorridorAt(midLat, midLon, course, 1.2, 0)
			if ci.Centered {
				centeredNM += d
			}
			if ci.Consensus > 0 {
				trackCounts = append(trackCounts, float64(ci.Consensus))
			}
			comment := corridorComment(ci)
			if assess.Comment != "" {
				if comment != "" {
					comment += " "
				}
				comment += assess.Comment
			}
			eta := dep.Add(time.Duration(elapsed * float64(time.Hour)))
			wp.Leg = &Leg{Status: assess.Status, CourseDeg: course, DistanceNM: d, SpeedKn: speed, ETAUTC: eta, Comment: comment, OlexAreaName: assess.PrimaryArea, SupportSegments: assess.Segments}
		}
		plan.Waypoints[i] = wp
	}
	if total > 0 {
		plan.SupportedPct = 100 * supportedNM / total
		plan.ReviewPct = 100 * reviewNM / total
		plan.UnsupportedPct = 100 * unsupportedNM / total
		plan.CorridorCenteredPct = 100 * centeredNM / total
	}
	if len(trackCounts) > 0 {
		sort.Float64s(trackCounts)
		plan.MedianCorridorTracks = median(trackCounts)
	}
	for n, nm := range contributions {
		if nm > 0 {
			plan.OlexAreaNames = append(plan.OlexAreaNames, n)
			plan.OlexContributions = append(plan.OlexContributions, OlexContribution{Name: n, Percent: 100 * nm / total})
		}
	}
	sort.Slice(plan.OlexContributions, func(i, j int) bool { return plan.OlexContributions[i].Percent > plan.OlexContributions[j].Percent })
	sort.Strings(plan.OlexAreaNames)
	return plan, nil
}
func corridorComment(c CorridorInfo) string {
	if c.Split {
		return "CORRIDOR SPLIT DETECTED: route kept within the nearest historical lane; no averaging across separated alternatives."
	}
	if c.Centered {
		return fmt.Sprintf("AI CORRIDOR CENTER: centred on the robust median of %d independent historical tracks (estimated directional band %.2f NM).", c.Consensus, c.WidthNM)
	}
	if c.Consensus >= 3 {
		return fmt.Sprintf("HISTORICAL CORRIDOR: %d aligned tracks detected; no synthetic centre shift applied because the band is wide or ambiguous.", c.Consensus)
	}
	if c.Consensus > 0 {
		return fmt.Sprintf("LIMITED TRACK CONSENSUS: only %d aligned historical track(s); retain bridge review.", c.Consensus)
	}
	return "NO LOCAL TRACK CONSENSUS: route follows the connected historical network; bridge review required."
}
