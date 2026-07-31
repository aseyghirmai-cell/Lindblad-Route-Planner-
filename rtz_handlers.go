package main

import (
	"net/http"
	"path/filepath"
	"strings"
)

func (a *App) plannerSnapshot() *PlannerData {
	a.plannerMu.RLock()
	defer a.plannerMu.RUnlock()
	return a.planner
}

func (a *App) rebuildPlanner() error {
	p, err := a.rtz.BuildPlanner(a.basePlanner)
	if err != nil {
		return err
	}
	a.plannerMu.Lock()
	a.planner = p
	a.plannerMu.Unlock()
	a.log.Printf("route engine rebuilt with %d stored RTZ files: %d nodes, %d edges", len(a.rtz.Areas()), len(p.Nodes), len(p.RawEdges))
	return nil
}

func (a *App) handleRTZAreas(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	writeJSON(w, 200, a.rtz.Areas())
}

func (a *App) handleRTZRename(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var q struct {
		ID      string `json:"id"`
		NewName string `json:"newName"`
	}
	if !decodeJSON(w, r, &q) {
		return
	}
	if err := a.rtz.Rename(strings.TrimSpace(q.ID), q.NewName); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) handleRTZToggle(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var q struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if !decodeJSON(w, r, &q) {
		return
	}
	if err := a.rtz.Toggle(strings.TrimSpace(q.ID), q.Enabled); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if err := a.rebuildPlanner(); err != nil {
		_ = a.rtz.Toggle(strings.TrimSpace(q.ID), !q.Enabled)
		writeError(w, 500, "Could not rebuild the route engine: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) handleRTZRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if err := a.rtz.Remove(id); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if err := a.rebuildPlanner(); err != nil {
		writeError(w, 500, "RTZ entry was removed, but the route engine could not rebuild: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) handleRTZImportPath(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var q struct {
		Path string `json:"path"`
	}
	if !decodeJSON(w, r, &q) {
		return
	}
	path := strings.TrimSpace(strings.Trim(q.Path, `"`))
	if path == "" {
		writeError(w, 400, "Enter a full file or folder path containing RTZ files")
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		writeError(w, 400, "Invalid RTZ path: "+err.Error())
		return
	}
	imported, failures := a.rtz.ImportPath(abs)
	if len(imported) > 0 {
		if err := a.rebuildPlanner(); err != nil {
			writeError(w, 500, "RTZ files were stored, but the route engine could not rebuild: "+err.Error())
			return
		}
	}
	status := 200
	if len(imported) == 0 {
		status = 400
	}
	writeJSON(w, status, map[string]any{"imported": imported, "failures": failures, "persistent": true})
}
