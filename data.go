package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

type EngineConfig struct {
	Name             string  `json:"name"`
	MaxRPM           float64 `json:"maxRPM"`
	SpeedKn          float64 `json:"speedKn"`
	ConsumptionMTDay float64 `json:"consumptionMtDay"`
	LoadPct          float64 `json:"loadPct"`
}

type Destination struct {
	Name     string   `json:"name"`
	Aliases  []string `json:"aliases,omitempty"`
	Lat      float64  `json:"lat"`
	Lon      float64  `json:"lon"`
	Category string   `json:"category"`
	Count    uint32   `json:"count"`
}

type Node struct {
	Lat, Lon float64
	Name     string
	RouteID  uint32
	Sequence uint32
	Endpoint bool
}

type Edge struct {
	To            int
	Historical    bool
	Connector     bool
	Count         uint32
	DistanceNM    float64
	CenterPenalty float64
	Consensus     int
	WidthNM       float64
}

type RawEdge struct {
	From, To   int
	DistanceNM float64
	Count      uint32
	Connector  bool
}

type PlannerData struct {
	Meta         map[string]string
	Configs      []EngineConfig
	Destinations []Destination
	Nodes        []Node
	RawEdges     []RawEdge
	Adj          [][]Edge
	Segments     []TrackSegment
	SegmentIndex *SegmentGrid
}

type binReader struct{ r *bufio.Reader }

func (b *binReader) u32() (uint32, error) {
	var v uint32
	err := binary.Read(b.r, binary.LittleEndian, &v)
	return v, err
}
func (b *binReader) f32() (float32, error) {
	var v float32
	err := binary.Read(b.r, binary.LittleEndian, &v)
	return v, err
}
func (b *binReader) f64() (float64, error) {
	var v float64
	err := binary.Read(b.r, binary.LittleEndian, &v)
	return v, err
}
func (b *binReader) str() (string, error) {
	n, err := b.u32()
	if err != nil {
		return "", err
	}
	if n > 64<<20 {
		return "", fmt.Errorf("invalid string length %d", n)
	}
	buf := make([]byte, n)
	if _, err = io.ReadFull(b.r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func LoadPlannerData(compressed []byte) (*PlannerData, error) {
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("planner gzip: %w", err)
	}
	defer gz.Close()
	br := &binReader{bufio.NewReaderSize(gz, 1<<20)}
	magic := make([]byte, 8)
	if _, err = io.ReadFull(br.r, magic); err != nil {
		return nil, err
	}
	if string(magic) != "LRPDATA1" {
		return nil, fmt.Errorf("invalid planner data magic")
	}
	jn, err := br.u32()
	if err != nil {
		return nil, err
	}
	jb := make([]byte, jn)
	if _, err = io.ReadFull(br.r, jb); err != nil {
		return nil, err
	}
	meta := map[string]string{}
	if err = json.Unmarshal(jb, &meta); err != nil {
		return nil, err
	}
	p := &PlannerData{Meta: meta}
	ncfg, err := br.u32()
	if err != nil {
		return nil, err
	}
	for i := uint32(0); i < ncfg; i++ {
		name, e := br.str()
		if e != nil {
			return nil, e
		}
		vals := make([]float32, 4)
		for j := range vals {
			vals[j], e = br.f32()
			if e != nil {
				return nil, e
			}
		}
		p.Configs = append(p.Configs, EngineConfig{name, float64(vals[0]), float64(vals[1]), float64(vals[2]), float64(vals[3])})
	}
	nd, err := br.u32()
	if err != nil {
		return nil, err
	}
	p.Destinations = make([]Destination, 0, nd)
	for i := uint32(0); i < nd; i++ {
		name, e := br.str()
		if e != nil {
			return nil, e
		}
		aliasJSON, e := br.str()
		if e != nil {
			return nil, e
		}
		lat, e := br.f64()
		if e != nil {
			return nil, e
		}
		lon, e := br.f64()
		if e != nil {
			return nil, e
		}
		cat, e := br.str()
		if e != nil {
			return nil, e
		}
		count, e := br.u32()
		if e != nil {
			return nil, e
		}
		var aliases []string
		_ = json.Unmarshal([]byte(aliasJSON), &aliases)
		p.Destinations = append(p.Destinations, Destination{name, aliases, lat, lon, cat, count})
	}
	nn, err := br.u32()
	if err != nil {
		return nil, err
	}
	p.Nodes = make([]Node, nn)
	for i := uint32(0); i < nn; i++ {
		lat, e := br.f64()
		if e != nil {
			return nil, e
		}
		lon, e := br.f64()
		if e != nil {
			return nil, e
		}
		name, e := br.str()
		if e != nil {
			return nil, e
		}
		rid, e := br.u32()
		if e != nil {
			return nil, e
		}
		seq, e := br.u32()
		if e != nil {
			return nil, e
		}
		flag, e := br.r.ReadByte()
		if e != nil {
			return nil, e
		}
		p.Nodes[i] = Node{lat, lon, name, rid, seq, flag != 0}
	}
	ne, err := br.u32()
	if err != nil {
		return nil, err
	}
	p.RawEdges = make([]RawEdge, 0, ne)
	for i := uint32(0); i < ne; i++ {
		from, e := br.u32()
		if e != nil {
			return nil, e
		}
		to, e := br.u32()
		if e != nil {
			return nil, e
		}
		dist, e := br.f32()
		if e != nil {
			return nil, e
		}
		count, e := br.u32()
		if e != nil {
			return nil, e
		}
		conn, e := br.r.ReadByte()
		if e != nil {
			return nil, e
		}
		for j := 0; j < 4; j++ {
			if _, e = br.f32(); e != nil {
				return nil, e
			}
		}
		if int(from) >= len(p.Nodes) || int(to) >= len(p.Nodes) {
			continue
		}
		realDist := HaversineNM(p.Nodes[from].Lat, p.Nodes[from].Lon, p.Nodes[to].Lat, p.Nodes[to].Lon)
		if math.IsNaN(realDist) || realDist <= 0 {
			realDist = float64(dist)
		}
		p.RawEdges = append(p.RawEdges, RawEdge{int(from), int(to), realDist, count, conn != 0})
	}
	p.buildSegmentsAndGraph()
	return p, nil
}

func (p *PlannerData) buildSegmentsAndGraph() {
	// One canonical segment for each consecutive pair in a historical route.
	byRoute := map[uint32][]int{}
	for i, n := range p.Nodes {
		byRoute[n.RouteID] = append(byRoute[n.RouteID], i)
	}
	for rid, ids := range byRoute {
		sort.Slice(ids, func(i, j int) bool { return p.Nodes[ids[i]].Sequence < p.Nodes[ids[j]].Sequence })
		for i := 0; i+1 < len(ids); i++ {
			a, b := ids[i], ids[i+1]
			if p.Nodes[b].Sequence != p.Nodes[a].Sequence+1 {
				continue
			}
			d := HaversineNM(p.Nodes[a].Lat, p.Nodes[a].Lon, p.Nodes[b].Lat, p.Nodes[b].Lon)
			if d < 0.002 || d > 600 {
				continue
			}
			p.Segments = append(p.Segments, NewTrackSegment(a, b, rid, p.Nodes[a], p.Nodes[b]))
		}
	}
	p.SegmentIndex = NewSegmentGrid(p.Segments)
	p.precomputeCorridorStats()
	type stat struct {
		pen, width float64
		n          int
	}
	// Corridor evidence is directional. A historical track that is well centred
	// northbound must not automatically receive the same score southbound, where
	// the accepted traffic lane may be on the other side of a narrow passage.
	stats := map[uint64]stat{}
	for _, s := range p.Segments {
		stats[directedPairKey(s.A, s.B)] = stat{s.CenterPenalty, s.WidthNM, s.Consensus}
		reverse := p.SegmentIndex.CorridorAt(s.MidLat, s.MidLon, normalizeBearing(s.Bearing+180), 1.2, s.RouteID)
		stats[directedPairKey(s.B, s.A)] = stat{corridorPenalty(reverse), reverse.WidthNM, reverse.Consensus}
	}
	p.Adj = make([][]Edge, len(p.Nodes))
	for _, e := range p.RawEdges {
		st := stats[directedPairKey(e.From, e.To)]
		historical := !e.Connector && p.Nodes[e.From].RouteID == p.Nodes[e.To].RouteID
		p.Adj[e.From] = append(p.Adj[e.From], Edge{To: e.To, Historical: historical, Connector: e.Connector, Count: e.Count, DistanceNM: e.DistanceNM, CenterPenalty: st.pen, Consensus: st.n, WidthNM: st.width})
	}
}

func directedPairKey(a, b int) uint64 {
	return uint64(uint32(a))<<32 | uint64(uint32(b))
}

func (p *PlannerData) SearchDestinations(q string, limit int) []Destination {
	q = strings.ToLower(strings.TrimSpace(q))
	if limit <= 0 {
		limit = 18
	}
	type ranked struct {
		d     Destination
		score int
	}
	rows := make([]ranked, 0)
	for _, d := range p.Destinations {
		n := strings.ToLower(d.Name)
		score := -1
		switch {
		case n == q:
			score = 1000000
		case strings.HasPrefix(n, q):
			score = 500000
		case strings.Contains(n, q):
			score = 200000
		}
		if score < 0 {
			for _, a := range d.Aliases {
				al := strings.ToLower(a)
				if al == q {
					score = 900000
					break
				}
				if strings.Contains(al, q) && score < 100000 {
					score = 100000
				}
			}
		}
		if score >= 0 {
			score += int(d.Count) * 10
			rows = append(rows, ranked{d, score})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].score == rows[j].score {
			return rows[i].d.Name < rows[j].d.Name
		}
		return rows[i].score > rows[j].score
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]Destination, len(rows))
	for i := range rows {
		out[i] = rows[i].d
	}
	return out
}

func (p *PlannerData) Resolve(input string) (Destination, error) {
	if lat, lon, ok := ParseCoordinates(input); ok {
		return Destination{Name: fmt.Sprintf("%.5f, %.5f", lat, lon), Lat: lat, Lon: lon, Category: "Coordinates"}, nil
	}
	q := strings.TrimSpace(input)
	if q == "" {
		return Destination{}, fmt.Errorf("position is empty")
	}
	rows := p.SearchDestinations(q, 30)
	if len(rows) == 0 {
		return Destination{}, fmt.Errorf("destination or coordinates not found: %s", q)
	}
	// Prefer exact name/alias, then highest-frequency match.
	for _, d := range rows {
		if strings.EqualFold(d.Name, q) {
			return d, nil
		}
		for _, a := range d.Aliases {
			if strings.EqualFold(a, q) {
				return d, nil
			}
		}
	}
	return rows[0], nil
}
