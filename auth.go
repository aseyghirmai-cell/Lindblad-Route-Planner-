package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	authStateVersion          = 1
	passwordIterations        = 600000
	passwordSaltBytes         = 24
	passwordKeyBytes          = 32
	minimumPasswordCharacters = 15
	maximumPasswordBytes      = 256
	sessionIdleLifetime       = 12 * time.Hour
	sessionAbsoluteLifetime   = 7 * 24 * time.Hour
)

type PasswordRecord struct {
	Algorithm  string `json:"algorithm"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
}

type UserRecord struct {
	ID             string         `json:"id"`
	Username       string         `json:"username"`
	DisplayName    string         `json:"displayName"`
	Role           string         `json:"role"`
	OrganizationID string         `json:"organizationId"`
	Disabled       bool           `json:"disabled"`
	Password       PasswordRecord `json:"password"`
	CreatedUTC     string         `json:"createdUtc"`
	UpdatedUTC     string         `json:"updatedUtc"`
}

type OrganizationRecord struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	QuotaBytes int64  `json:"quotaBytes,omitempty"`
	CreatedUTC string `json:"createdUtc"`
	UpdatedUTC string `json:"updatedUtc"`
}

type SessionRecord struct {
	TokenHash   string `json:"tokenHash"`
	UserID      string `json:"userId"`
	CSRFToken   string `json:"csrfToken"`
	CreatedUTC  string `json:"createdUtc"`
	LastSeenUTC string `json:"lastSeenUtc"`
	ExpiresUTC  string `json:"expiresUtc"`
	AbsoluteUTC string `json:"absoluteUtc"`
}

type authState struct {
	Version       int                  `json:"version"`
	Users         []UserRecord         `json:"users"`
	Organizations []OrganizationRecord `json:"organizations"`
	Sessions      []SessionRecord      `json:"sessions"`
}

type loginAttempt struct {
	Count int
	Reset time.Time
}

type AuthStore struct {
	mu       sync.Mutex
	path     string
	state    authState
	pepper   []byte
	attempts map[string]loginAttempt
}

type AuthSession struct {
	User    UserRecord
	Session SessionRecord
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	return b, nil
}

func randomURLToken(n int) (string, error) {
	b, err := randomBytes(n)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func randomID(prefix string) (string, error) {
	b, err := randomBytes(12)
	if err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

// pbkdf2SHA256 implements PBKDF2-HMAC-SHA-256 without external dependencies.
func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	if iterations <= 0 || keyLen <= 0 {
		return nil
	}
	hLen := sha256.Size
	blocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, blocks*hLen)
	var counter [4]byte
	for block := 1; block <= blocks; block++ {
		counter[0] = byte(block >> 24)
		counter[1] = byte(block >> 16)
		counter[2] = byte(block >> 8)
		counter[3] = byte(block)
		mac := hmac.New(sha256.New, password)
		_, _ = mac.Write(salt)
		_, _ = mac.Write(counter[:])
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			mac = hmac.New(sha256.New, password)
			_, _ = mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

func passwordMaterial(password string, pepper []byte) []byte {
	raw := []byte(password)
	if len(pepper) == 0 {
		return raw
	}
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write(raw)
	return mac.Sum(nil)
}

func validatePassword(password string) error {
	if utf8.RuneCountInString(password) < minimumPasswordCharacters {
		return fmt.Errorf("password must contain at least %d characters", minimumPasswordCharacters)
	}
	if len([]byte(password)) > maximumPasswordBytes {
		return fmt.Errorf("password must not exceed %d UTF-8 bytes", maximumPasswordBytes)
	}
	return nil
}

func hashPassword(password string, pepper []byte) (PasswordRecord, error) {
	if err := validatePassword(password); err != nil {
		return PasswordRecord{}, err
	}
	salt, err := randomBytes(passwordSaltBytes)
	if err != nil {
		return PasswordRecord{}, err
	}
	key := pbkdf2SHA256(passwordMaterial(password, pepper), salt, passwordIterations, passwordKeyBytes)
	return PasswordRecord{
		Algorithm:  "pbkdf2-hmac-sha256",
		Iterations: passwordIterations,
		Salt:       base64.RawStdEncoding.EncodeToString(salt),
		Hash:       base64.RawStdEncoding.EncodeToString(key),
	}, nil
}

func verifyPassword(password string, rec PasswordRecord, pepper []byte) bool {
	if rec.Algorithm != "pbkdf2-hmac-sha256" || rec.Iterations < 100000 || rec.Iterations > 5000000 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(rec.Salt)
	expected, err2 := base64.RawStdEncoding.DecodeString(rec.Hash)
	if err1 != nil || err2 != nil || len(salt) < 16 || len(expected) != passwordKeyBytes {
		return false
	}
	got := pbkdf2SHA256(passwordMaterial(password, pepper), salt, rec.Iterations, len(expected))
	return subtle.ConstantTimeCompare(got, expected) == 1
}

func normalizeUsername(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func validUsername(s string) bool {
	s = normalizeUsername(s)
	if len(s) < 3 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' || r == '@' {
			continue
		}
		return false
	}
	return true
}

func validRole(role string) bool {
	return role == "admin" || role == "planner" || role == "viewer"
}

func NewAuthStore(root string) (*AuthStore, string, error) {
	authDir := filepath.Join(root, "auth")
	if err := os.MkdirAll(authDir, 0700); err != nil {
		return nil, "", err
	}
	a := &AuthStore{
		path:     filepath.Join(authDir, "accounts.json"),
		pepper:   []byte(os.Getenv("LRP_PASSWORD_PEPPER")),
		attempts: map[string]loginAttempt{},
	}
	if b, err := os.ReadFile(a.path); err == nil {
		if err := json.Unmarshal(b, &a.state); err != nil {
			return nil, "", fmt.Errorf("read account database: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, "", err
	}
	if a.state.Version == 0 {
		a.state.Version = authStateVersion
	}
	if a.state.Version != authStateVersion {
		return nil, "", fmt.Errorf("unsupported account database version %d", a.state.Version)
	}
	a.pruneSessionsLocked(time.Now().UTC())
	if len(a.state.Users) > 0 {
		if err := a.saveLocked(); err != nil {
			return nil, "", err
		}
		return a, "", nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	org := OrganizationRecord{ID: "org_main", Name: "Main", CreatedUTC: now, UpdatedUTC: now}
	username := normalizeUsername(os.Getenv("LRP_BOOTSTRAP_ADMIN_USERNAME"))
	if username == "" {
		username = "admin"
	}
	if !validUsername(username) {
		return nil, "", errors.New("LRP_BOOTSTRAP_ADMIN_USERNAME is invalid")
	}
	password := os.Getenv("LRP_BOOTSTRAP_ADMIN_PASSWORD")
	generated := false
	if password == "" {
		var err error
		password, err = randomURLToken(20)
		if err != nil {
			return nil, "", err
		}
		generated = true
	}
	pass, err := hashPassword(password, a.pepper)
	if err != nil {
		return nil, "", fmt.Errorf("bootstrap administrator password: %w", err)
	}
	userID, err := randomID("usr_")
	if err != nil {
		return nil, "", err
	}
	a.state.Organizations = []OrganizationRecord{org}
	a.state.Users = []UserRecord{{
		ID: userID, Username: username, DisplayName: "Administrator", Role: "admin",
		OrganizationID: org.ID, Password: pass, CreatedUTC: now, UpdatedUTC: now,
	}}
	if err := a.saveLocked(); err != nil {
		return nil, "", err
	}
	if generated {
		credentialPath := filepath.Join(authDir, "INITIAL_ADMIN_CREDENTIALS.txt")
		text := fmt.Sprintf("Username: %s\nPassword: %s\n\nDelete this file after the first successful login and password change.\n", username, password)
		if err := os.WriteFile(credentialPath, []byte(text), 0600); err != nil {
			return nil, "", err
		}
		return a, credentialPath, nil
	}
	return a, "", nil
}

func (a *AuthStore) saveLocked() error {
	a.state.Version = authStateVersion
	sort.SliceStable(a.state.Users, func(i, j int) bool { return a.state.Users[i].Username < a.state.Users[j].Username })
	sort.SliceStable(a.state.Organizations, func(i, j int) bool { return a.state.Organizations[i].Name < a.state.Organizations[j].Name })
	b, err := json.MarshalIndent(a.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, a.path)
}

func (a *AuthStore) pruneSessionsLocked(now time.Time) {
	out := a.state.Sessions[:0]
	for _, s := range a.state.Sessions {
		expires, e1 := time.Parse(time.RFC3339, s.ExpiresUTC)
		absolute, e2 := time.Parse(time.RFC3339, s.AbsoluteUTC)
		if e1 == nil && e2 == nil && now.Before(expires) && now.Before(absolute) {
			out = append(out, s)
		}
	}
	a.state.Sessions = out
}

func (a *AuthStore) findUserLocked(username string) (int, bool) {
	username = normalizeUsername(username)
	for i := range a.state.Users {
		if a.state.Users[i].Username == username {
			return i, true
		}
	}
	return -1, false
}

func (a *AuthStore) findUserIDLocked(id string) (int, bool) {
	for i := range a.state.Users {
		if a.state.Users[i].ID == id {
			return i, true
		}
	}
	return -1, false
}

func (a *AuthStore) findOrgLocked(id string) (int, bool) {
	for i := range a.state.Organizations {
		if a.state.Organizations[i].ID == id {
			return i, true
		}
	}
	return -1, false
}

func (a *AuthStore) AllowLoginAttempt(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	attempt := a.attempts[key]
	if attempt.Reset.IsZero() || now.After(attempt.Reset) {
		attempt = loginAttempt{Reset: now.Add(15 * time.Minute)}
	}
	if attempt.Count >= 6 {
		a.attempts[key] = attempt
		return false
	}
	attempt.Count++
	a.attempts[key] = attempt
	return true
}

func (a *AuthStore) ClearLoginAttempts(key string) {
	a.mu.Lock()
	delete(a.attempts, key)
	a.mu.Unlock()
}

func (a *AuthStore) Login(username, password string) (string, AuthSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	idx, ok := a.findUserLocked(username)
	if !ok || a.state.Users[idx].Disabled || !verifyPassword(password, a.state.Users[idx].Password, a.pepper) {
		return "", AuthSession{}, errors.New("invalid username or password")
	}
	rawToken, err := randomURLToken(32)
	if err != nil {
		return "", AuthSession{}, err
	}
	csrf, err := randomURLToken(32)
	if err != nil {
		return "", AuthSession{}, err
	}
	tokenHash := sha256.Sum256([]byte(rawToken))
	now := time.Now().UTC()
	session := SessionRecord{
		TokenHash: hex.EncodeToString(tokenHash[:]), UserID: a.state.Users[idx].ID, CSRFToken: csrf,
		CreatedUTC: now.Format(time.RFC3339), LastSeenUTC: now.Format(time.RFC3339),
		ExpiresUTC:  now.Add(sessionIdleLifetime).Format(time.RFC3339),
		AbsoluteUTC: now.Add(sessionAbsoluteLifetime).Format(time.RFC3339),
	}
	a.pruneSessionsLocked(now)
	a.state.Sessions = append(a.state.Sessions, session)
	if err := a.saveLocked(); err != nil {
		return "", AuthSession{}, err
	}
	return rawToken, AuthSession{User: a.state.Users[idx], Session: session}, nil
}

func (a *AuthStore) Authenticate(rawToken string) (AuthSession, bool) {
	if rawToken == "" {
		return AuthSession{}, false
	}
	hash := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(hash[:])
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now().UTC()
	a.pruneSessionsLocked(now)
	for i := range a.state.Sessions {
		s := &a.state.Sessions[i]
		if subtle.ConstantTimeCompare([]byte(s.TokenHash), []byte(tokenHash)) != 1 {
			continue
		}
		ui, ok := a.findUserIDLocked(s.UserID)
		if !ok || a.state.Users[ui].Disabled {
			return AuthSession{}, false
		}
		lastSeen, _ := time.Parse(time.RFC3339, s.LastSeenUTC)
		absolute, _ := time.Parse(time.RFC3339, s.AbsoluteUTC)
		if now.Sub(lastSeen) >= 10*time.Minute {
			s.LastSeenUTC = now.Format(time.RFC3339)
			next := now.Add(sessionIdleLifetime)
			if next.After(absolute) {
				next = absolute
			}
			s.ExpiresUTC = next.Format(time.RFC3339)
			_ = a.saveLocked()
		}
		return AuthSession{User: a.state.Users[ui], Session: *s}, true
	}
	_ = a.saveLocked()
	return AuthSession{}, false
}

func (a *AuthStore) Logout(rawToken string) {
	if rawToken == "" {
		return
	}
	hash := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(hash[:])
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.state.Sessions[:0]
	for _, s := range a.state.Sessions {
		if subtle.ConstantTimeCompare([]byte(s.TokenHash), []byte(tokenHash)) != 1 {
			out = append(out, s)
		}
	}
	a.state.Sessions = out
	_ = a.saveLocked()
}

func (a *AuthStore) revokeUserSessionsLocked(userID string) {
	out := a.state.Sessions[:0]
	for _, s := range a.state.Sessions {
		if s.UserID != userID {
			out = append(out, s)
		}
	}
	a.state.Sessions = out
}

func (a *AuthStore) ListUsers() []UserRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := append([]UserRecord(nil), a.state.Users...)
	for i := range out {
		out[i].Password = PasswordRecord{}
	}
	return out
}

func (a *AuthStore) ListOrganizations() []OrganizationRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]OrganizationRecord(nil), a.state.Organizations...)
}

func (a *AuthStore) Organization(id string) (OrganizationRecord, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	i, ok := a.findOrgLocked(id)
	if !ok {
		return OrganizationRecord{}, false
	}
	return a.state.Organizations[i], true
}

func (a *AuthStore) CreateOrganization(name string, quotaBytes int64) (OrganizationRecord, error) {
	name = strings.TrimSpace(name)
	if len(name) < 2 || len(name) > 100 {
		return OrganizationRecord{}, errors.New("organization name must contain 2 to 100 characters")
	}
	if quotaBytes < 0 {
		return OrganizationRecord{}, errors.New("quota cannot be negative")
	}
	id, err := randomID("org_")
	if err != nil {
		return OrganizationRecord{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	org := OrganizationRecord{ID: id, Name: name, QuotaBytes: quotaBytes, CreatedUTC: now, UpdatedUTC: now}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, existing := range a.state.Organizations {
		if strings.EqualFold(existing.Name, name) {
			return OrganizationRecord{}, errors.New("an organization with that name already exists")
		}
	}
	a.state.Organizations = append(a.state.Organizations, org)
	return org, a.saveLocked()
}

func (a *AuthStore) UpdateOrganization(id, name string, quotaBytes int64) (OrganizationRecord, error) {
	name = strings.TrimSpace(name)
	if len(name) < 2 || len(name) > 100 || quotaBytes < 0 {
		return OrganizationRecord{}, errors.New("invalid organization name or quota")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	i, ok := a.findOrgLocked(id)
	if !ok {
		return OrganizationRecord{}, errors.New("organization not found")
	}
	for j, existing := range a.state.Organizations {
		if j != i && strings.EqualFold(existing.Name, name) {
			return OrganizationRecord{}, errors.New("an organization with that name already exists")
		}
	}
	a.state.Organizations[i].Name = name
	a.state.Organizations[i].QuotaBytes = quotaBytes
	a.state.Organizations[i].UpdatedUTC = time.Now().UTC().Format(time.RFC3339)
	return a.state.Organizations[i], a.saveLocked()
}

func (a *AuthStore) CreateUser(username, displayName, role, orgID, password string) (UserRecord, error) {
	username = normalizeUsername(username)
	displayName = strings.TrimSpace(displayName)
	role = strings.ToLower(strings.TrimSpace(role))
	if !validUsername(username) {
		return UserRecord{}, errors.New("username must contain 3 to 64 lowercase letters, numbers, dots, underscores, hyphens or @")
	}
	if displayName == "" {
		displayName = username
	}
	if len(displayName) > 100 || !validRole(role) {
		return UserRecord{}, errors.New("invalid display name or role")
	}
	pass, err := hashPassword(password, a.pepper)
	if err != nil {
		return UserRecord{}, err
	}
	id, err := randomID("usr_")
	if err != nil {
		return UserRecord{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	user := UserRecord{ID: id, Username: username, DisplayName: displayName, Role: role, OrganizationID: orgID, Password: pass, CreatedUTC: now, UpdatedUTC: now}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.findUserLocked(username); ok {
		return UserRecord{}, errors.New("username already exists")
	}
	if _, ok := a.findOrgLocked(orgID); !ok {
		return UserRecord{}, errors.New("organization not found")
	}
	a.state.Users = append(a.state.Users, user)
	if err := a.saveLocked(); err != nil {
		return UserRecord{}, err
	}
	user.Password = PasswordRecord{}
	return user, nil
}

func (a *AuthStore) ResetPassword(userID, password string) error {
	pass, err := hashPassword(password, a.pepper)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	i, ok := a.findUserIDLocked(userID)
	if !ok {
		return errors.New("user not found")
	}
	a.state.Users[i].Password = pass
	a.state.Users[i].UpdatedUTC = time.Now().UTC().Format(time.RFC3339)
	a.revokeUserSessionsLocked(userID)
	return a.saveLocked()
}

func (a *AuthStore) UpdateUser(userID, displayName, role, orgID string, disabled bool) (UserRecord, error) {
	displayName = strings.TrimSpace(displayName)
	role = strings.ToLower(strings.TrimSpace(role))
	if displayName == "" || len(displayName) > 100 || !validRole(role) {
		return UserRecord{}, errors.New("invalid display name or role")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	i, ok := a.findUserIDLocked(userID)
	if !ok {
		return UserRecord{}, errors.New("user not found")
	}
	if _, ok := a.findOrgLocked(orgID); !ok {
		return UserRecord{}, errors.New("organization not found")
	}
	if a.state.Users[i].Role == "admin" && (role != "admin" || disabled) {
		admins := 0
		for _, u := range a.state.Users {
			if u.Role == "admin" && !u.Disabled {
				admins++
			}
		}
		if admins <= 1 {
			return UserRecord{}, errors.New("at least one enabled administrator is required")
		}
	}
	a.state.Users[i].DisplayName = displayName
	a.state.Users[i].Role = role
	a.state.Users[i].OrganizationID = orgID
	a.state.Users[i].Disabled = disabled
	a.state.Users[i].UpdatedUTC = time.Now().UTC().Format(time.RFC3339)
	if disabled {
		a.revokeUserSessionsLocked(userID)
	}
	if err := a.saveLocked(); err != nil {
		return UserRecord{}, err
	}
	out := a.state.Users[i]
	out.Password = PasswordRecord{}
	return out, nil
}

func (a *AuthStore) DeleteUser(userID, actorID string) error {
	if userID == actorID {
		return errors.New("you cannot delete your own account")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	idx, ok := a.findUserIDLocked(userID)
	if !ok {
		return errors.New("user not found")
	}
	if a.state.Users[idx].Role == "admin" && !a.state.Users[idx].Disabled {
		admins := 0
		for _, u := range a.state.Users {
			if u.Role == "admin" && !u.Disabled {
				admins++
			}
		}
		if admins <= 1 {
			return errors.New("at least one enabled administrator is required")
		}
	}
	a.revokeUserSessionsLocked(userID)
	a.state.Users = append(a.state.Users[:idx], a.state.Users[idx+1:]...)
	return a.saveLocked()
}

func (a *AuthStore) ChangePassword(userID, currentPassword, newPassword string) error {
	pass, err := hashPassword(newPassword, a.pepper)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	i, ok := a.findUserIDLocked(userID)
	if !ok || a.state.Users[i].Disabled {
		return errors.New("user not found")
	}
	if !verifyPassword(currentPassword, a.state.Users[i].Password, a.pepper) {
		return errors.New("current password is incorrect")
	}
	a.state.Users[i].Password = pass
	a.state.Users[i].UpdatedUTC = time.Now().UTC().Format(time.RFC3339)
	a.revokeUserSessionsLocked(userID)
	return a.saveLocked()
}
