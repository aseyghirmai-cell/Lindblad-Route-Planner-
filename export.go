package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

func ExportOlexPlot(p *RoutePlan) ([]byte, error) {
	if p == nil || len(p.Waypoints) < 2 {
		return nil, fmt.Errorf("route has fewer than two waypoints")
	}
	var raw bytes.Buffer
	raw.WriteString("Ferdig forenklet\n")
	raw.WriteString("Rute uten navn\n")
	raw.WriteString("Plottsett 512\n")
	stamp := time.Now().Unix()
	for i, w := range p.Waypoints {
		fmt.Fprintf(&raw, "%.7f %.7f %d Brunsirkel\n", w.Lat*60, w.Lon*60, stamp+int64(i*2))
	}
	var out bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if _, err := gz.Write(raw.Bytes()); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

type rtzRoute struct {
	XMLName   xml.Name     `xml:"route"`
	Xmlns     string       `xml:"xmlns,attr"`
	Version   string       `xml:"version,attr"`
	RouteInfo rtzInfo      `xml:"routeInfo"`
	Waypoints rtzWaypoints `xml:"waypoints"`
}
type rtzInfo struct {
	RouteName  string `xml:"routeName,attr"`
	VesselName string `xml:"vesselName,attr,omitempty"`
	VesselMMSI string `xml:"vesselMMSI,attr,omitempty"`
}
type rtzWaypoints struct {
	Default rtzDefault `xml:"defaultWaypoint"`
	Items   []rtzWP    `xml:"waypoint"`
}
type rtzDefault struct {
	Radius   string `xml:"radius,attr"`
	Revision string `xml:"revision,attr"`
}
type rtzWP struct {
	ID       int     `xml:"id,attr"`
	Revision int     `xml:"revision,attr"`
	Name     string  `xml:"name,attr"`
	Radius   string  `xml:"radius,attr"`
	Position rtzPos  `xml:"position"`
	Leg      *rtzLeg `xml:"leg,omitempty"`
}
type rtzPos struct {
	Lat string `xml:"lat,attr"`
	Lon string `xml:"lon,attr"`
}
type rtzLeg struct {
	GeometryType   string `xml:"geometryType,attr"`
	StarboardXTD   string `xml:"starboardXTD,attr"`
	PortsideXTD    string `xml:"portsideXTD,attr"`
	DraughtForward string `xml:"draughtForward,attr"`
	DraughtAft     string `xml:"draughtAft,attr"`
}

func ExportRTZ(p *RoutePlan) ([]byte, error) {
	if p == nil || len(p.Waypoints) < 2 {
		return nil, fmt.Errorf("route has fewer than two waypoints")
	}
	r := rtzRoute{Xmlns: "http://www.cirm.org/RTZ/1/1", Version: "1.1", RouteInfo: rtzInfo{RouteName: p.RouteName, VesselName: "National Geographic Resolution"}, Waypoints: rtzWaypoints{Default: rtzDefault{Radius: "0.5", Revision: "1"}}}
	for i, w := range p.Waypoints {
		name := strings.TrimSpace(w.Name)
		if name == "" {
			name = fmt.Sprintf("WP%03d", i+1)
		}
		x := rtzWP{ID: i + 1, Revision: 1, Name: name, Radius: fmt.Sprintf("%.3f", w.RadiusNM), Position: rtzPos{fmt.Sprintf("%.8f", w.Lat), fmt.Sprintf("%.8f", w.Lon)}}
		if i > 0 {
			geometry := normalizeGeometryType(w.GeometryType)
			x.Leg = &rtzLeg{geometry, fmt.Sprintf("%.3f", w.StarboardXTDNM), fmt.Sprintf("%.3f", w.PortsideXTDNM), "5.7", "5.7"}
		}
		r.Waypoints.Items = append(r.Waypoints.Items, x)
	}
	body, err := xml.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}

// ExportRouteJSON preserves every editable enterprise waypoint field for exact round-tripping.
func ExportRouteJSON(p *RoutePlan) ([]byte, error) {
	if p == nil || len(p.Waypoints) < 2 {
		return nil, fmt.Errorf("route has fewer than two waypoints")
	}
	return json.MarshalIndent(p, "", "  ")
}
