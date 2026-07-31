package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxUploadChunk = int64(32 << 20)

type persistentUploadSession struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	OriginalName string `json:"originalName"`
	SizeBytes    int64  `json:"sizeBytes"`
	LastModified int64  `json:"lastModified,omitempty"`
	CreatedUTC   string `json:"createdUTC"`
	UpdatedUTC   string `json:"updatedUTC"`
}

type uploadStartRequest struct {
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	OriginalName string `json:"originalName"`
	SizeBytes    int64  `json:"sizeBytes"`
	LastModified int64  `json:"lastModified"`
}

type uploadFinishRequest struct {
	ID string `json:"id"`
}

func uploadFingerprint(kind, name string, size, modified int64) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%d", strings.ToLower(kind), strings.ToLower(filepath.Base(name)), size, modified)))
	return hex.EncodeToString(h[:12])
}

func validUploadKind(kind, name string) error {
	kind = strings.ToLower(strings.TrimSpace(kind))
	lower := strings.ToLower(filepath.Base(name))
	switch kind {
	case "olex":
		if !strings.HasSuffix(lower, ".gz") {
			return fmt.Errorf("OLEX uploads must be supported .gz or .olxidx.gz files")
		}
	case "rtz":
		if !strings.HasSuffix(lower, ".rtz") && !strings.HasSuffix(lower, ".xml") {
			return fmt.Errorf("RTZ uploads must be .rtz or RTZ XML files")
		}
	default:
		return fmt.Errorf("unknown upload type")
	}
	return nil
}

func (a *App) uploadSessionDir(kind string) string {
	if strings.EqualFold(kind, "olex") {
		return filepath.Join(a.olex.dir, ".resumable_uploads")
	}
	return filepath.Join(a.uploadDir, "rtz")
}

func (a *App) uploadPaths(kind, id string) (part, meta string, err error) {
	if id == "" || strings.ContainsAny(id, `/\\.`) {
		return "", "", fmt.Errorf("invalid upload session")
	}
	dir := a.uploadSessionDir(kind)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", err
	}
	return filepath.Join(dir, id+".part"), filepath.Join(dir, id+".json"), nil
}

func writeUploadSession(path string, s persistentUploadSession) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readUploadSession(path string) (persistentUploadSession, error) {
	var s persistentUploadSession
	b, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	return s, nil
}

func (a *App) locateUploadSession(id string) (persistentUploadSession, string, string, error) {
	for _, kind := range []string{"olex", "rtz"} {
		part, meta, err := a.uploadPaths(kind, id)
		if err != nil {
			return persistentUploadSession{}, "", "", err
		}
		s, err := readUploadSession(meta)
		if err == nil && s.ID == id && strings.EqualFold(s.Kind, kind) {
			return s, part, meta, nil
		}
	}
	return persistentUploadSession{}, "", "", fmt.Errorf("upload session not found")
}

func (a *App) handleUploadStart(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var req uploadStartRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Kind = strings.ToLower(strings.TrimSpace(req.Kind))
	req.OriginalName = filepath.Base(strings.TrimSpace(req.OriginalName))
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		req.Name = strings.TrimSuffix(req.OriginalName, filepath.Ext(req.OriginalName))
	}
	if req.SizeBytes <= 0 {
		writeError(w, 400, "The selected upload is empty")
		return
	}
	if err := validUploadKind(req.Kind, req.OriginalName); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	id := uploadFingerprint(req.Kind, req.OriginalName, req.SizeBytes, req.LastModified)
	part, meta, err := a.uploadPaths(req.Kind, id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	a.uploadMu.Lock()
	defer a.uploadMu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	session := persistentUploadSession{ID: id, Kind: req.Kind, Name: req.Name, OriginalName: req.OriginalName, SizeBytes: req.SizeBytes, LastModified: req.LastModified, CreatedUTC: now, UpdatedUTC: now}
	if old, err := readUploadSession(meta); err == nil && old.ID == id && old.SizeBytes == req.SizeBytes {
		session = old
		session.Name = req.Name
		session.UpdatedUTC = now
	}
	st, statErr := os.Stat(part)
	offset := int64(0)
	if statErr == nil {
		offset = st.Size()
		if offset > req.SizeBytes {
			_ = os.Remove(part)
			offset = 0
		}
	}
	if err := writeUploadSession(meta, session); err != nil {
		writeError(w, 500, "Could not create persistent upload session: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "offset": offset, "sizeBytes": req.SizeBytes, "resumed": offset > 0, "persistent": true})
}

func (a *App) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	offset, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("offset")), 10, 64)
	if err != nil || offset < 0 {
		writeError(w, 400, "Invalid upload offset")
		return
	}
	a.uploadMu.Lock()
	defer a.uploadMu.Unlock()
	session, part, meta, err := a.locateUploadSession(id)
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	st, statErr := os.Stat(part)
	current := int64(0)
	if statErr == nil {
		current = st.Size()
	}
	if current != offset {
		writeJSON(w, 409, map[string]any{"error": "Upload offset does not match the stored partial file", "offset": current})
		return
	}
	if current >= session.SizeBytes {
		writeJSON(w, 200, map[string]any{"id": id, "offset": current, "complete": true})
		return
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		writeError(w, 500, "Could not open persistent upload file: "+err.Error())
		return
	}
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		_ = f.Close()
		writeError(w, 500, err.Error())
		return
	}
	limited := io.LimitReader(r.Body, maxUploadChunk+1)
	n, copyErr := io.CopyBuffer(f, limited, make([]byte, 1<<20))
	syncErr := f.Sync()
	closeErr := f.Close()
	if n > maxUploadChunk {
		_ = os.Truncate(part, current)
		writeError(w, http.StatusRequestEntityTooLarge, "Upload chunk is too large")
		return
	}
	if copyErr != nil || syncErr != nil || closeErr != nil || n == 0 {
		_ = os.Truncate(part, current)
		if copyErr == nil {
			if syncErr != nil {
				copyErr = syncErr
			} else if closeErr != nil {
				copyErr = closeErr
			} else {
				copyErr = fmt.Errorf("empty upload chunk")
			}
		}
		writeError(w, 500, "Could not store upload chunk: "+copyErr.Error())
		return
	}
	newOffset := current + n
	if newOffset > session.SizeBytes {
		_ = os.Truncate(part, current)
		writeError(w, 400, "Upload exceeds the selected file size")
		return
	}
	session.UpdatedUTC = time.Now().UTC().Format(time.RFC3339)
	_ = writeUploadSession(meta, session)
	writeJSON(w, 200, map[string]any{"id": id, "offset": newOffset, "sizeBytes": session.SizeBytes, "complete": newOffset == session.SizeBytes})
}

func (a *App) cleanupUpload(part, meta string) {
	_ = os.Remove(part)
	_ = os.Remove(meta)
}

func (a *App) handleUploadFinish(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var req uploadFinishRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a.uploadMu.Lock()
	session, part, meta, err := a.locateUploadSession(strings.TrimSpace(req.ID))
	if err != nil {
		a.uploadMu.Unlock()
		writeError(w, 404, err.Error())
		return
	}
	st, err := os.Stat(part)
	if err != nil || st.Size() != session.SizeBytes {
		a.uploadMu.Unlock()
		current := int64(0)
		if err == nil {
			current = st.Size()
		}
		writeJSON(w, 409, map[string]any{"error": "Upload is incomplete", "offset": current, "sizeBytes": session.SizeBytes})
		return
	}
	a.uploadMu.Unlock()

	switch session.Kind {
	case "rtz":
		area, err := a.rtz.ImportFile(part, session.Name, session.OriginalName)
		if err != nil {
			writeError(w, 400, "RTZ import failed: "+err.Error())
			return
		}
		if err := a.rebuildPlanner(); err != nil {
			_ = a.rtz.Remove(area.ID)
			writeError(w, 500, "RTZ was stored but the route engine could not rebuild: "+err.Error())
			return
		}
		a.uploadMu.Lock()
		a.cleanupUpload(part, meta)
		a.uploadMu.Unlock()
		writeJSON(w, 200, map[string]any{"ok": true, "kind": "rtz", "area": area, "persistent": true})
	case "olex":
		incoming := filepath.Join(a.olex.dir, "incoming-olex-"+session.ID+".gz")
		if err := os.Rename(part, incoming); err != nil {
			writeError(w, 500, "Could not prepare OLEX database for indexing: "+err.Error())
			return
		}
		job, err := a.startOlexImportJob(session.Name, session.OriginalName, incoming, true)
		if err != nil {
			_ = os.Rename(incoming, part)
			writeError(w, 409, err.Error())
			return
		}
		a.uploadMu.Lock()
		_ = os.Remove(meta)
		a.uploadMu.Unlock()
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "kind": "olex", "id": job.ID, "name": job.Name, "status": job.Status, "totalBytes": job.TotalBytes, "persistent": true})
	default:
		writeError(w, 400, "Unknown upload type")
	}
}

func (a *App) handleUploadCancel(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var req uploadFinishRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a.uploadMu.Lock()
	defer a.uploadMu.Unlock()
	_, part, meta, err := a.locateUploadSession(strings.TrimSpace(req.ID))
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	a.cleanupUpload(part, meta)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
