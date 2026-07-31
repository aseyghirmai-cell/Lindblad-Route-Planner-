package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var testOnce sync.Once
var testPlanner *PlannerData
var testLand *LandMask
var testErr error

func testData(t *testing.T) (*PlannerData, *LandMask) {
	t.Helper()
	testOnce.Do(func() {
		testPlanner, testErr = LoadPlannerData(readAsset("planner.bin.gz"))
		testLand = LoadLandMask(readAsset("land.geojson"))
	})
	if testErr != nil {
		t.Fatal(testErr)
	}
	return testPlanner, testLand
}
func testLibrary(t *testing.T) *OlexLibrary {
	t.Helper()
	l, e := NewOlexLibrary(t.TempDir(), readAsset("default_olex.olxidx.gz"))
	if e != nil {
		t.Fatal(e)
	}
	return l
}
func req(a, b, name, hours string) RouteRequest {
	return RouteRequest{Start: a, End: b, RouteName: name, DepartureDate: "2026-07-30", DepartureTime: "12:00", DepartureZone: "UTC", ArrivalDate: "2026-07-30", ArrivalTime: hours, ArrivalZone: "UTC", MinimumWaypoints: true, AddComments: true}
}
func TestMain(m *testing.M) { os.Exit(m.Run()) }
func TestPlannerCounts(t *testing.T) {
	p, _ := testData(t)
	if len(p.Nodes) != 29082 || len(p.RawEdges) != 336266 || len(p.Destinations) != 5528 {
		t.Fatalf("unexpected counts: %d/%d/%d", len(p.Nodes), len(p.RawEdges), len(p.Destinations))
	}
	t.Logf("planner data: %d destinations, %d nodes, %d graph edges, %d historical segments", len(p.Destinations), len(p.Nodes), len(p.RawEdges), len(p.Segments))
}
func TestDirectionalCorridorScoring(t *testing.T) {
	p, _ := testData(t)
	different := 0
	for _, s := range p.Segments {
		var forward, reverse *Edge
		for i := range p.Adj[s.A] {
			if p.Adj[s.A][i].To == s.B && p.Adj[s.A][i].Historical {
				forward = &p.Adj[s.A][i]
				break
			}
		}
		for i := range p.Adj[s.B] {
			if p.Adj[s.B][i].To == s.A && p.Adj[s.B][i].Historical {
				reverse = &p.Adj[s.B][i]
				break
			}
		}
		if forward != nil && reverse != nil && (math.Abs(forward.CenterPenalty-reverse.CenterPenalty) > 1e-6 || forward.Consensus != reverse.Consensus) {
			different++
		}
	}
	if different == 0 {
		t.Fatal("directional corridor scores were accidentally symmetric")
	}
	t.Logf("%d historical segments have direction-specific centreline evidence", different)
}
func TestLemaireCorridorCentering(t *testing.T) {
	p, land := testData(t)
	l := testLibrary(t)
	c, e := l.CompositeForCorridor(-65.13, -64.04, -65.02, -63.87)
	if e != nil {
		t.Fatal(e)
	}
	plan, e := p.Generate(req("-65.1191, -64.0165", "-65.0316, -63.8869", "Lemaire", "14:00"), c, land)
	if e != nil {
		t.Fatal(e)
	}
	if plan.CorridorCenteredPct < 80 {
		t.Fatalf("centering too low %.1f%%", plan.CorridorCenteredPct)
	}
	if plan.MedianCorridorTracks < 3 {
		t.Fatalf("insufficient consensus %.0f", plan.MedianCorridorTracks)
	}
	if plan.WaypointCount < 3 || plan.WaypointCount > 25 {
		t.Fatalf("impractical waypoint count %d", plan.WaypointCount)
	}
	t.Logf("Lemaire: %.3f NM, %d WPs, %.1f%% corridor-centred, median %.0f tracks, OLEX %.1f%% supported / %.1f%% unsupported", plan.DistanceNM, plan.WaypointCount, plan.CorridorCenteredPct, plan.MedianCorridorTracks, plan.SupportedPct, plan.UnsupportedPct)
}
func TestNeumayerCorridor(t *testing.T) {
	p, land := testData(t)
	l := testLibrary(t)
	c, e := l.CompositeForCorridor(-64.87, -63.68, -64.68, -63.16)
	if e != nil {
		t.Fatal(e)
	}
	plan, e := p.Generate(req("-64.8552, -63.6569", "-64.6985, -63.1930", "Neumayer", "16:00"), c, land)
	if e != nil {
		t.Fatal(e)
	}
	if plan.DistanceNM < 5 || plan.DistanceNM > 30 {
		t.Fatalf("unexpected distance %.1f", plan.DistanceNM)
	}
	if plan.CorridorCenteredPct <= 0 {
		t.Fatalf("corridor centering inactive")
	}
	t.Logf("Neumayer: %.3f NM, %d WPs, %.1f%% corridor-centred, median %.0f tracks, OLEX %.1f%% supported / %.1f%% unsupported", plan.DistanceNM, plan.WaypointCount, plan.CorridorCenteredPct, plan.MedianCorridorTracks, plan.SupportedPct, plan.UnsupportedPct)
}
func TestBeagleHistoricalRouteWithoutOlex(t *testing.T) {
	p, land := testData(t)
	l := testLibrary(t)
	c, e := l.CompositeForCorridor(-54.86, -68.10, -54.91, -67.39)
	if e != nil {
		t.Fatal(e)
	}
	plan, e := p.Generate(req("-54.8533, -68.0794", "-54.9172, -67.4090", "Beagle", "18:00"), c, land)
	if e != nil {
		t.Fatal(e)
	}
	if plan.DistanceNM < 15 || plan.DistanceNM > 40 {
		t.Fatalf("unexpected distance %.1f", plan.DistanceNM)
	}
	if plan.ReviewPct < 99 {
		t.Fatalf("expected no OLEX review coverage, got %.1f", plan.ReviewPct)
	}
	if plan.CorridorCenteredPct <= 0 {
		t.Fatalf("historical corridor centering inactive")
	}
	t.Logf("Beagle: %.3f NM, %d WPs, %.1f%% corridor-centred, median %.0f tracks, OLEX %.1f%% review", plan.DistanceNM, plan.WaypointCount, plan.CorridorCenteredPct, plan.MedianCorridorTracks, plan.ReviewPct)
}

func TestPreviewSupportSegmentsMatchSummaryFractions(t *testing.T) {
	l := testLibrary(t)
	c, err := l.CompositeForCorridor(-65.13, -64.04, -65.02, -63.87)
	if err != nil {
		t.Fatal(err)
	}
	a := c.AssessSegment(-65.1191, -64.0165, -65.0316, -63.8869, 5.7)
	if len(a.Segments) == 0 {
		t.Fatal("assessment produced no preview support segments")
	}
	var supported, review, unsupported, cursor float64
	for i, seg := range a.Segments {
		if math.Abs(seg.StartFraction-cursor) > 1e-9 {
			t.Fatalf("segment %d starts at %.12f, expected %.12f", i, seg.StartFraction, cursor)
		}
		if seg.EndFraction <= seg.StartFraction {
			t.Fatalf("segment %d has invalid range %.12f..%.12f", i, seg.StartFraction, seg.EndFraction)
		}
		length := seg.EndFraction - seg.StartFraction
		switch seg.Status {
		case "SUPPORTED":
			supported += length
		case "OFFICER CHECK":
			review += length
		case "UNSUPPORTED":
			unsupported += length
		default:
			t.Fatalf("unexpected segment status %q", seg.Status)
		}
		cursor = seg.EndFraction
	}
	if math.Abs(cursor-1) > 1e-9 {
		t.Fatalf("segments end at %.12f, expected 1", cursor)
	}
	if math.Abs(supported-a.SupportedFraction) > 1e-9 || math.Abs(review-a.ReviewFraction) > 1e-9 || math.Abs(unsupported-a.UnsupportedFraction) > 1e-9 {
		t.Fatalf("preview fractions %.12f/%.12f/%.12f do not match summary fractions %.12f/%.12f/%.12f", supported, review, unsupported, a.SupportedFraction, a.ReviewFraction, a.UnsupportedFraction)
	}
}

func TestRawOlexImportAndManagement(t *testing.T) {
	l := testLibrary(t)
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	fmt.Fprintln(gz, "# lat lon depth")
	fmt.Fprintln(gz, "-65.0000 -64.0000 120")
	fmt.Fprintln(gz, "-65.0001 -64.0001 110")
	fmt.Fprintln(gz, "-65.0010 -64.0020 90")
	_ = gz.Close()
	a, e := l.ImportGZ("Test Soundings", "test.gz", raw.Bytes())
	if e != nil {
		t.Fatal(e)
	}
	if a.Records < 2 {
		t.Fatalf("too few cells %d", a.Records)
	}
	if e = l.Rename("Test Soundings", "Renamed Soundings"); e != nil {
		t.Fatal(e)
	}
	if e = l.Toggle("Renamed Soundings", false); e != nil {
		t.Fatal(e)
	}
	found := false
	for _, x := range l.Areas() {
		if x.Name == "Renamed Soundings" {
			found = true
			if !x.Disabled {
				t.Fatal("toggle did not disable")
			}
		}
	}
	if !found {
		t.Fatal("renamed area missing")
	}
	if e = l.Remove("Renamed Soundings"); e != nil {
		t.Fatal(e)
	}
}
func TestExports(t *testing.T) {
	p, land := testData(t)
	l := testLibrary(t)
	c, _ := l.CompositeForCorridor(-65.13, -64.04, -65.02, -63.87)
	plan, e := p.Generate(req("-65.1191, -64.0165", "-65.0316, -63.8869", "Export", "14:00"), c, land)
	if e != nil {
		t.Fatal(e)
	}
	rtz, e := ExportRTZ(plan)
	if e != nil || !strings.Contains(string(rtz), "http://www.cirm.org/RTZ/1/1") {
		t.Fatalf("bad RTZ: %v", e)
	}
	ogz, e := ExportOlexPlot(plan)
	if e != nil {
		t.Fatal(e)
	}
	zr, e := gzip.NewReader(bytes.NewReader(ogz))
	if e != nil {
		t.Fatal(e)
	}
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(zr)
	if !strings.HasPrefix(buf.String(), "Ferdig forenklet\nRute uten navn\nPlottsett 512\n") {
		t.Fatal("bad OLEX plot header")
	}
}

func TestTiledOlexLoadsOnlyRouteNearTiles(t *testing.T) {
	l := testLibrary(t)
	path := filepath.Join(t.TempDir(), "global.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	for i := 0; i < 100; i++ {
		fmt.Fprintf(gz, "%.6f %.6f %.1f\n", -65.0+float64(i)*0.0001, -64.0+float64(i)*0.0001, 100+float64(i%5))
		fmt.Fprintf(gz, "%.6f %.6f %.1f\n", 10.0+float64(i)*0.0001, 20.0+float64(i)*0.0001, 200+float64(i%5))
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	area, err := l.ImportGZFile("Global Test", "global.gz", path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if area.Format != tiledOlexFormat || area.IndexedSizeBytes <= 0 {
		t.Fatalf("expected tiled import, got format=%q indexed=%d", area.Format, area.IndexedSizeBytes)
	}
	comp, err := l.CompositeForCorridor(-65.1, -64.1, -64.9, -63.9)
	if err != nil {
		t.Fatal(err)
	}
	foundNear, foundFar := false, false
	for _, g := range comp.Grids {
		if g.Name != "Global Test" {
			continue
		}
		for _, c := range g.List {
			lat := float64(c.LatKey) * g.LatRes
			if lat < -60 {
				foundNear = true
			}
			if lat > 0 {
				foundFar = true
			}
		}
	}
	if !foundNear || foundFar {
		t.Fatalf("corridor tile selection failed: near=%v far=%v", foundNear, foundFar)
	}
}

func TestRejectUnsupportedGzipEarly(t *testing.T) {
	l := testLibrary(t)
	path := filepath.Join(t.TempDir(), "native-backup.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	for i := 0; i < 1000; i++ {
		fmt.Fprintln(gz, "this is not latitude longitude depth data")
	}
	_ = gz.Close()
	_ = f.Close()
	_, err = l.ImportGZFile("Unsupported", "native-backup.gz", path, nil)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "unsupported olex file format") {
		t.Fatalf("expected clear unsupported-format error, got %v", err)
	}
}
