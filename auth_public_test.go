package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPBKDF2SHA256KnownVector(t *testing.T) {
	got := pbkdf2SHA256([]byte("password"), []byte("salt"), 2, 32)
	const want = "ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43"
	if hex.EncodeToString(got) != want {
		t.Fatalf("PBKDF2 mismatch: got %x want %s", got, want)
	}
}

func TestAuthStorePersistsHashedCredentialsAndSessions(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LRP_BOOTSTRAP_ADMIN_PASSWORD", "Bootstrap-Password-2026!")
	t.Setenv("LRP_PASSWORD_PEPPER", "test-pepper")
	auth, _, err := NewAuthStore(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "auth", "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("Bootstrap-Password-2026!")) {
		t.Fatal("plaintext password was written to disk")
	}
	if !bytes.Contains(raw, []byte("pbkdf2-hmac-sha256")) {
		t.Fatal("password algorithm metadata is missing")
	}
	token, session, err := auth.Login("admin", "Bootstrap-Password-2026!")
	if err != nil {
		t.Fatal(err)
	}
	if session.User.Role != "admin" {
		t.Fatalf("unexpected bootstrap role: %s", session.User.Role)
	}
	restarted, _, err := NewAuthStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := restarted.Authenticate(token); !ok {
		t.Fatal("persisted session was not valid after restart")
	}
}

func TestPublicServerRequiresSessionAndCSRF(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LRP_ALLOW_INSECURE_HTTP", "1")
	t.Setenv("LRP_BOOTSTRAP_ADMIN_PASSWORD", "Public-Test-Password-2026!")
	t.Setenv("LINDBLAD_OLEX_LIBRARY", filepath.Join(root, "olex"))
	logger := log.New(io.Discard, "", 0)
	server, err := NewPublicServer(root, tinyPlanner(), nil, nil, map[string][]byte{}, logger)
	if err != nil {
		t.Fatal(err)
	}
	h := server.routes()

	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "http://example.test/api/status", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", unauth.Code, unauth.Body.String())
	}

	loginBody := strings.NewReader(`{"username":"admin","password":"Public-Test-Password-2026!"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "http://example.test/api/auth/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/json")
	login := httptest.NewRecorder()
	h.ServeHTTP(login, loginReq)
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range login.Result().Cookies() {
		if c.Name == server.cookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("session cookie missing")
	}

	meReq := httptest.NewRequest(http.MethodGet, "http://example.test/api/auth/me", nil)
	meReq.AddCookie(cookie)
	me := httptest.NewRecorder()
	h.ServeHTTP(me, meReq)
	if me.Code != http.StatusOK {
		t.Fatalf("me status=%d body=%s", me.Code, me.Body.String())
	}
	var meData map[string]any
	if err := json.Unmarshal(me.Body.Bytes(), &meData); err != nil {
		t.Fatal(err)
	}
	csrf, _ := meData["csrfToken"].(string)
	if csrf == "" {
		t.Fatal("CSRF token missing")
	}

	payload := []byte(`{"date":"2026-07-31","time":"12:00","zone":"UTC"}`)
	blockedReq := httptest.NewRequest(http.MethodPost, "http://example.test/api/time/convert", bytes.NewReader(payload))
	blockedReq.AddCookie(cookie)
	blockedReq.Header.Set("Content-Type", "application/json")
	blocked := httptest.NewRecorder()
	h.ServeHTTP(blocked, blockedReq)
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d body=%s", blocked.Code, blocked.Body.String())
	}

	allowedReq := httptest.NewRequest(http.MethodPost, "http://example.test/api/time/convert", bytes.NewReader(payload))
	allowedReq.AddCookie(cookie)
	allowedReq.Header.Set("Content-Type", "application/json")
	allowedReq.Header.Set("X-CSRF-Token", csrf)
	allowed := httptest.NewRecorder()
	h.ServeHTTP(allowed, allowedReq)
	if allowed.Code != http.StatusOK {
		t.Fatalf("valid CSRF status=%d body=%s", allowed.Code, allowed.Body.String())
	}
}

func TestViewerCannotMutateOrganizationWorkspace(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LRP_ALLOW_INSECURE_HTTP", "1")
	t.Setenv("LRP_BOOTSTRAP_ADMIN_PASSWORD", "Viewer-Test-Admin-Password-2026!")
	t.Setenv("LRP_PASSWORD_PEPPER", "viewer-test-pepper")
	t.Setenv("LINDBLAD_OLEX_LIBRARY", filepath.Join(root, "olex"))
	logger := log.New(io.Discard, "", 0)
	server, err := NewPublicServer(root, tinyPlanner(), nil, nil, map[string][]byte{}, logger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.auth.CreateUser("viewer", "Read Only", "viewer", "org_main", "Viewer-Password-2026!"); err != nil {
		t.Fatal(err)
	}
	h := server.routes()
	loginReq := httptest.NewRequest(http.MethodPost, "http://example.test/api/auth/login", strings.NewReader(`{"username":"viewer","password":"Viewer-Password-2026!"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	login := httptest.NewRecorder()
	h.ServeHTTP(login, loginReq)
	if login.Code != http.StatusOK {
		t.Fatalf("viewer login status=%d body=%s", login.Code, login.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range login.Result().Cookies() {
		if c.Name == server.cookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("viewer session cookie missing")
	}
	meReq := httptest.NewRequest(http.MethodGet, "http://example.test/api/auth/me", nil)
	meReq.AddCookie(cookie)
	me := httptest.NewRecorder()
	h.ServeHTTP(me, meReq)
	var meData map[string]any
	if err := json.Unmarshal(me.Body.Bytes(), &meData); err != nil {
		t.Fatal(err)
	}
	csrf, _ := meData["csrfToken"].(string)
	mutation := httptest.NewRequest(http.MethodPost, "http://example.test/api/time/convert", strings.NewReader(`{"date":"2026-07-31","time":"12:00","zone":"UTC"}`))
	mutation.AddCookie(cookie)
	mutation.Header.Set("Content-Type", "application/json")
	mutation.Header.Set("X-CSRF-Token", csrf)
	blocked := httptest.NewRecorder()
	h.ServeHTTP(blocked, mutation)
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("viewer mutation status=%d body=%s", blocked.Code, blocked.Body.String())
	}
}
