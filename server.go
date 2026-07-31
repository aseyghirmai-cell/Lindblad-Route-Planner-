package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type App struct {
	plannerMu     sync.RWMutex
	planner       *PlannerData
	basePlanner   *PlannerData
	olex          *OlexLibrary
	rtz           *RTZLibrary
	land          *LandMask
	uploadDir     string
	uploadMu      sync.Mutex
	plansMu       sync.RWMutex
	plansSaveMu   sync.Mutex
	plans         map[string]*RoutePlan
	importMu      sync.RWMutex
	importJobs    map[string]*OlexImportJob
	importRunning bool
	server        *http.Server
	done          chan struct{}
	assets        map[string][]byte
	log           *log.Logger
	publicMode    bool
}

func NewApp(basePlanner, planner *PlannerData, olex *OlexLibrary, rtz *RTZLibrary, land *LandMask, uploadDir string, assets map[string][]byte, logger *log.Logger) *App {
	app := &App{basePlanner: basePlanner, planner: planner, olex: olex, rtz: rtz, land: land, uploadDir: uploadDir, plans: map[string]*RoutePlan{}, importJobs: map[string]*OlexImportJob{}, done: make(chan struct{}), assets: assets, log: logger}
	app.loadSavedPlans()
	return app
}
func (a *App) registerAPIRoutes(mux *http.ServeMux, public bool) {
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok", "engine": func() string {
			if public {
				return publicEngineName
			}
			return "AI Corridor 2.6 Persistent Online"
		}()})
	})
	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/destinations", a.handleDestinations)
	mux.HandleFunc("/api/resolve", a.handleResolve)
	mux.HandleFunc("/api/coverage", a.handleCoverage)
	mux.HandleFunc("/api/time/convert", a.handleTimeConvert)
	mux.HandleFunc("/api/route", a.handleRoute)
	mux.HandleFunc("/api/route/update", a.handleRouteUpdate)
	mux.HandleFunc("/api/route/get", a.handleRouteGet)
	mux.HandleFunc("/api/route/clone", a.handleRouteClone)
	mux.HandleFunc("/api/route/delete", a.handleRouteDelete)
	mux.HandleFunc("/api/routes", a.handleRouteList)
	mux.HandleFunc("/api/olex/areas", a.handleOlexAreas)
	if !public {
		mux.HandleFunc("/api/olex/import", a.handleOlexImport)
		mux.HandleFunc("/api/olex/import-path", a.handleOlexImportPath)
		mux.HandleFunc("/api/rtz/import-path", a.handleRTZImportPath)
		mux.HandleFunc("/api/shutdown", a.handleShutdown)
	}
	mux.HandleFunc("/api/olex/import/status", a.handleOlexImportStatus)
	mux.HandleFunc("/api/olex/import/current", a.handleOlexImportCurrent)
	mux.HandleFunc("/api/managed/olex/rename", a.handleOlexRename)
	mux.HandleFunc("/api/managed/olex/toggle", a.handleOlexToggle)
	mux.HandleFunc("/api/managed/olex/remove", a.handleOlexRemove)
	mux.HandleFunc("/api/rtz/areas", a.handleRTZAreas)
	mux.HandleFunc("/api/managed/rtz/rename", a.handleRTZRename)
	mux.HandleFunc("/api/managed/rtz/toggle", a.handleRTZToggle)
	mux.HandleFunc("/api/managed/rtz/remove", a.handleRTZRemove)
	mux.HandleFunc("/api/upload/start", a.handleUploadStart)
	mux.HandleFunc("/api/upload/chunk", a.handleUploadChunk)
	mux.HandleFunc("/api/upload/finish", a.handleUploadFinish)
	mux.HandleFunc("/api/upload/cancel", a.handleUploadCancel)
	mux.HandleFunc("/api/preview/context", a.handlePreview)
	mux.HandleFunc("/api/download/rtz", a.handleDownloadRTZ)
	mux.HandleFunc("/api/download/olex", a.handleDownloadOlex)
	mux.HandleFunc("/api/download/json", a.handleDownloadJSON)
}

func (a *App) publicAPIRoutes() http.Handler {
	mux := http.NewServeMux()
	a.registerAPIRoutes(mux, true)
	return recoveryMiddleware(a.log, mux)
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/index.html", a.handleIndex)
	mux.HandleFunc("/styles.css", a.asset("styles.css", "text/css; charset=utf-8"))
	mux.HandleFunc("/app.js", a.asset("app.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("/library.js", a.asset("library.js", "application/javascript; charset=utf-8"))
	a.registerAPIRoutes(mux, false)
	return recoveryMiddleware(a.log, mux)
}

func recoveryMiddleware(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if x := recover(); x != nil {
				logger.Printf("panic %s %s: %v", r.Method, r.URL.Path, x)
				writeError(w, 500, "Unexpected internal error. Details were written to the local log.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
func (a *App) asset(name, ct string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(a.assets[name])
	}
}
func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(a.assets["index.html"])
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	if err := d.Decode(v); err != nil {
		writeError(w, 400, "Invalid request: "+err.Error())
		return false
	}
	return true
}
func method(w http.ResponseWriter, r *http.Request, want string) bool {
	if r.Method != want {
		w.Header().Set("Allow", want)
		writeError(w, 405, "Method not allowed")
		return false
	}
	return true
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	areas := a.olex.Areas()
	rtzAreas := a.rtz.Areas()
	var bytes int64
	for _, x := range areas {
		bytes += x.SizeBytes
	}
	var rtzBytes int64
	activeRTZ := 0
	for _, x := range rtzAreas {
		rtzBytes += x.SizeBytes
		if !x.Disabled {
			activeRTZ++
		}
	}
	planner := a.plannerSnapshot()
	olexPath, rtzPath := a.olex.dir, a.rtz.dir
	engineName := "AI Corridor 2.6 Persistent Online"
	if a.publicMode {
		olexPath, rtzPath = "Secure organization storage", "Secure organization storage"
		engineName = publicEngineName
	}
	writeJSON(w, 200, map[string]any{"meta": planner.Meta, "counts": map[string]any{"destinations": len(planner.Destinations), "routeNodes": len(planner.Nodes), "graphEdges": len(planner.RawEdges), "olexStorageBytes": bytes, "rtzFiles": len(rtzAreas), "activeRTZFiles": activeRTZ, "rtzStorageBytes": rtzBytes}, "olexAreas": areas, "rtzAreas": rtzAreas, "olexStorageBytes": bytes, "rtzStorageBytes": rtzBytes, "olexLibraryPath": olexPath, "rtzLibraryPath": rtzPath, "persistent": true, "engine": map[string]any{"name": engineName, "corridorCentering": true, "directionalLaneProtection": true, "splitCorridorProtection": true, "persistentUploads": true, "runtimeRTZLibrary": true, "multiUser": a.publicMode}})
}

func (a *App) storageBytes() int64 {
	var total int64
	for _, x := range a.olex.Areas() {
		total += x.SizeBytes
		total += x.IndexedSizeBytes
	}
	for _, x := range a.rtz.Areas() {
		total += x.SizeBytes
	}
	return total
}
func (a *App) handleDestinations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	writeJSON(w, 200, a.plannerSnapshot().SearchDestinations(q, limit))
}
func (a *App) handleResolve(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "POST") {
		return
	}
	var req struct {
		Input string `json:"input"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	d, err := a.plannerSnapshot().Resolve(req.Input)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, Position{d.Name, d.Lat, d.Lon})
}
func (a *App) handleCoverage(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "POST") {
		return
	}
	var q struct{ StartLat, StartLon, EndLat, EndLon float64 }
	if !decodeJSON(w, r, &q) {
		return
	}
	areas := a.olex.AreasForCorridor(q.StartLat, q.StartLon, q.EndLat, q.EndLon)
	names := []string{}
	for _, x := range areas {
		names = append(names, x.Name)
	}
	writeJSON(w, 200, map[string]any{"covered": len(areas) > 0, "areaCount": len(areas), "areas": names})
}
func (a *App) handleTimeConvert(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "POST") {
		return
	}
	var q struct{ Date, Time, Zone string }
	if !decodeJSON(w, r, &q) {
		return
	}
	t, err := parseUTC(q.Date, q.Time, q.Zone)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"utc": t})
}
func (a *App) handleRoute(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "POST") {
		return
	}
	var req RouteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	planner := a.plannerSnapshot()
	sd, e := planner.Resolve(req.Start)
	if e != nil {
		writeError(w, 400, e.Error())
		return
	}
	ed, e := planner.Resolve(req.End)
	if e != nil {
		writeError(w, 400, e.Error())
		return
	}
	comp, e := a.olex.CompositeForCorridor(sd.Lat, sd.Lon, ed.Lat, ed.Lon)
	if e != nil {
		a.log.Printf("load OLEX: %v", e)
		writeError(w, 500, "Could not load the selected OLEX databases: "+e.Error())
		return
	}
	plan, e := planner.Generate(req, comp, a.land)
	if e != nil {
		a.log.Printf("route error: %v", e)
		writeError(w, 422, e.Error())
		return
	}
	a.plansMu.Lock()
	a.plans[plan.ID] = plan
	a.plansMu.Unlock()
	if err := a.savePlans(); err != nil {
		a.log.Printf("save generated route: %v", err)
		writeError(w, http.StatusInternalServerError, "Route was generated but could not be persisted")
		return
	}
	writeJSON(w, 200, plan)
}
func (a *App) handleOlexAreas(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.olex.Areas())
}
func (a *App) handleOlexRename(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "POST") {
		return
	}
	var q struct {
		Name    string `json:"name"`
		NewName string `json:"newName"`
	}
	if !decodeJSON(w, r, &q) {
		return
	}
	if err := a.olex.Rename(q.Name, q.NewName); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (a *App) handleOlexToggle(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "POST") {
		return
	}
	var q struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if !decodeJSON(w, r, &q) {
		return
	}
	if err := a.olex.Toggle(q.Name, q.Enabled); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (a *App) handleOlexRemove(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "DELETE") {
		return
	}
	if err := a.olex.Remove(r.URL.Query().Get("name")); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (a *App) getPlan(r *http.Request) (*RoutePlan, bool) {
	id := r.URL.Query().Get("id")
	a.plansMu.RLock()
	p := a.plans[id]
	a.plansMu.RUnlock()
	if p == nil {
		return nil, false
	}
	return p, true
}
func (a *App) handlePreview(w http.ResponseWriter, r *http.Request) {
	p, ok := a.getPlan(r)
	if !ok {
		writeError(w, 404, "Route not found")
		return
	}
	minLat, maxLat, minLon, maxLon := 90.0, -90.0, 180.0, -180.0
	for _, x := range p.Waypoints {
		if x.Lat < minLat {
			minLat = x.Lat
		}
		if x.Lat > maxLat {
			maxLat = x.Lat
		}
		if x.Lon < minLon {
			minLon = x.Lon
		}
		if x.Lon > maxLon {
			maxLon = x.Lon
		}
	}
	padLat, padLon := .2, .4
	comp, err := a.olex.CompositeForCorridor(minLat, minLon, maxLat, maxLon)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	olexCells := any([]any{})
	if comp != nil {
		olexCells = comp.Preview(minLat-padLat, maxLat+padLat, minLon-padLon, maxLon+padLon, 12000)
	}
	type previewHistoricalSegment struct {
		Lat1      float64 `json:"lat1"`
		Lon1      float64 `json:"lon1"`
		Lat2      float64 `json:"lat2"`
		Lon2      float64 `json:"lon2"`
		Consensus int     `json:"consensus"`
		WidthNM   float64 `json:"widthNM"`
	}
	planner := a.plannerSnapshot()
	historical := make([]previewHistoricalSegment, 0, 4000)
	for _, seg := range planner.Segments {
		if math.Max(seg.Lat1, seg.Lat2) < minLat-padLat || math.Min(seg.Lat1, seg.Lat2) > maxLat+padLat || math.Max(seg.Lon1, seg.Lon2) < minLon-padLon || math.Min(seg.Lon1, seg.Lon2) > maxLon+padLon {
			continue
		}
		historical = append(historical, previewHistoricalSegment{Lat1: seg.Lat1, Lon1: seg.Lon1, Lat2: seg.Lat2, Lon2: seg.Lon2, Consensus: seg.Consensus, WidthNM: seg.WidthNM})
		if len(historical) >= 12000 {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"olexCells": olexCells, "historicalSegments": historical})
}
func filenameSafe(s string) string {
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == ' ' {
			return r
		}
		return '_'
	}, s)
	return strings.TrimSpace(s)
}
func (a *App) handleDownloadRTZ(w http.ResponseWriter, r *http.Request) {
	p, ok := a.getPlan(r)
	if !ok {
		writeError(w, 404, "Route not found")
		return
	}
	b, err := ExportRTZ(p)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filenameSafe(p.RouteName)+".rtz"))
	_, _ = w.Write(b)
}
func (a *App) handleDownloadOlex(w http.ResponseWriter, r *http.Request) {
	p, ok := a.getPlan(r)
	if !ok {
		writeError(w, 404, "Route not found")
		return
	}
	b, err := ExportOlexPlot(p)
	if err != nil {
		writeError(w, 500, "OLEX plot export failed: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filenameSafe(p.RouteName)+"_OlexPlot.gz"))
	_, _ = w.Write(b)
}
func (a *App) handleDownloadJSON(w http.ResponseWriter, r *http.Request) {
	p, ok := a.getPlan(r)
	if !ok {
		writeError(w, http.StatusNotFound, "Route not found")
		return
	}
	b, err := ExportRouteJSON(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filenameSafe(p.RouteName)+".route.json"))
	_, _ = w.Write(b)
}
func (a *App) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "POST") {
		return
	}
	a.importMu.RLock()
	running := a.importRunning
	a.importMu.RUnlock()
	if running {
		writeError(w, 409, "An OLEX import is still running. Keep the planner open until it reaches 100%.")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
	go func() {
		time.Sleep(150 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = a.server.Shutdown(ctx)
	}()
}
func (a *App) Serve(listenAddr string) (string, error) {
	if strings.TrimSpace(listenAddr) == "" {
		listenAddr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return "", err
	}
	a.server = &http.Server{Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	browserAddr := ln.Addr().String()
	if host, port, splitErr := net.SplitHostPort(browserAddr); splitErr == nil && (host == "0.0.0.0" || host == "::") {
		browserAddr = net.JoinHostPort("127.0.0.1", port)
	}
	u := "http://" + browserAddr + "/"
	go func() {
		defer close(a.done)
		if err := a.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			a.log.Printf("server: %v", err)
		}
	}()
	return u, nil
}

func localDataDir() string {
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		return filepath.Join(v, "LindbladRoutePlannerOnlineData")
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "LindbladRoutePlannerOnlineData")
	}
	return filepath.Join(os.TempDir(), "LindbladRoutePlannerOnlineData")
}
