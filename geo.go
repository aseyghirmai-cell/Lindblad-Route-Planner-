package main

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const earthRadiusNM = 3440.065

func HaversineNM(lat1, lon1, lat2, lon2 float64) float64 {
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp := (lat2 - lat1) * math.Pi / 180
	dl := normalizeLon(lon2-lon1) * math.Pi / 180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	if a > 1 {
		a = 1
	}
	return 2 * earthRadiusNM * math.Asin(math.Sqrt(a))
}
func normalizeLon(v float64) float64 {
	for v > 180 {
		v -= 360
	}
	for v < -180 {
		v += 360
	}
	return v
}

func normalizeBearing(v float64) float64 {
	v = math.Mod(v, 360)
	if v < 0 {
		v += 360
	}
	return v
}
func InitialBearing(lat1, lon1, lat2, lon2 float64) float64 {
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dl := normalizeLon(lon2-lon1) * math.Pi / 180
	y := math.Sin(dl) * math.Cos(p2)
	x := math.Cos(p1)*math.Sin(p2) - math.Sin(p1)*math.Cos(p2)*math.Cos(dl)
	d := math.Atan2(y, x) * 180 / math.Pi
	if d < 0 {
		d += 360
	}
	return d
}
func InterpolateGC(lat1, lon1, lat2, lon2, f float64) (float64, float64) {
	if f <= 0 {
		return lat1, lon1
	}
	if f >= 1 {
		return lat2, lon2
	}
	p1 := lat1 * math.Pi / 180
	l1 := lon1 * math.Pi / 180
	p2 := lat2 * math.Pi / 180
	l2 := lon2 * math.Pi / 180
	d := HaversineNM(lat1, lon1, lat2, lon2) / earthRadiusNM
	if d < 1e-12 {
		return lat1, lon1
	}
	a := math.Sin((1-f)*d) / math.Sin(d)
	b := math.Sin(f*d) / math.Sin(d)
	x := a*math.Cos(p1)*math.Cos(l1) + b*math.Cos(p2)*math.Cos(l2)
	y := a*math.Cos(p1)*math.Sin(l1) + b*math.Cos(p2)*math.Sin(l2)
	z := a*math.Sin(p1) + b*math.Sin(p2)
	lat := math.Atan2(z, math.Sqrt(x*x+y*y)) * 180 / math.Pi
	lon := math.Atan2(y, x) * 180 / math.Pi
	return lat, normalizeLon(lon)
}
func LocalXYNM(lat, lon, refLat, refLon float64) (x, y float64) {
	c := math.Cos(refLat * math.Pi / 180)
	if math.Abs(c) < 0.03 {
		c = 0.03
	}
	return normalizeLon(lon-refLon) * 60 * c, (lat - refLat) * 60
}
func FromLocalXYNM(x, y, refLat, refLon float64) (lat, lon float64) {
	c := math.Cos(refLat * math.Pi / 180)
	if math.Abs(c) < 0.03 {
		c = 0.03
	}
	return refLat + y/60, normalizeLon(refLon + x/(60*c))
}
func AngleDiff(a, b float64) float64 {
	d := math.Abs(a - b)
	for d >= 360 {
		d -= 360
	}
	if d > 180 {
		d = 360 - d
	}
	return d
}
func CrossTrackPointToSegmentNM(lat, lon, aLat, aLon, bLat, bLon float64) (dist, signed, along float64) {
	ax, ay := LocalXYNM(aLat, aLon, lat, lon)
	bx, by := LocalXYNM(bLat, bLon, lat, lon)
	vx, vy := bx-ax, by-ay
	den := vx*vx + vy*vy
	t := 0.0
	if den > 1e-12 {
		t = -(ax*vx + ay*vy) / den
	}
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	px, py := ax+t*vx, ay+t*vy
	dist = math.Hypot(px, py)
	length := math.Hypot(vx, vy)
	if length > 1e-9 {
		signed = (vx*py - vy*px) / length
		along = t * length
	} else {
		signed = dist
	}
	return
}

var decimalCoordRE = regexp.MustCompile(`^\s*([+-]?[0-9]+(?:\.[0-9]+)?)\s*[,/\s]\s*([+-]?[0-9]+(?:\.[0-9]+)?)\s*$`)
var hemiCoordRE = regexp.MustCompile(`(?i)^\s*([0-9]+(?:\.[0-9]+)?)\s*([NS])\s*[,;/]?\s*([0-9]+(?:\.[0-9]+)?)\s*([EW])\s*$`)

func ParseCoordinates(s string) (float64, float64, bool) {
	if m := decimalCoordRE.FindStringSubmatch(s); m != nil {
		a, e1 := strconv.ParseFloat(m[1], 64)
		b, e2 := strconv.ParseFloat(m[2], 64)
		if e1 == nil && e2 == nil && a >= -90 && a <= 90 && b >= -180 && b <= 180 {
			return a, b, true
		}
	}
	if m := hemiCoordRE.FindStringSubmatch(s); m != nil {
		a, e1 := strconv.ParseFloat(m[1], 64)
		b, e2 := strconv.ParseFloat(m[3], 64)
		if e1 == nil && e2 == nil {
			if strings.EqualFold(m[2], "S") {
				a = -a
			}
			if strings.EqualFold(m[4], "W") {
				b = -b
			}
			if a >= -90 && a <= 90 && b >= -180 && b <= 180 {
				return a, b, true
			}
		}
	}
	return 0, 0, false
}

type LandMask struct{ Polys []landPoly }
type landPoly struct {
	Rings                          [][][]float64
	MinLat, MaxLat, MinLon, MaxLon float64
}

func LoadLandMask(data []byte) *LandMask {
	var root struct {
		Features []struct {
			Geometry struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if json.Unmarshal(data, &root) != nil {
		return &LandMask{}
	}
	m := &LandMask{}
	for _, f := range root.Features {
		if f.Geometry.Type == "Polygon" {
			var p [][][]float64
			if json.Unmarshal(f.Geometry.Coordinates, &p) == nil {
				m.addPoly(p)
			}
		}
		if f.Geometry.Type == "MultiPolygon" {
			var ps [][][][]float64
			if json.Unmarshal(f.Geometry.Coordinates, &ps) == nil {
				for _, p := range ps {
					m.addPoly(p)
				}
			}
		}
	}
	return m
}
func (m *LandMask) addPoly(rings [][][]float64) {
	if len(rings) == 0 {
		return
	}
	p := landPoly{Rings: rings, MinLat: 90, MaxLat: -90, MinLon: 180, MaxLon: -180}
	for _, r := range rings {
		for _, pt := range r {
			if len(pt) < 2 {
				continue
			}
			lon, lat := pt[0], pt[1]
			if lat < p.MinLat {
				p.MinLat = lat
			}
			if lat > p.MaxLat {
				p.MaxLat = lat
			}
			if lon < p.MinLon {
				p.MinLon = lon
			}
			if lon > p.MaxLon {
				p.MaxLon = lon
			}
		}
	}
	m.Polys = append(m.Polys, p)
}
func (m *LandMask) Contains(lat, lon float64) bool {
	if m == nil {
		return false
	}
	for _, p := range m.Polys {
		if lat < p.MinLat || lat > p.MaxLat || lon < p.MinLon || lon > p.MaxLon {
			continue
		}
		if pointInRing(lat, lon, p.Rings[0]) {
			insideHole := false
			for i := 1; i < len(p.Rings); i++ {
				if pointInRing(lat, lon, p.Rings[i]) {
					insideHole = true
					break
				}
			}
			if !insideHole {
				return true
			}
		}
	}
	return false
}
func pointInRing(lat, lon float64, ring [][]float64) bool {
	inside := false
	j := len(ring) - 1
	for i := 0; i < len(ring); i++ {
		if len(ring[i]) < 2 || len(ring[j]) < 2 {
			j = i
			continue
		}
		xi, yi := ring[i][0], ring[i][1]
		xj, yj := ring[j][0], ring[j][1]
		hit := ((yi > lat) != (yj > lat)) && (lon < (xj-xi)*(lat-yi)/(yj-yi+1e-20)+xi)
		if hit {
			inside = !inside
		}
		j = i
	}
	return inside
}
func (m *LandMask) SegmentTouchesLand(aLat, aLon, bLat, bLon, stepNM float64) bool {
	d := HaversineNM(aLat, aLon, bLat, bLon)
	n := int(math.Ceil(d / stepNM))
	if n < 1 {
		n = 1
	}
	for i := 0; i <= n; i++ {
		lat, lon := InterpolateGC(aLat, aLon, bLat, bLon, float64(i)/float64(n))
		if m.Contains(lat, lon) {
			return true
		}
	}
	return false
}
