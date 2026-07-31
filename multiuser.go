package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const publicEngineName = "Lindblad Route Planner Cloud 1.0"

var publicAuditWriteMu sync.Mutex

type authContextKey struct{}

type PublicServer struct {
	root          string
	mainOlexDir   string
	basePlanner   *PlannerData
	land          *LandMask
	defaultOlex   []byte
	assets        map[string][]byte
	auth          *AuthStore
	logger        *log.Logger
	publicURL     *url.URL
	trustProxy    bool
	secureCookies bool
	cookieName    string

	mu       sync.Mutex
	apps     map[string]*App
	handlers map[string]http.Handler
	server   *http.Server
	done     chan struct{}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func NewPublicServer(root string, basePlanner *PlannerData, land *LandMask, defaultOlex []byte, assets map[string][]byte, logger *log.Logger) (*PublicServer, error) {
	auth, credentialPath, err := NewAuthStore(root)
	if err != nil {
		return nil, err
	}
	rawPublicURL := strings.TrimSpace(os.Getenv("LRP_PUBLIC_URL"))
	if rawPublicURL == "" {
		// Render provides the public HTTPS URL automatically. A custom domain can
		// still be pinned explicitly with LRP_PUBLIC_URL.
		rawPublicURL = strings.TrimSpace(os.Getenv("RENDER_EXTERNAL_URL"))
	}
	var publicURL *url.URL
	if rawPublicURL != "" {
		publicURL, err = url.Parse(rawPublicURL)
		if err != nil || publicURL.Host == "" {
			return nil, errors.New("LRP_PUBLIC_URL must be a complete URL such as https://planner.example.com")
		}
	}
	secureCookies := publicURL != nil && strings.EqualFold(publicURL.Scheme, "https")
	if envBool("LRP_FORCE_SECURE_COOKIES") {
		secureCookies = true
	}
	if !secureCookies && !envBool("LRP_ALLOW_INSECURE_HTTP") {
		return nil, errors.New("public mode requires HTTPS: set LRP_PUBLIC_URL=https://your-domain or use LRP_ALLOW_INSECURE_HTTP=1 only for local testing")
	}
	cookieName := "lrp_session_dev"
	if secureCookies {
		cookieName = "__Host-lrp_session"
	}
	s := &PublicServer{
		root: root, mainOlexDir: configuredOlexDir(filepath.Join(root, "ai_corridor_olex"), logger), basePlanner: basePlanner, land: land, defaultOlex: defaultOlex,
		assets: assets, auth: auth, logger: logger, publicURL: publicURL,
		trustProxy: envBool("LRP_TRUST_PROXY"), secureCookies: secureCookies,
		cookieName: cookieName, apps: map[string]*App{}, handlers: map[string]http.Handler{}, done: make(chan struct{}),
	}
	if credentialPath != "" {
		logger.Printf("initial administrator credentials created at %s", credentialPath)
		fmt.Fprintf(os.Stderr, "Initial administrator credentials: %s\n", credentialPath)
	}
	for _, org := range auth.ListOrganizations() {
		olexDir, _, _ := s.orgPaths(org.ID)
		if pathExists(filepath.Join(olexDir, "pending_olex_import.json")) {
			if _, _, loadErr := s.orgApp(org.ID); loadErr != nil {
				logger.Printf("could not resume pending OLEX import for %s: %v", org.Name, loadErr)
			}
		}
	}
	return s, nil
}

func (s *PublicServer) clientIP(r *http.Request) string {
	if s.trustProxy {
		if v := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); v != "" {
			return strings.TrimSpace(strings.Split(v, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *PublicServer) requestHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if s.trustProxy {
		return strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
	}
	return false
}

func (s *PublicServer) expectedHost(r *http.Request) string {
	if s.publicURL != nil {
		return strings.ToLower(s.publicURL.Host)
	}
	return strings.ToLower(r.Host)
}

func (s *PublicServer) sameOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, s.expectedHost(r))
}

func (s *PublicServer) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.publicURL != nil {
			host := strings.TrimSpace(r.Host)
			if s.trustProxy {
				if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwarded != "" {
					host = strings.TrimSpace(strings.Split(forwarded, ",")[0])
				}
			}
			if !strings.EqualFold(host, s.publicURL.Host) {
				http.Error(w, "Invalid host", http.StatusMisdirectedRequest)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
		if s.requestHTTPS(r) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *PublicServer) setSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	cookie := &http.Cookie{
		Name: s.cookieName, Value: token, Path: "/", HttpOnly: true,
		Secure: s.secureCookies, SameSite: http.SameSiteStrictMode, MaxAge: maxAge,
	}
	if maxAge < 0 {
		cookie.Expires = time.Unix(1, 0)
	}
	http.SetCookie(w, cookie)
}

func (s *PublicServer) tokenFromRequest(r *http.Request) string {
	c, err := r.Cookie(s.cookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func sessionFromContext(r *http.Request) (AuthSession, bool) {
	v, ok := r.Context().Value(authContextKey{}).(AuthSession)
	return v, ok
}

func (s *PublicServer) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, ok := s.auth.Authenticate(s.tokenFromRequest(r))
		if !ok {
			s.setSessionCookie(w, "", -1)
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeError(w, http.StatusUnauthorized, "Authentication required")
			} else {
				http.Redirect(w, r, "/login.html", http.StatusSeeOther)
			}
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if !s.sameOrigin(r) {
				writeError(w, http.StatusForbidden, "Cross-site request blocked")
				return
			}
			supplied := r.Header.Get("X-CSRF-Token")
			if subtle.ConstantTimeCompare([]byte(supplied), []byte(session.Session.CSRFToken)) != 1 {
				writeError(w, http.StatusForbidden, "Missing or invalid CSRF token")
				return
			}
		}
		ctx := context.WithValue(r.Context(), authContextKey{}, session)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *PublicServer) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/login.html" && r.URL.Path != "/login" {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.auth.Authenticate(s.tokenFromRequest(r)); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.serveAsset(w, "login.html", "text/html; charset=utf-8")
}

func (s *PublicServer) serveAsset(w http.ResponseWriter, name, contentType string) {
	b, ok := s.assets[name]
	if !ok {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (s *PublicServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "Cross-site request blocked")
		return
	}
	if s.secureCookies && !s.requestHTTPS(r) {
		writeError(w, http.StatusUpgradeRequired, "HTTPS is required")
		return
	}
	var q struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	d := json.NewDecoder(io.LimitReader(r.Body, 16<<10))
	if err := d.Decode(&q); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid login request")
		return
	}
	q.Username = normalizeUsername(q.Username)
	key := s.clientIP(r) + "|" + q.Username
	if !s.auth.AllowLoginAttempt(key) {
		writeError(w, http.StatusTooManyRequests, "Too many login attempts. Try again later.")
		return
	}
	token, session, err := s.auth.Login(q.Username, q.Password)
	if err != nil {
		time.Sleep(500 * time.Millisecond)
		s.audit(r, nil, "auth.login_failed", http.StatusUnauthorized, map[string]any{"username": q.Username})
		writeError(w, http.StatusUnauthorized, "Invalid username or password")
		return
	}
	s.auth.ClearLoginAttempts(key)
	s.setSessionCookie(w, token, int(sessionIdleLifetime.Seconds()))
	s.audit(r, &session, "auth.login", http.StatusOK, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "redirect": "/"})
}

func (s *PublicServer) handleMe(w http.ResponseWriter, r *http.Request) {
	session, _ := sessionFromContext(r)
	org, _ := s.auth.Organization(session.User.OrganizationID)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": session.User.ID, "username": session.User.Username,
		"displayName": session.User.DisplayName, "role": session.User.Role,
		"organization": org, "csrfToken": session.Session.CSRFToken,
		"engine": publicEngineName,
	})
}

func (s *PublicServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	session, _ := sessionFromContext(r)
	s.auth.Logout(s.tokenFromRequest(r))
	s.setSessionCookie(w, "", -1)
	s.audit(r, &session, "auth.logout", http.StatusOK, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *PublicServer) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	session, _ := sessionFromContext(r)
	var q struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeJSON(w, r, &q) {
		return
	}
	if err := s.auth.ChangePassword(session.User.ID, q.CurrentPassword, q.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.setSessionCookie(w, "", -1)
	credentialPath := filepath.Join(s.root, "auth", "INITIAL_ADMIN_CREDENTIALS.txt")
	_ = os.Remove(credentialPath)
	s.audit(r, &session, "auth.password_changed", http.StatusOK, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reauthenticate": true})
}

func (s *PublicServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		http.NotFound(w, r)
		return
	}
	s.serveAsset(w, "public_index.html", "text/html; charset=utf-8")
}

func (s *PublicServer) orgPaths(orgID string) (olexDir, rtzDir, uploadDir string) {
	// The first organization uses existing AI Corridor 2.6 storage in place when present.
	// This avoids duplicating tens of gigabytes during migration.
	if orgID == "org_main" {
		return s.mainOlexDir, filepath.Join(s.root, "rtz_library"), filepath.Join(s.root, "persistent_uploads")
	}
	base := filepath.Join(s.root, "organizations", orgID)
	return filepath.Join(base, "olex"), filepath.Join(base, "rtz"), filepath.Join(base, "uploads")
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (s *PublicServer) orgApp(orgID string) (*App, http.Handler, error) {
	if _, ok := s.auth.Organization(orgID); !ok {
		return nil, nil, errors.New("organization not found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if app := s.apps[orgID]; app != nil {
		return app, s.handlers[orgID], nil
	}
	olexDir, rtzDir, uploadDir := s.orgPaths(orgID)
	olex, err := NewOlexLibrary(olexDir, s.defaultOlex)
	if err != nil {
		return nil, nil, err
	}
	rtz, err := NewRTZLibrary(rtzDir)
	if err != nil {
		return nil, nil, err
	}
	planner, err := rtz.BuildPlanner(s.basePlanner)
	if err != nil {
		return nil, nil, err
	}
	orgLog := log.New(s.logger.Writer(), "["+orgID+"] ", log.LstdFlags|log.Lmicroseconds)
	app := NewApp(s.basePlanner, planner, olex, rtz, s.land, uploadDir, s.assets, orgLog)
	app.publicMode = true
	app.recoverPendingOlexImport()
	handler := app.publicAPIRoutes()
	s.apps[orgID] = app
	s.handlers[orgID] = handler
	return app, handler, nil
}

func (s *PublicServer) viewerBlocked(path string) bool {
	prefixes := []string{"/api/upload/", "/api/managed/", "/api/olex/import", "/api/rtz/import"}
	for _, p := range prefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

func (s *PublicServer) localOnly(path string) bool {
	return path == "/api/shutdown" || path == "/api/olex/import-path" || path == "/api/rtz/import-path" || path == "/api/olex/import"
}

func (s *PublicServer) enforceQuota(w http.ResponseWriter, r *http.Request, session AuthSession, app *App) bool {
	if r.URL.Path != "/api/upload/start" || r.Method != http.MethodPost {
		return true
	}
	org, ok := s.auth.Organization(session.User.OrganizationID)
	if !ok || org.QuotaBytes <= 0 {
		return true
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "Could not read upload request")
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var q uploadStartRequest
	if err := json.Unmarshal(raw, &q); err != nil {
		return true // The tenant handler will return the normal validation error.
	}
	used := app.storageBytes()
	if q.SizeBytes > 0 && used+q.SizeBytes > org.QuotaBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "This upload would exceed the organization's storage quota")
		return false
	}
	return true
}

func (s *PublicServer) dispatchTenantAPI(w http.ResponseWriter, r *http.Request) {
	session, _ := sessionFromContext(r)
	if s.localOnly(r.URL.Path) {
		writeError(w, http.StatusForbidden, "This server-side filesystem operation is disabled on the public service")
		return
	}
	if session.User.Role == "viewer" && (r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions || s.viewerBlocked(r.URL.Path)) {
		writeError(w, http.StatusForbidden, "Your account is read-only")
		return
	}
	app, handler, err := s.orgApp(session.User.OrganizationID)
	if err != nil {
		s.logger.Printf("load organization %s: %v", session.User.OrganizationID, err)
		writeError(w, http.StatusInternalServerError, "Could not load the organization workspace")
		return
	}
	if !s.enforceQuota(w, r, session, app) {
		return
	}
	rec := &statusRecorder{ResponseWriter: w}
	handler.ServeHTTP(rec, r)
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
		s.audit(r, &session, "api.request", rec.status, map[string]any{"path": r.URL.Path, "method": r.Method})
	}
}

func (s *PublicServer) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, ok := sessionFromContext(r)
		if !ok || session.User.Role != "admin" {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeError(w, http.StatusForbidden, "Administrator access required")
			} else {
				http.Error(w, "Administrator access required", http.StatusForbidden)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

type userView struct {
	ID             string `json:"id"`
	Username       string `json:"username"`
	DisplayName    string `json:"displayName"`
	Role           string `json:"role"`
	OrganizationID string `json:"organizationId"`
	Disabled       bool   `json:"disabled"`
	CreatedUTC     string `json:"createdUtc"`
	UpdatedUTC     string `json:"updatedUtc"`
}

func safeUser(u UserRecord) userView {
	return userView{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Role: u.Role, OrganizationID: u.OrganizationID, Disabled: u.Disabled, CreatedUTC: u.CreatedUTC, UpdatedUTC: u.UpdatedUTC}
}

func (s *PublicServer) handleAdminData(w http.ResponseWriter, r *http.Request) {
	rawUsers := s.auth.ListUsers()
	users := make([]userView, 0, len(rawUsers))
	for _, u := range rawUsers {
		users = append(users, safeUser(u))
	}
	orgs := s.auth.ListOrganizations()
	storage := map[string]int64{}
	for _, org := range orgs {
		if app, _, err := s.orgApp(org.ID); err == nil {
			storage[org.ID] = app.storageBytes()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users, "organizations": orgs, "storageBytes": storage})
}

func (s *PublicServer) handleAdminCreateOrg(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var q struct {
		Name    string  `json:"name"`
		QuotaGB float64 `json:"quotaGB"`
	}
	if !decodeJSON(w, r, &q) {
		return
	}
	quota := int64(0)
	if q.QuotaGB > 0 {
		quota = int64(q.QuotaGB * 1_000_000_000)
	}
	org, err := s.auth.CreateOrganization(q.Name, quota)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, _ := sessionFromContext(r)
	s.audit(r, &session, "admin.organization.create", http.StatusOK, map[string]any{"organizationId": org.ID})
	writeJSON(w, http.StatusOK, org)
}

func (s *PublicServer) handleAdminUpdateOrg(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var q struct {
		ID      string  `json:"id"`
		Name    string  `json:"name"`
		QuotaGB float64 `json:"quotaGB"`
	}
	if !decodeJSON(w, r, &q) {
		return
	}
	quota := int64(0)
	if q.QuotaGB > 0 {
		quota = int64(q.QuotaGB * 1_000_000_000)
	}
	org, err := s.auth.UpdateOrganization(q.ID, q.Name, quota)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, _ := sessionFromContext(r)
	s.audit(r, &session, "admin.organization.update", http.StatusOK, map[string]any{"organizationId": org.ID})
	writeJSON(w, http.StatusOK, org)
}

func (s *PublicServer) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var q struct {
		Username       string `json:"username"`
		DisplayName    string `json:"displayName"`
		Password       string `json:"password"`
		Role           string `json:"role"`
		OrganizationID string `json:"organizationId"`
	}
	if !decodeJSON(w, r, &q) {
		return
	}
	user, err := s.auth.CreateUser(q.Username, q.DisplayName, q.Role, q.OrganizationID, q.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, _ := sessionFromContext(r)
	s.audit(r, &session, "admin.user.create", http.StatusOK, map[string]any{"userId": user.ID, "username": user.Username})
	writeJSON(w, http.StatusOK, safeUser(user))
}

func (s *PublicServer) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var q struct {
		ID             string `json:"id"`
		DisplayName    string `json:"displayName"`
		Role           string `json:"role"`
		OrganizationID string `json:"organizationId"`
		Disabled       bool   `json:"disabled"`
	}
	if !decodeJSON(w, r, &q) {
		return
	}
	user, err := s.auth.UpdateUser(q.ID, q.DisplayName, q.Role, q.OrganizationID, q.Disabled)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, _ := sessionFromContext(r)
	s.audit(r, &session, "admin.user.update", http.StatusOK, map[string]any{"userId": user.ID})
	writeJSON(w, http.StatusOK, safeUser(user))
}

func (s *PublicServer) handleAdminPassword(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var q struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &q) {
		return
	}
	if err := s.auth.ResetPassword(q.ID, q.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	session, _ := sessionFromContext(r)
	s.audit(r, &session, "admin.user.password_reset", http.StatusOK, map[string]any{"userId": q.ID})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *PublicServer) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodDelete) {
		return
	}
	session, _ := sessionFromContext(r)
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if err := s.auth.DeleteUser(id, session.User.ID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, &session, "admin.user.delete", http.StatusOK, map[string]any{"userId": id})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *PublicServer) audit(r *http.Request, session *AuthSession, action string, status int, details any) {
	entry := map[string]any{
		"utc": time.Now().UTC().Format(time.RFC3339Nano), "action": action,
		"status": status, "ip": s.clientIP(r),
	}
	if session != nil {
		entry["userId"] = session.User.ID
		entry["username"] = session.User.Username
		entry["organizationId"] = session.User.OrganizationID
	}
	if details != nil {
		entry["details"] = details
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	path := filepath.Join(s.root, "audit", "audit.jsonl")
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	publicAuditWriteMu.Lock()
	defer publicAuditWriteMu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		s.logger.Printf("audit: %v", err)
		return
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
}

func (s *PublicServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "engine": publicEngineName, "utc": time.Now().UTC()})
	})
	mux.HandleFunc("/api/auth/login", s.handleLogin)
	mux.HandleFunc("/login", s.handleLoginPage)
	mux.HandleFunc("/login.html", s.handleLoginPage)
	mux.HandleFunc("/login.css", func(w http.ResponseWriter, r *http.Request) { s.serveAsset(w, "login.css", "text/css; charset=utf-8") })
	mux.HandleFunc("/login.js", func(w http.ResponseWriter, r *http.Request) {
		s.serveAsset(w, "login.js", "application/javascript; charset=utf-8")
	})

	authenticated := http.NewServeMux()
	authenticated.HandleFunc("/", s.handleIndex)
	authenticated.HandleFunc("/index.html", s.handleIndex)
	authenticated.HandleFunc("/styles.css", func(w http.ResponseWriter, r *http.Request) { s.serveAsset(w, "styles.css", "text/css; charset=utf-8") })
	authenticated.HandleFunc("/account.css", func(w http.ResponseWriter, r *http.Request) {
		s.serveAsset(w, "account.css", "text/css; charset=utf-8")
	})
	authenticated.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		s.serveAsset(w, "public_app.js", "application/javascript; charset=utf-8")
	})
	authenticated.HandleFunc("/library.js", func(w http.ResponseWriter, r *http.Request) {
		s.serveAsset(w, "public_library.js", "application/javascript; charset=utf-8")
	})
	authenticated.HandleFunc("/cloud_editor.js", func(w http.ResponseWriter, r *http.Request) {
		s.serveAsset(w, "cloud_editor.js", "application/javascript; charset=utf-8")
	})
	authenticated.HandleFunc("/account.js", func(w http.ResponseWriter, r *http.Request) {
		s.serveAsset(w, "account.js", "application/javascript; charset=utf-8")
	})
	authenticated.HandleFunc("/api/auth/me", s.handleMe)
	authenticated.HandleFunc("/api/auth/logout", s.handleLogout)
	authenticated.HandleFunc("/api/auth/change-password", s.handleChangePassword)

	admin := http.NewServeMux()
	admin.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		s.serveAsset(w, "admin.html", "text/html; charset=utf-8")
	})
	admin.HandleFunc("/admin.html", func(w http.ResponseWriter, r *http.Request) {
		s.serveAsset(w, "admin.html", "text/html; charset=utf-8")
	})
	admin.HandleFunc("/admin.css", func(w http.ResponseWriter, r *http.Request) { s.serveAsset(w, "admin.css", "text/css; charset=utf-8") })
	admin.HandleFunc("/admin.js", func(w http.ResponseWriter, r *http.Request) {
		s.serveAsset(w, "admin.js", "application/javascript; charset=utf-8")
	})
	admin.HandleFunc("/api/admin/data", s.handleAdminData)
	admin.HandleFunc("/api/admin/organizations/create", s.handleAdminCreateOrg)
	admin.HandleFunc("/api/admin/organizations/update", s.handleAdminUpdateOrg)
	admin.HandleFunc("/api/admin/users/create", s.handleAdminCreateUser)
	admin.HandleFunc("/api/admin/users/update", s.handleAdminUpdateUser)
	admin.HandleFunc("/api/admin/users/password", s.handleAdminPassword)
	admin.HandleFunc("/api/admin/users/delete", s.handleAdminDeleteUser)
	authenticated.Handle("/admin", s.requireAdmin(admin))
	authenticated.Handle("/admin.html", s.requireAdmin(admin))
	authenticated.Handle("/admin.css", s.requireAdmin(admin))
	authenticated.Handle("/admin.js", s.requireAdmin(admin))
	authenticated.Handle("/api/admin/", s.requireAdmin(admin))
	authenticated.HandleFunc("/api/", s.dispatchTenantAPI)

	mux.Handle("/", s.requireAuth(authenticated))
	return recoveryMiddleware(s.logger, s.securityHeaders(mux))
}

func (s *PublicServer) Serve(listenAddr string) (string, error) {
	if strings.TrimSpace(listenAddr) == "" {
		listenAddr = "127.0.0.1:8787"
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return "", err
	}
	s.server = &http.Server{
		Handler: s.routes(), ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	display := "http://" + ln.Addr().String() + "/"
	if s.publicURL != nil {
		display = s.publicURL.String()
	}
	go func() {
		defer close(s.done)
		if err := s.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.logger.Printf("public server: %v", err)
		}
	}()
	return display, nil
}

func (s *PublicServer) Shutdown() error {
	if s.server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}

func parseQuotaGB(raw string) int64 {
	n, _ := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if n <= 0 {
		return 0
	}
	return int64(n * 1_000_000_000)
}
