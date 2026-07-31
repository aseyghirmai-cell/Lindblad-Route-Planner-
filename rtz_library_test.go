package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const sampleRTZ = `<?xml version="1.0" encoding="UTF-8"?>
<route xmlns="http://www.cirm.org/RTZ/1/1">
  <routeInfo routeName="Persistent RTZ Test"/>
  <waypoints>
    <waypoint id="1" name="Test Start"><position lat="0.0000" lon="0.0000"/></waypoint>
    <waypoint id="2" name="Test Mid"><position lat="0.0000" lon="0.1000"/></waypoint>
    <waypoint id="3" name="Test End"><position lat="0.0000" lon="0.2000"/></waypoint>
  </waypoints>
</route>`

func TestParseRTZNamespaceAndRejectMissingPosition(t *testing.T) {
	xmlText := `<?xml version="1.0"?>
<route xmlns="http://www.cirm.org/RTZ/1/1">
 <routeInfo routeName="Namespace Route"/>
 <waypoints>
  <waypoint id="bad" name="Missing Position"/>
  <waypoint id="1" name="Origin"><position lat="0" lon="0"/></waypoint>
  <waypoint id="2" name="End"><position lat="1.25" lon="2.5"/></waypoint>
 </waypoints>
</route>`
	routes, err := ParseRTZ(strings.NewReader(xmlText))
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Name != "Namespace Route" {
		t.Fatalf("unexpected parsed routes: %#v", routes)
	}
	if len(routes[0].Waypoints) != 2 {
		t.Fatalf("missing-position waypoint was not rejected: %#v", routes[0].Waypoints)
	}
	if routes[0].Waypoints[0].Lat != 0 || routes[0].Waypoints[0].Lon != 0 {
		t.Fatalf("valid 0,0 waypoint was rejected or changed: %#v", routes[0].Waypoints[0])
	}
}

func TestRTZLibraryPersistsAcrossRestartAndBuildsPlanner(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "route.rtz")
	if err := os.WriteFile(source, []byte(sampleRTZ), 0644); err != nil {
		t.Fatal(err)
	}
	libraryDir := filepath.Join(root, "rtz_library")
	lib, err := NewRTZLibrary(libraryDir)
	if err != nil {
		t.Fatal(err)
	}
	area, err := lib.ImportFile(source, "Stored Route", filepath.Base(source))
	if err != nil {
		t.Fatal(err)
	}
	if area.RouteCount != 1 || area.WaypointCount != 3 {
		t.Fatalf("unexpected RTZ area: %#v", area)
	}

	// Simulate a full application restart by constructing the library again from disk.
	restarted, err := NewRTZLibrary(libraryDir)
	if err != nil {
		t.Fatal(err)
	}
	areas := restarted.Areas()
	if len(areas) != 1 || areas[0].ID != area.ID || areas[0].Name != "Stored Route" {
		t.Fatalf("RTZ library did not survive restart: %#v", areas)
	}
	base := tinyPlanner()
	built, err := restarted.BuildPlanner(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Nodes) != len(base.Nodes)+3 {
		t.Fatalf("uploaded RTZ waypoints not merged: base=%d built=%d", len(base.Nodes), len(built.Nodes))
	}
	if built.Meta["uploaded_rtz_files"] != "1" || built.Meta["uploaded_rtz_routes"] != "1" {
		t.Fatalf("unexpected RTZ planner metadata: %#v", built.Meta)
	}
	if _, err := built.Resolve("Test Start"); err != nil {
		t.Fatalf("imported RTZ endpoint is not resolvable: %v", err)
	}
}

func tinyPlanner() *PlannerData {
	p := &PlannerData{
		Meta: map[string]string{"route_count": "1", "cleaned_routes": "1"},
		Nodes: []Node{
			{Lat: 0, Lon: -0.05, Name: "Base Start", RouteID: 1, Sequence: 0, Endpoint: true},
			{Lat: 0, Lon: 0.25, Name: "Base End", RouteID: 1, Sequence: 1, Endpoint: true},
		},
		RawEdges:     []RawEdge{{From: 0, To: 1, DistanceNM: 18, Count: 1}, {From: 1, To: 0, DistanceNM: 18, Count: 1}},
		Destinations: []Destination{{Name: "Base Start", Lat: 0, Lon: -0.05}, {Name: "Base End", Lat: 0, Lon: 0.25}},
	}
	p.buildSegmentsAndGraph()
	return p
}

func doJSONRequest(t *testing.T, h http.Handler, method, url string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, url, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func decodeMap(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("invalid JSON response %q: %v", w.Body.String(), err)
	}
	return v
}

func TestResumableRTZUploadSurvivesApplicationRestart(t *testing.T) {
	root := t.TempDir()
	olex, err := NewOlexLibrary(filepath.Join(root, "olex"), nil)
	if err != nil {
		t.Fatal(err)
	}
	rtzDir := filepath.Join(root, "rtz_library")
	rtz, err := NewRTZLibrary(rtzDir)
	if err != nil {
		t.Fatal(err)
	}
	base := tinyPlanner()
	logger := log.New(io.Discard, "", 0)
	newApp := func(lib *RTZLibrary) *App {
		return NewApp(base, base, olex, lib, nil, filepath.Join(root, "uploads"), map[string][]byte{}, logger)
	}

	data := []byte(sampleRTZ)
	startBody := uploadStartRequest{Kind: "rtz", Name: "Resumed Route", OriginalName: "resume.rtz", SizeBytes: int64(len(data)), LastModified: 123456789}
	app1 := newApp(rtz)
	w := doJSONRequest(t, app1.routes(), http.MethodPost, "/api/upload/start", startBody)
	if w.Code != http.StatusOK {
		t.Fatalf("start failed: %d %s", w.Code, w.Body.String())
	}
	started := decodeMap(t, w)
	id, _ := started["id"].(string)
	if id == "" {
		t.Fatalf("upload ID missing: %#v", started)
	}

	half := len(data) / 2
	req := httptest.NewRequest(http.MethodPost, "/api/upload/chunk?id="+id+"&offset=0", bytes.NewReader(data[:half]))
	w = httptest.NewRecorder()
	app1.routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first chunk failed: %d %s", w.Code, w.Body.String())
	}

	// Recreate both the persistent RTZ library and App to simulate closing/reopening the EXE.
	rtzRestarted, err := NewRTZLibrary(rtzDir)
	if err != nil {
		t.Fatal(err)
	}
	app2 := newApp(rtzRestarted)
	w = doJSONRequest(t, app2.routes(), http.MethodPost, "/api/upload/start", startBody)
	if w.Code != http.StatusOK {
		t.Fatalf("resume start failed: %d %s", w.Code, w.Body.String())
	}
	resumed := decodeMap(t, w)
	if int64(resumed["offset"].(float64)) != int64(half) || resumed["resumed"] != true {
		t.Fatalf("partial upload was not recovered: %#v", resumed)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/upload/chunk?id="+id+"&offset="+strconv.Itoa(half), bytes.NewReader(data[half:]))
	w = httptest.NewRecorder()
	app2.routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("second chunk failed: %d %s", w.Code, w.Body.String())
	}
	w = doJSONRequest(t, app2.routes(), http.MethodPost, "/api/upload/finish", uploadFinishRequest{ID: id})
	if w.Code != http.StatusOK {
		t.Fatalf("finish failed: %d %s", w.Code, w.Body.String())
	}

	finalRestart, err := NewRTZLibrary(rtzDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(finalRestart.Areas()) != 1 || finalRestart.Areas()[0].Name != "Resumed Route" {
		t.Fatalf("completed RTZ upload did not persist: %#v", finalRestart.Areas())
	}
	part, meta, err := app2.uploadPaths("rtz", id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatalf("completed partial file was not cleaned up: %v", err)
	}
	if _, err := os.Stat(meta); !os.IsNotExist(err) {
		t.Fatalf("completed session metadata was not cleaned up: %v", err)
	}
}
