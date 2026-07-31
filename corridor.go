package main

import (
	"math"
	"sort"
)

type TrackSegment struct {
	A, B                   int
	RouteID                uint32
	Lat1, Lon1, Lat2, Lon2 float64
	MidLat, MidLon         float64
	Bearing                float64
	LengthNM               float64
	CenterPenalty          float64
	Consensus              int
	WidthNM                float64
}

func NewTrackSegment(a, b int, rid uint32, n1, n2 Node) TrackSegment {
	return TrackSegment{A: a, B: b, RouteID: rid, Lat1: n1.Lat, Lon1: n1.Lon, Lat2: n2.Lat, Lon2: n2.Lon, MidLat: (n1.Lat + n2.Lat) / 2, MidLon: normalizeLon((n1.Lon + n2.Lon) / 2), Bearing: InitialBearing(n1.Lat, n1.Lon, n2.Lat, n2.Lon), LengthNM: HaversineNM(n1.Lat, n1.Lon, n2.Lat, n2.Lon)}
}

type SegmentGrid struct {
	CellDeg  float64
	Cells    map[int64][]int
	Segments []TrackSegment
}

func gridKey(a, b int32) int64 { return int64(a)<<32 | int64(uint32(b)) }
func NewSegmentGrid(segs []TrackSegment) *SegmentGrid {
	g := &SegmentGrid{CellDeg: .05, Cells: make(map[int64][]int), Segments: segs}
	for i, s := range segs {
		steps := int(math.Ceil(s.LengthNM / 2.0))
		if steps < 1 {
			steps = 1
		}
		seen := map[int64]bool{}
		for j := 0; j <= steps; j++ {
			lat, lon := InterpolateGC(s.Lat1, s.Lon1, s.Lat2, s.Lon2, float64(j)/float64(steps))
			a := int32(math.Floor(lat / g.CellDeg))
			b := int32(math.Floor(lon / g.CellDeg))
			k := gridKey(a, b)
			if !seen[k] {
				g.Cells[k] = append(g.Cells[k], i)
				seen[k] = true
			}
		}
	}
	return g
}
func (g *SegmentGrid) Nearby(lat, lon, radiusNM float64) []int {
	if g == nil {
		return nil
	}
	latR := int(math.Ceil(radiusNM/(60*g.CellDeg))) + 1
	c := math.Abs(math.Cos(lat * math.Pi / 180))
	if c < .05 {
		c = .05
	}
	lonR := int(math.Ceil(radiusNM/(60*g.CellDeg*c))) + 1
	ca := int32(math.Floor(lat / g.CellDeg))
	cb := int32(math.Floor(lon / g.CellDeg))
	seen := make(map[int]struct{})
	out := make([]int, 0, 64)
	for da := -latR; da <= latR; da++ {
		for db := -lonR; db <= lonR; db++ {
			for _, id := range g.Cells[gridKey(ca+int32(da), cb+int32(db))] {
				if _, ok := seen[id]; !ok {
					seen[id] = struct{}{}
					out = append(out, id)
				}
			}
		}
	}
	return out
}

type CorridorInfo struct {
	Consensus   int
	WidthNM     float64
	ShiftNM     float64
	Centered    bool
	Directional bool
	Split       bool
}
type offsetRec struct {
	route        uint32
	offset, dist float64
}

func (g *SegmentGrid) CorridorAt(lat, lon, heading, radiusNM float64, excludeRoute uint32) CorridorInfo {
	// First pass deliberately uses only traffic in the requested direction. This avoids
	// averaging opposing lanes or different sides of a split passage.
	info := g.corridorPass(lat, lon, heading, radiusNM, excludeRoute, false)
	if info.Consensus >= 3 {
		info.Directional = true
		return info
	}
	// Bidirectional consensus is only a fallback, and receives a tighter movement cap.
	info = g.corridorPass(lat, lon, heading, radiusNM, excludeRoute, true)
	info.Directional = false
	if info.Centered {
		cap := .12
		if math.Abs(info.ShiftNM) > cap {
			info.ShiftNM = math.Copysign(cap, info.ShiftNM)
		}
	}
	return info
}
func (g *SegmentGrid) corridorPass(lat, lon, heading, radiusNM float64, excludeRoute uint32, bidirectional bool) CorridorInfo {
	byRoute := map[uint32]offsetRec{}
	for _, id := range g.Nearby(lat, lon, radiusNM) {
		s := g.Segments[id]
		// Never let the source route vote for its own centreline. This prevents a
		// single historical track from manufacturing apparent consensus and makes
		// the score represent independent voyages only.
		if excludeRoute != 0 && s.RouteID == excludeRoute {
			continue
		}
		diff := AngleDiff(s.Bearing, heading)
		if bidirectional {
			if diff > 35 && math.Abs(diff-180) > 35 {
				continue
			}
		} else if diff > 35 {
			continue
		}
		dist, signed, _ := CrossTrackPointToSegmentNM(lat, lon, s.Lat1, s.Lon1, s.Lat2, s.Lon2)
		if dist > radiusNM {
			continue
		}
		old, ok := byRoute[s.RouteID]
		if !ok || dist < old.dist {
			byRoute[s.RouteID] = offsetRec{s.RouteID, signed, dist}
		}
	}
	vals := make([]float64, 0, len(byRoute))
	for _, r := range byRoute {
		vals = append(vals, r.offset)
	}
	if len(vals) < 3 {
		return CorridorInfo{Consensus: len(vals)}
	}
	sort.Float64s(vals)
	// Split cross-track alternatives at meaningful gaps, then keep the cluster nearest
	// the current path. This prevents a median line being drawn through an island/hazard.
	gapThreshold := .22
	clusters := [][]float64{}
	start := 0
	for i := 1; i < len(vals); i++ {
		if vals[i]-vals[i-1] > gapThreshold {
			clusters = append(clusters, append([]float64(nil), vals[start:i]...))
			start = i
		}
	}
	clusters = append(clusters, append([]float64(nil), vals[start:]...))
	best := clusters[0]
	bestAbs := math.Abs(median(best))
	for _, c := range clusters[1:] {
		m := math.Abs(median(c))
		if (len(c) >= 3 && len(best) < 3) || ((len(c) >= 3) == (len(best) >= 3) && m < bestAbs) {
			best = c
			bestAbs = m
		}
	}
	split := len(clusters) > 1
	if len(best) < 3 {
		return CorridorInfo{Consensus: len(best), Split: split}
	}
	med := median(best)
	q10 := quantile(best, .10)
	q90 := quantile(best, .90)
	width := math.Max(.06, q90-q10)
	// Multi-route consensus is used only in compact bands. Wide/open-water bundles are
	// route-choice evidence, not a licence to manufacture a new centreline.
	centered := width <= 1.50 && math.Abs(med) <= .70
	shift := med
	cap := math.Min(.35, math.Max(.04, .45*width))
	if math.Abs(shift) > cap {
		shift = math.Copysign(cap, shift)
	}
	if split && bestAbs > .18 {
		centered = false
		shift = 0
	}
	return CorridorInfo{Consensus: len(best), WidthNM: width, ShiftNM: shift, Centered: centered, Split: split}
}
func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}
func quantile(v []float64, q float64) float64 {
	if len(v) == 0 {
		return 0
	}
	if q <= 0 {
		return v[0]
	}
	if q >= 1 {
		return v[len(v)-1]
	}
	x := q * float64(len(v)-1)
	i := int(math.Floor(x))
	f := x - float64(i)
	if i+1 >= len(v) {
		return v[i]
	}
	return v[i]*(1-f) + v[i+1]*f
}

func (p *PlannerData) precomputeCorridorStats() {
	for i := range p.Segments {
		s := &p.Segments[i]
		info := p.SegmentIndex.CorridorAt(s.MidLat, s.MidLon, s.Bearing, 1.2, s.RouteID)
		s.Consensus = info.Consensus
		s.WidthNM = info.WidthNM
		s.CenterPenalty = corridorPenalty(info)
	}
}

func corridorPenalty(info CorridorInfo) float64 {
	if info.Consensus < 3 {
		return .10
	}
	// Penalise an edge if it sits away from the robust centre of its own
	// directional track bundle. Narrow passages receive the strongest weighting.
	norm := math.Abs(info.ShiftNM) / math.Max(.06, info.WidthNM/2)
	strength := .18
	if info.WidthNM <= .35 {
		strength = 1.8
	} else if info.WidthNM <= .8 {
		strength = .9
	} else if info.WidthNM <= 1.5 {
		strength = .42
	}
	penalty := math.Min(2.5, strength*norm*norm) - math.Min(.14, .018*float64(info.Consensus-2))
	if penalty < -.15 {
		penalty = -.15
	}
	return penalty
}
