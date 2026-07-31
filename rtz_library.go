package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type RTZWaypoint struct {
	Name string
	Lat  float64
	Lon  float64
}

type ParsedRTZRoute struct {
	Name      string
	Waypoints []RTZWaypoint
}

type RTZAreaInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	File          string `json:"file,omitempty"`
	OriginalName  string `json:"originalName,omitempty"`
	Disabled      bool   `json:"disabled"`
	RouteCount    int    `json:"routeCount"`
	WaypointCount int    `json:"waypointCount"`
	SizeBytes     int64  `json:"sizeBytes"`
	ImportedUTC   string `json:"importedUTC"`
	SHA256        string `json:"sha256,omitempty"`
}

type storedRTZ struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	File          string `json:"file"`
	OriginalName  string `json:"originalName,omitempty"`
	Disabled      bool   `json:"disabled"`
	RouteCount    int    `json:"routeCount"`
	WaypointCount int    `json:"waypointCount"`
	SizeBytes     int64  `json:"sizeBytes"`
	ImportedUTC   string `json:"importedUTC"`
	SHA256        string `json:"sha256"`
}

type rtzLibraryState struct {
	Imports []storedRTZ `json:"imports"`
}

type RTZLibrary struct {
	mu    sync.RWMutex
	dir   string
	state rtzLibraryState
}

func NewRTZLibrary(dir string) (*RTZLibrary, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	l := &RTZLibrary{dir: dir}
	if b, err := os.ReadFile(filepath.Join(dir, "rtz_library.json")); err == nil {
		if err := json.Unmarshal(b, &l.state); err != nil {
			return nil, fmt.Errorf("read RTZ library manifest: %w", err)
		}
	}
	l.cleanMissing()
	if err := l.saveLocked(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *RTZLibrary) cleanMissing() {
	out := l.state.Imports[:0]
	for _, x := range l.state.Imports {
		if _, err := os.Stat(filepath.Join(l.dir, x.File)); err == nil {
			out = append(out, x)
		}
	}
	l.state.Imports = out
}

func (l *RTZLibrary) saveLocked() error {
	b, err := json.MarshalIndent(l.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(l.dir, "rtz_library.json.tmp")
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(l.dir, "rtz_library.json"))
}

func (l *RTZLibrary) Areas() []RTZAreaInfo {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]RTZAreaInfo, 0, len(l.state.Imports))
	for _, x := range l.state.Imports {
		out = append(out, RTZAreaInfo{
			ID: x.ID, Name: x.Name, File: x.File, OriginalName: x.OriginalName,
			Disabled: x.Disabled, RouteCount: x.RouteCount, WaypointCount: x.WaypointCount,
			SizeBytes: x.SizeBytes, ImportedUTC: x.ImportedUTC, SHA256: x.SHA256,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func attrValue(attrs []xml.Attr, names ...string) string {
	// Callers pass preferred attribute names first (for example name before id).
	// Preserve that preference regardless of the attribute order in the XML.
	for _, n := range names {
		for _, a := range attrs {
			if strings.EqualFold(a.Name.Local, n) {
				return strings.TrimSpace(a.Value)
			}
		}
	}
	return ""
}

func parseRTZWaypoint(dec *xml.Decoder, start xml.StartElement) (RTZWaypoint, error) {
	wp := RTZWaypoint{Name: attrValue(start.Attr, "name", "id"), Lat: math.NaN(), Lon: math.NaN()}
	for {
		tok, err := dec.Token()
		if err != nil {
			return wp, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if strings.EqualFold(t.Name.Local, "position") {
				lat, e1 := strconv.ParseFloat(attrValue(t.Attr, "lat", "latitude"), 64)
				lon, e2 := strconv.ParseFloat(attrValue(t.Attr, "lon", "longitude"), 64)
				if e1 == nil && e2 == nil {
					wp.Lat, wp.Lon = lat, lon
				}
			}
		case xml.EndElement:
			if t.Name == start.Name {
				return wp, nil
			}
		}
	}
}

func ParseRTZ(r io.Reader) ([]ParsedRTZRoute, error) {
	dec := xml.NewDecoder(r)
	var current ParsedRTZRoute
	var routes []ParsedRTZRoute
	seenRoute := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("RTZ XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch strings.ToLower(t.Name.Local) {
			case "route":
				if seenRoute && len(current.Waypoints) >= 2 {
					routes = append(routes, current)
				}
				current = ParsedRTZRoute{}
				seenRoute = true
			case "routeinfo":
				if name := attrValue(t.Attr, "routeName", "name"); name != "" {
					current.Name = name
				}
			case "waypoint":
				wp, err := parseRTZWaypoint(dec, t)
				if err != nil {
					return nil, fmt.Errorf("RTZ waypoint: %w", err)
				}
				if wp.Lat < -90 || wp.Lat > 90 || wp.Lon < -180 || wp.Lon > 180 || math.IsNaN(wp.Lat) || math.IsNaN(wp.Lon) {
					continue
				}
				current.Waypoints = append(current.Waypoints, wp)
			}
		case xml.EndElement:
			if strings.EqualFold(t.Name.Local, "route") && len(current.Waypoints) >= 2 {
				routes = append(routes, current)
				current = ParsedRTZRoute{}
				seenRoute = false
			}
		}
	}
	if len(current.Waypoints) >= 2 {
		routes = append(routes, current)
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("no RTZ route containing at least two valid waypoints was found")
	}
	for i := range routes {
		if strings.TrimSpace(routes[i].Name) == "" {
			routes[i].Name = fmt.Sprintf("Imported RTZ Route %d", i+1)
		}
	}
	return routes, nil
}

func parseRTZFile(path string) ([]ParsedRTZRoute, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseRTZ(f)
}

func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func sanitizeFileStem(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		s = "RTZ_Route"
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
}

func (l *RTZLibrary) uniqueNameLocked(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Imported RTZ"
	}
	used := map[string]bool{}
	for _, x := range l.state.Imports {
		used[strings.ToLower(x.Name)] = true
	}
	if !used[strings.ToLower(name)] {
		return name
	}
	base := name
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s (%d)", base, i)
		if !used[strings.ToLower(candidate)] {
			return candidate
		}
	}
}

func (l *RTZLibrary) ImportFile(path, displayName, originalName string) (RTZAreaInfo, error) {
	routes, err := parseRTZFile(path)
	if err != nil {
		return RTZAreaInfo{}, err
	}
	hash, size, err := fileSHA256(path)
	if err != nil {
		return RTZAreaInfo{}, err
	}
	waypoints := 0
	for _, r := range routes {
		waypoints += len(r.Waypoints)
	}
	if strings.TrimSpace(displayName) == "" {
		displayName = routes[0].Name
		if len(routes) > 1 {
			displayName = strings.TrimSuffix(filepath.Base(originalName), filepath.Ext(originalName))
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, x := range l.state.Imports {
		if strings.EqualFold(x.SHA256, hash) {
			return RTZAreaInfo{}, fmt.Errorf("this RTZ file is already stored as %s", x.Name)
		}
	}
	name := l.uniqueNameLocked(displayName)
	id := hash[:24]
	fileName := fmt.Sprintf("%s_%s.rtz", sanitizeFileStem(name), id[:12])
	target := filepath.Join(l.dir, fileName)
	tmp := target + ".tmp"
	in, err := os.Open(path)
	if err != nil {
		return RTZAreaInfo{}, err
	}
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		_ = in.Close()
		return RTZAreaInfo{}, err
	}
	_, copyErr := io.CopyBuffer(out, in, make([]byte, 1<<20))
	closeOutErr := out.Close()
	closeInErr := in.Close()
	if copyErr != nil || closeOutErr != nil || closeInErr != nil {
		_ = os.Remove(tmp)
		if copyErr != nil {
			return RTZAreaInfo{}, copyErr
		}
		if closeOutErr != nil {
			return RTZAreaInfo{}, closeOutErr
		}
		return RTZAreaInfo{}, closeInErr
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return RTZAreaInfo{}, err
	}
	entry := storedRTZ{
		ID: id, Name: name, File: fileName, OriginalName: filepath.Base(originalName),
		RouteCount: len(routes), WaypointCount: waypoints, SizeBytes: size,
		ImportedUTC: time.Now().UTC().Format(time.RFC3339), SHA256: hash,
	}
	l.state.Imports = append(l.state.Imports, entry)
	if err := l.saveLocked(); err != nil {
		l.state.Imports = l.state.Imports[:len(l.state.Imports)-1]
		_ = os.Remove(target)
		return RTZAreaInfo{}, err
	}
	return RTZAreaInfo{ID: entry.ID, Name: entry.Name, File: entry.File, OriginalName: entry.OriginalName, RouteCount: entry.RouteCount, WaypointCount: entry.WaypointCount, SizeBytes: entry.SizeBytes, ImportedUTC: entry.ImportedUTC, SHA256: entry.SHA256}, nil
}

func (l *RTZLibrary) Rename(id, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("new RTZ library name is empty")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, x := range l.state.Imports {
		if x.ID != id && strings.EqualFold(x.Name, newName) {
			return fmt.Errorf("an RTZ entry named %s already exists", newName)
		}
	}
	for i := range l.state.Imports {
		if l.state.Imports[i].ID == id {
			l.state.Imports[i].Name = newName
			return l.saveLocked()
		}
	}
	return fmt.Errorf("RTZ library entry not found")
}

func (l *RTZLibrary) Toggle(id string, enabled bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.state.Imports {
		if l.state.Imports[i].ID == id {
			l.state.Imports[i].Disabled = !enabled
			return l.saveLocked()
		}
	}
	return fmt.Errorf("RTZ library entry not found")
}

func (l *RTZLibrary) Remove(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, x := range l.state.Imports {
		if x.ID != id {
			continue
		}
		if err := os.Remove(filepath.Join(l.dir, x.File)); err != nil && !os.IsNotExist(err) {
			return err
		}
		l.state.Imports = append(l.state.Imports[:i], l.state.Imports[i+1:]...)
		return l.saveLocked()
	}
	return fmt.Errorf("RTZ library entry not found")
}

func (l *RTZLibrary) importEntries() []storedRTZ {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]storedRTZ, 0, len(l.state.Imports))
	for _, x := range l.state.Imports {
		if !x.Disabled {
			out = append(out, x)
		}
	}
	return out
}

func clonePlannerBase(base *PlannerData) *PlannerData {
	p := &PlannerData{
		Meta:         map[string]string{},
		Configs:      append([]EngineConfig(nil), base.Configs...),
		Destinations: append([]Destination(nil), base.Destinations...),
		Nodes:        append([]Node(nil), base.Nodes...),
		RawEdges:     append([]RawEdge(nil), base.RawEdges...),
	}
	for k, v := range base.Meta {
		p.Meta[k] = v
	}
	return p
}

type nodeSpatialGrid struct {
	cellDeg float64
	cells   map[int64][]int
	nodes   *[]Node
}

func newNodeSpatialGrid(nodes *[]Node) *nodeSpatialGrid {
	g := &nodeSpatialGrid{cellDeg: 0.2, cells: map[int64][]int{}, nodes: nodes}
	for i := range *nodes {
		g.add(i)
	}
	return g
}

func (g *nodeSpatialGrid) key(lat, lon float64) int64 {
	return gridKey(int32(math.Floor((lat+90)/g.cellDeg)), int32(math.Floor((normalizeLon(lon)+180)/g.cellDeg)))
}

func (g *nodeSpatialGrid) add(i int) {
	n := (*g.nodes)[i]
	k := g.key(n.Lat, n.Lon)
	g.cells[k] = append(g.cells[k], i)
}

func (g *nodeSpatialGrid) nearest(lat, lon float64, excludeRoute uint32, radiusNM float64, limit int) []endpointCandidate {
	if limit <= 0 {
		limit = 4
	}
	latDeg := radiusNM / 60
	cos := math.Abs(math.Cos(lat * math.Pi / 180))
	if cos < 0.08 {
		cos = 0.08
	}
	lonDeg := radiusNM / (60 * cos)
	minLat, maxLat := lat-latDeg, lat+latDeg
	minLon, maxLon := lon-lonDeg, lon+lonDeg
	minA := int32(math.Floor((minLat + 90) / g.cellDeg))
	maxA := int32(math.Floor((maxLat + 90) / g.cellDeg))
	minB := int32(math.Floor((normalizeLon(minLon) + 180) / g.cellDeg))
	maxB := int32(math.Floor((normalizeLon(maxLon) + 180) / g.cellDeg))
	// Imported RTZ routes are expected to be regional. For an antimeridian-spanning
	// search, fall back to all longitude cells rather than silently missing a match.
	if maxB < minB || maxB-minB > int32(360/g.cellDeg) {
		minB, maxB = 0, int32(360/g.cellDeg)
	}
	seen := map[int]bool{}
	var out []endpointCandidate
	for a := minA; a <= maxA; a++ {
		for b := minB; b <= maxB; b++ {
			for _, i := range g.cells[gridKey(a, b)] {
				if seen[i] {
					continue
				}
				seen[i] = true
				n := (*g.nodes)[i]
				if n.RouteID == excludeRoute {
					continue
				}
				d := HaversineNM(lat, lon, n.Lat, n.Lon)
				if d <= radiusNM {
					out = append(out, endpointCandidate{node: i, dist: d})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].dist < out[j].dist })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func addDestinationUnique(p *PlannerData, name string, lat, lon float64, category string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	for _, d := range p.Destinations {
		if strings.EqualFold(d.Name, name) && HaversineNM(d.Lat, d.Lon, lat, lon) < 0.05 {
			return
		}
	}
	base := name
	used := map[string]bool{}
	for _, d := range p.Destinations {
		used[strings.ToLower(d.Name)] = true
	}
	if used[strings.ToLower(name)] {
		for i := 2; ; i++ {
			candidate := fmt.Sprintf("%s (%d)", base, i)
			if !used[strings.ToLower(candidate)] {
				name = candidate
				break
			}
		}
	}
	p.Destinations = append(p.Destinations, Destination{Name: name, Lat: lat, Lon: lon, Category: category, Count: 1})
}

func genericWaypointName(name string) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	if n == "" {
		return true
	}
	if strings.HasPrefix(n, "WP") || strings.HasPrefix(n, "WPT") || strings.HasPrefix(n, "WAYPOINT") {
		return true
	}
	return false
}

func (l *RTZLibrary) BuildPlanner(base *PlannerData) (*PlannerData, error) {
	p := clonePlannerBase(base)
	entries := l.importEntries()
	maxRouteID := uint32(0)
	routeIDs := map[uint32]bool{}
	for _, n := range p.Nodes {
		if n.RouteID > maxRouteID {
			maxRouteID = n.RouteID
		}
		routeIDs[n.RouteID] = true
	}
	grid := newNodeSpatialGrid(&p.Nodes)
	connectorPairs := map[uint64]bool{}
	for _, e := range p.RawEdges {
		if e.Connector {
			connectorPairs[directedPairKey(e.From, e.To)] = true
		}
	}
	importedRoutes := 0
	importedWaypoints := 0
	for _, entry := range entries {
		routes, err := parseRTZFile(filepath.Join(l.dir, entry.File))
		if err != nil {
			return nil, fmt.Errorf("load stored RTZ %s: %w", entry.Name, err)
		}
		for routeIndex, route := range routes {
			maxRouteID++
			rid := maxRouteID
			routeName := strings.TrimSpace(route.Name)
			if routeName == "" {
				routeName = entry.Name
			}
			if len(routes) > 1 && strings.EqualFold(routeName, entry.Name) {
				routeName = fmt.Sprintf("%s — Route %d", entry.Name, routeIndex+1)
			}
			startIndex := len(p.Nodes)
			for i, wp := range route.Waypoints {
				name := strings.TrimSpace(wp.Name)
				endpoint := i == 0 || i == len(route.Waypoints)-1
				p.Nodes = append(p.Nodes, Node{Lat: wp.Lat, Lon: wp.Lon, Name: name, RouteID: rid, Sequence: uint32(i), Endpoint: endpoint})
			}
			for i := 0; i+1 < len(route.Waypoints); i++ {
				a := startIndex + i
				b := a + 1
				d := HaversineNM(p.Nodes[a].Lat, p.Nodes[a].Lon, p.Nodes[b].Lat, p.Nodes[b].Lon)
				if d > 0.002 && d < 600 {
					p.RawEdges = append(p.RawEdges, RawEdge{From: a, To: b, DistanceNM: d, Count: 1, Connector: false})
				}
			}
			startName := route.Waypoints[0].Name
			if genericWaypointName(startName) {
				startName = routeName + " — Start"
			}
			endName := route.Waypoints[len(route.Waypoints)-1].Name
			if genericWaypointName(endName) {
				endName = routeName + " — End"
			}
			addDestinationUnique(p, startName, route.Waypoints[0].Lat, route.Waypoints[0].Lon, "Imported RTZ")
			addDestinationUnique(p, endName, route.Waypoints[len(route.Waypoints)-1].Lat, route.Waypoints[len(route.Waypoints)-1].Lon, "Imported RTZ")
			for i := startIndex; i < len(p.Nodes); i++ {
				n := p.Nodes[i]
				radius := 1.25
				limit := 3
				if n.Endpoint {
					radius = 8
					limit = 6
				}
				for _, c := range grid.nearest(n.Lat, n.Lon, rid, radius, limit) {
					if c.dist < 0.002 {
						continue
					}
					for _, pair := range [][2]int{{i, c.node}, {c.node, i}} {
						key := directedPairKey(pair[0], pair[1])
						if connectorPairs[key] {
							continue
						}
						connectorPairs[key] = true
						p.RawEdges = append(p.RawEdges, RawEdge{From: pair[0], To: pair[1], DistanceNM: c.dist, Count: 1, Connector: true})
					}
				}
			}
			for i := startIndex; i < len(p.Nodes); i++ {
				grid.add(i)
			}
			importedRoutes++
			importedWaypoints += len(route.Waypoints)
			routeIDs[rid] = true
		}
	}
	p.Meta["uploaded_rtz_files"] = strconv.Itoa(len(entries))
	p.Meta["uploaded_rtz_routes"] = strconv.Itoa(importedRoutes)
	p.Meta["uploaded_rtz_waypoints"] = strconv.Itoa(importedWaypoints)
	p.Meta["route_count"] = strconv.Itoa(len(routeIDs))
	p.Meta["cleaned_routes"] = strconv.Itoa(len(routeIDs))
	p.buildSegmentsAndGraph()
	return p, nil
}

func (l *RTZLibrary) ImportPath(path string) ([]RTZAreaInfo, []string) {
	path = strings.TrimSpace(strings.Trim(path, `"`))
	var files []string
	st, err := os.Stat(path)
	if err != nil {
		return nil, []string{err.Error()}
	}
	if st.Mode().IsRegular() {
		files = append(files, path)
	} else if st.IsDir() {
		_ = filepath.WalkDir(path, func(p string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(d.Name()))
			if ext == ".rtz" || ext == ".xml" {
				files = append(files, p)
			}
			return nil
		})
	}
	sort.Strings(files)
	var imported []RTZAreaInfo
	var failures []string
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
		area, err := l.ImportFile(file, name, filepath.Base(file))
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", filepath.Base(file), err))
			continue
		}
		imported = append(imported, area)
	}
	if len(files) == 0 {
		failures = append(failures, "no .rtz or .xml files were found")
	}
	return imported, failures
}
