package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type pendingOlexImport struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Path        string `json:"path"`
	RemoveAfter bool   `json:"removeAfter"`
	StartedUTC  string `json:"startedUTC"`
}

type OlexImportJob struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Source      string        `json:"source"`
	Status      string        `json:"status"`
	Phase       string        `json:"phase"`
	Progress    float64       `json:"progress"`
	BytesRead   int64         `json:"bytesRead"`
	TotalBytes  int64         `json:"totalBytes"`
	ValidRows   int64         `json:"validRows"`
	TilesDone   int           `json:"tilesDone"`
	TilesTotal  int           `json:"tilesTotal"`
	Detail      string        `json:"detail,omitempty"`
	Message     string        `json:"message,omitempty"`
	Error       string        `json:"error,omitempty"`
	Area        *OlexAreaInfo `json:"area,omitempty"`
	StartedUTC  string        `json:"startedUTC"`
	FinishedUTC string        `json:"finishedUTC,omitempty"`
	removePath  string
}

func (a *App) pendingOlexImportPath() string {
	return filepath.Join(a.olex.dir, "pending_olex_import.json")
}

func (a *App) writePendingOlexImport(p pendingOlexImport) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.pendingOlexImportPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, a.pendingOlexImportPath())
}

func (a *App) recoverPendingOlexImport() {
	b, err := os.ReadFile(a.pendingOlexImportPath())
	if err != nil {
		return
	}
	var pending pendingOlexImport
	if err := json.Unmarshal(b, &pending); err != nil || strings.TrimSpace(pending.Path) == "" {
		a.log.Printf("discarding invalid pending OLEX import: %v", err)
		_ = os.Remove(a.pendingOlexImportPath())
		return
	}
	if _, err := os.Stat(pending.Path); err != nil {
		a.log.Printf("pending OLEX source is unavailable: %s: %v", pending.Path, err)
		_ = os.Remove(a.pendingOlexImportPath())
		return
	}
	job, err := a.startOlexImportJob(pending.Name, pending.Source, pending.Path, pending.RemoveAfter)
	if err != nil {
		a.log.Printf("could not restart pending OLEX import: %v", err)
		return
	}
	a.log.Printf("restarted persistent OLEX import after application restart: %s (%s)", job.Name, pending.Path)
}

func newImportID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err == nil {
		return hex.EncodeToString(b)
	}
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

func (a *App) startOlexImportJob(name, source, path string, removeAfter bool) (*OlexImportJob, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() <= 0 {
		return nil, fmt.Errorf("selected OLEX file is empty or is not a regular file")
	}
	a.importMu.Lock()
	if a.importRunning {
		a.importMu.Unlock()
		return nil, fmt.Errorf("another OLEX database is already being imported; wait for it to finish")
	}
	pending := pendingOlexImport{Name: name, Source: source, Path: path, RemoveAfter: removeAfter, StartedUTC: time.Now().UTC().Format(time.RFC3339)}
	if err := a.writePendingOlexImport(pending); err != nil {
		a.importMu.Unlock()
		return nil, fmt.Errorf("could not persist the OLEX import job: %w", err)
	}
	a.importRunning = true
	job := &OlexImportJob{ID: newImportID(), Name: name, Source: source, Status: "queued", Phase: "preparing import", TotalBytes: st.Size(), StartedUTC: time.Now().UTC().Format(time.RFC3339)}
	if removeAfter {
		job.removePath = path
	}
	a.importJobs[job.ID] = job
	// Keep the job list bounded without deleting active work.
	if len(a.importJobs) > 20 {
		for id, old := range a.importJobs {
			if id != job.ID && (old.Status == "complete" || old.Status == "failed") {
				delete(a.importJobs, id)
				if len(a.importJobs) <= 20 {
					break
				}
			}
		}
	}
	a.importMu.Unlock()

	go func() {
		defer func() {
			if job.removePath != "" {
				_ = os.Remove(job.removePath)
			}
			_ = os.Remove(a.pendingOlexImportPath())
		}()
		a.importMu.Lock()
		job.Status = "running"
		a.importMu.Unlock()
		area, err := a.olex.ImportGZFile(name, source, path, func(p OlexImportProgress) {
			a.importMu.Lock()
			job.Phase = p.Phase
			job.Progress = p.Progress
			job.BytesRead = p.BytesRead
			job.TotalBytes = p.TotalBytes
			job.ValidRows = p.ValidRows
			job.TilesDone = p.TilesDone
			job.TilesTotal = p.TilesTotal
			job.Detail = p.Detail
			a.importMu.Unlock()
		})
		a.importMu.Lock()
		defer a.importMu.Unlock()
		a.importRunning = false
		job.FinishedUTC = time.Now().UTC().Format(time.RFC3339)
		if err != nil {
			job.Status = "failed"
			job.Phase = "failed"
			job.Error = err.Error()
			job.Message = "OLEX import failed"
			a.log.Printf("OLEX import %s failed: %v", source, err)
			return
		}
		job.Status = "complete"
		job.Phase = "complete"
		job.Progress = 1
		job.Area = &area
		job.Message = fmt.Sprintf("OLEX database ready: %s (%d indexed cells)", area.Name, area.Records)
		a.log.Printf("OLEX import %s complete: %s, %d cells, source %.2f GB, indexed %.2f GB", source, area.Name, area.Records, float64(area.SizeBytes)/(1<<30), float64(area.IndexedSizeBytes)/(1<<30))
	}()
	return job, nil
}

func (a *App) handleOlexImport(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "POST") {
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, 400, "Could not read the selected OLEX file: "+err.Error())
		return
	}
	var stagingPath, source string
	for {
		part, nextErr := mr.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			if stagingPath != "" {
				_ = os.Remove(stagingPath)
			}
			writeError(w, 400, "OLEX upload interrupted: "+nextErr.Error())
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			_ = part.Close()
			continue
		}
		source = filepath.Base(part.FileName())
		tmp, createErr := os.CreateTemp(a.olex.dir, "incoming-olex-*.gz")
		if createErr != nil {
			_ = part.Close()
			writeError(w, 500, "Could not create OLEX staging file: "+createErr.Error())
			return
		}
		stagingPath = tmp.Name()
		_, copyErr := io.CopyBuffer(tmp, part, make([]byte, 4<<20))
		closeErr := tmp.Close()
		_ = part.Close()
		if copyErr != nil || closeErr != nil {
			_ = os.Remove(stagingPath)
			if copyErr == nil {
				copyErr = closeErr
			}
			writeError(w, 500, "Could not store the selected OLEX file: "+copyErr.Error())
			return
		}
		break
	}
	if stagingPath == "" {
		writeError(w, 400, "Select a supported OLEX .gz or .olxidx.gz file")
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = strings.TrimSuffix(source, filepath.Ext(source))
	}
	job, err := a.startOlexImportJob(name, source, stagingPath, true)
	if err != nil {
		_ = os.Remove(stagingPath)
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": job.ID, "name": job.Name, "status": "queued", "totalBytes": job.TotalBytes})
}

func (a *App) handleOlexImportPath(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "POST") {
		return
	}
	var q struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &q) {
		return
	}
	path := strings.TrimSpace(strings.Trim(q.Path, `"`))
	if path == "" {
		writeError(w, 400, "Enter the full Windows path to the compressed OLEX file")
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		writeError(w, 400, "Invalid OLEX file path: "+err.Error())
		return
	}
	lower := strings.ToLower(abs)
	if !strings.HasSuffix(lower, ".gz") {
		writeError(w, 400, "The selected file must be a supported .gz or .olxidx.gz OLEX export")
		return
	}
	name := strings.TrimSpace(q.Name)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	}
	job, err := a.startOlexImportJob(name, filepath.Base(abs), abs, false)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": job.ID, "name": job.Name, "status": "queued", "totalBytes": job.TotalBytes})
}

func (a *App) handleOlexImportStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	a.importMu.RLock()
	job := a.importJobs[id]
	if job == nil {
		a.importMu.RUnlock()
		writeError(w, 404, "OLEX import job not found")
		return
	}
	// Copy through JSON to avoid exposing mutable job fields while the worker updates them.
	b, _ := json.Marshal(job)
	a.importMu.RUnlock()
	var snapshot OlexImportJob
	_ = json.Unmarshal(b, &snapshot)
	writeJSON(w, 200, snapshot)
}

func (a *App) handleOlexImportCurrent(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	a.importMu.RLock()
	var active *OlexImportJob
	for _, job := range a.importJobs {
		if job.Status == "queued" || job.Status == "running" {
			active = job
			break
		}
	}
	if active == nil {
		a.importMu.RUnlock()
		writeError(w, 404, "No OLEX import is currently running")
		return
	}
	b, _ := json.Marshal(active)
	a.importMu.RUnlock()
	var snapshot OlexImportJob
	_ = json.Unmarshal(b, &snapshot)
	writeJSON(w, 200, snapshot)
}
