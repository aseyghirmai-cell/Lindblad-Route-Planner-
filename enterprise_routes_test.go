package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func enterpriseTestApp(t *testing.T) (*App, *RoutePlan) {
	t.Helper()
	planner, land := testData(t)
	root := t.TempDir()
	olex, err := NewOlexLibrary(filepath.Join(root, "olex"), readAsset("default_olex.olxidx.gz"))
	if err != nil {
		t.Fatal(err)
	}
	rtz, err := NewRTZLibrary(filepath.Join(root, "rtz"))
	if err != nil {
		t.Fatal(err)
	}
	comp, err := olex.CompositeForCorridor(-65.13, -64.04, -65.02, -63.87)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Generate(req("-65.1191, -64.0165", "-65.0316, -63.8869", "Enterprise Test", "14:00"), comp, land)
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(planner, planner, olex, rtz, land, filepath.Join(root, "uploads"), map[string][]byte{}, log.New(io.Discard, "", 0))
	app.plans[plan.ID] = plan
	return app, plan
}

func TestEnterpriseWaypointUpdatePersistsAllEditableFields(t *testing.T) {
	app, plan := enterpriseTestApp(t)
	waypoints := make([]WaypointEdit, len(plan.Waypoints))
	for i, wp := range plan.Waypoints {
		waypoints[i] = WaypointEdit{
			Name: wp.Name, Lat: wp.Lat, Lon: wp.Lon, RadiusNM: wp.RadiusNM,
			PortsideXTDNM: 0.15, StarboardXTDNM: 0.25, WheelOverNM: 0.08,
			SpeedKn: 8.5, GeometryType: "Orthodrome", Remarks: "Bridge review note",
		}
	}
	payload, _ := json.Marshal(RouteUpdateRequest{ID: plan.ID, RouteName: "Edited Enterprise Route", Waypoints: waypoints})
	req := httptest.NewRequest(http.MethodPost, "/api/route/update", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.handleRouteUpdate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rec.Code, rec.Body.String())
	}
	var updated RoutePlan
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.RouteName != "Edited Enterprise Route" || updated.Revision < 2 {
		t.Fatalf("route metadata not updated: %#v", updated)
	}
	first := updated.Waypoints[0]
	if first.PortsideXTDNM != 0.15 || first.StarboardXTDNM != 0.25 || first.WheelOverNM != 0.08 || first.SpeedKn != 8.5 || first.GeometryType != "Orthodrome" || first.Remarks != "Bridge review note" {
		t.Fatalf("editable waypoint fields were not preserved: %#v", first)
	}
	if first.Leg == nil || first.Leg.SpeedKn != 8.5 {
		t.Fatalf("leg speed was not applied: %#v", first.Leg)
	}

	restarted := NewApp(app.basePlanner, app.planner, app.olex, app.rtz, app.land, app.uploadDir, map[string][]byte{}, log.New(io.Discard, "", 0))
	persisted := restarted.plans[plan.ID]
	if persisted == nil || persisted.Waypoints[0].Remarks != "Bridge review note" || persisted.Waypoints[0].GeometryType != "Orthodrome" {
		t.Fatalf("route did not survive restart: %#v", persisted)
	}
}

func TestEnterpriseExportsPreserveRouteEditingData(t *testing.T) {
	_, plan := enterpriseTestApp(t)
	plan.Waypoints[1].Name = "CUSTOM-WP"
	plan.Waypoints[1].RadiusNM = 0.75
	plan.Waypoints[1].PortsideXTDNM = 0.12
	plan.Waypoints[1].StarboardXTDNM = 0.34
	plan.Waypoints[1].GeometryType = "Orthodrome"
	plan.Waypoints[1].Remarks = "Exact enterprise round-trip note"

	rtz, err := ExportRTZ(plan)
	if err != nil {
		t.Fatal(err)
	}
	text := string(rtz)
	for _, want := range []string{`name="CUSTOM-WP"`, `radius="0.750"`, `geometryType="Orthodrome"`, `starboardXTD="0.340"`, `portsideXTD="0.120"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("RTZ missing %s\n%s", want, text)
		}
	}
	full, err := ExportRouteJSON(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(full, []byte("Exact enterprise round-trip note")) || !bytes.Contains(full, []byte("wheelOverNM")) {
		t.Fatalf("enterprise JSON export omitted editable fields: %s", full)
	}
}
