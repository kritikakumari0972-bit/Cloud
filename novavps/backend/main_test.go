package main

import (
	"os"
	"path/filepath"
	"strings"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestAdminPasswordEnvironmentOverridesSavedHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin_password.hash")
	oldHash, err := bcrypt.GenerateFromPassword([]byte("previous-test-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, oldHash, 0600); err != nil {
		t.Fatal(err)
	}

	const configuredPassword = "new-test-password-123"
	t.Setenv("NOVAVPS_ADMIN_PASSWORD", configuredPassword)

	plain, hash, created, err := loadOrCreateAdminCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "" {
		t.Fatal("configured password must not be returned for logging")
	}
	if created {
		t.Fatal("environment-provided password must not be treated as a generated password")
	}
	if err := bcrypt.CompareHashAndPassword(hash, []byte(configuredPassword)); err != nil {
		t.Fatal("returned hash does not match configured password")
	}
	if err := bcrypt.CompareHashAndPassword(hash, []byte("previous-test-password")); err == nil {
		t.Fatal("previous password still matches after rotation")
	}

	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword(stored, []byte(configuredPassword)); err != nil {
		t.Fatal("persisted hash does not match configured password")
	}
}

func TestAdminPasswordEnvironmentRejectsShortPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin_password.hash")
	t.Setenv("NOVAVPS_ADMIN_PASSWORD", "too-short")

	if _, _, _, err := loadOrCreateAdminCredentials(path); err == nil {
		t.Fatal("expected short environment password to be rejected")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("short password must not create a hash file")
	}
}

func TestFreshInstallWaitsForOwnerSetup(t *testing.T) {
	t.Setenv("NOVAVPS_ADMIN_PASSWORD", "")
	path := filepath.Join(t.TempDir(), "admin_password.hash")
	plain, hash, created, err := loadOrCreateAdminCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "" || hash != nil || created {
		t.Fatalf("fresh install should wait for setup, got plain=%q hashPresent=%v created=%v", plain, len(hash) > 0, created)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("fresh install must not create an implicit admin password")
	}
}

func TestSetupAdminCreatesLocalAccountAndOnlyOnce(t *testing.T) {
	dataDir := t.TempDir()
	a := &App{cfg: Config{DataDir: dataDir}, sessions: map[string]sessionRecord{}}
	payload := `{"username":"owner.name","password":"a-very-strong-test-password","confirmPassword":"a-very-strong-test-password"}`

	request := httptest.NewRequest(http.MethodPost, "/api/auth/setup", strings.NewReader(payload))
	request.RemoteAddr = "127.0.0.1:4567"
	response := httptest.NewRecorder()
	a.setupAdmin(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("setup status = %d, body=%s", response.Code, response.Body.String())
	}
	if a.adminUsername != "owner.name" {
		t.Fatalf("admin username = %q", a.adminUsername)
	}
	if len(response.Result().Cookies()) == 0 {
		t.Fatal("successful first-run setup should issue a session cookie")
	}
	stored, err := os.ReadFile(filepath.Join(dataDir, "admin_password.hash"))
	if err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword(stored, []byte("a-very-strong-test-password")); err != nil {
		t.Fatal("password hash was not persisted")
	}
	storedUsername, err := os.ReadFile(filepath.Join(dataDir, "admin_username"))
	if err != nil || string(storedUsername) != "owner.name" {
		t.Fatalf("username was not persisted: %q, %v", storedUsername, err)
	}

	second := httptest.NewRequest(http.MethodPost, "/api/auth/setup", strings.NewReader(payload))
	second.RemoteAddr = "127.0.0.1:4567"
	secondResponse := httptest.NewRecorder()
	a.setupAdmin(secondResponse, second)
	if secondResponse.Code != http.StatusConflict {
		t.Fatalf("second setup status = %d, want %d", secondResponse.Code, http.StatusConflict)
	}
}

func TestSetupAdminRejectsNonLoopbackRequest(t *testing.T) {
	dataDir := t.TempDir()
	a := &App{cfg: Config{DataDir: dataDir}}
	payload := `{"username":"owner","password":"a-very-strong-test-password","confirmPassword":"a-very-strong-test-password"}`
	request := httptest.NewRequest(http.MethodPost, "/api/auth/setup", strings.NewReader(payload))
	request.RemoteAddr = "203.0.113.10:4567"
	response := httptest.NewRecorder()
	a.setupAdmin(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("remote setup status = %d, want %d", response.Code, http.StatusForbidden)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "admin_password.hash")); !os.IsNotExist(err) {
		t.Fatal("remote setup attempt must not create an admin hash")
	}
}

func TestAdminUsernameAndPasswordValidation(t *testing.T) {
	for _, username := range []string{"ab", "bad name", "_leading", "-leading", "has/slash", "ééé"} {
		if validAdminUsername(username) {
			t.Errorf("username %q should be rejected", username)
		}
	}
	for _, username := range []string{"admin", "owner.name", "user_01", "node-admin"} {
		if !validAdminUsername(username) {
			t.Errorf("username %q should be accepted", username)
		}
	}
	if err := validateAdminPassword("short"); err == nil {
		t.Fatal("short password should be rejected")
	}
	if err := validateAdminPassword(strings.Repeat("x", 73)); err == nil {
		t.Fatal("bcrypt-overlong password should be rejected")
	}
	if err := validateAdminPassword("long-enough-password"); err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
}


func TestPanelUserLoginCreatesScopedSession(t *testing.T) {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("strong-test-user-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{
		adminUsername: "owner",
		adminHash:     []byte("unused-owner-hash"),
		users: map[string]PanelUser{
			"user-1": {ID: "user-1", Username: "member", PasswordHash: passwordHash, ServerIDs: []string{"server-a"}},
		},
		sessions: map[string]sessionRecord{},
		servers: map[string]Server{
			"server-a": {ID: "server-a", Name: "Assigned"},
			"server-b": {ID: "server-b", Name: "Private"},
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"member","password":"strong-test-user-password"}`))
	request.RemoteAddr = "198.51.100.20:1234"
	response := httptest.NewRecorder()
	a.login(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login status = %d, body=%s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("successful login did not set a session cookie")
	}
	session := a.sessions[cookies[0].Value]
	if session.Role != "user" || session.UserID != "user-1" {
		t.Fatalf("unexpected user session: %+v", session)
	}
	if !a.canAccessServer(session, "server-a") {
		t.Fatal("user should access their assigned server")
	}
	if a.canAccessServer(session, "server-b") {
		t.Fatal("user must not access an unassigned server")
	}
}

func TestCreateUserPersistsOnlySelectedServerAccess(t *testing.T) {
	dataDir := t.TempDir()
	a := &App{
		cfg: Config{DataDir: dataDir},
		adminUsername: "owner",
		users: map[string]PanelUser{},
		servers: map[string]Server{
			"server-a": {ID: "server-a", Name: "Assigned"},
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader(`{"username":"member1","password":"strong-test-user-password","serverIds":["server-a"]}`))
	response := httptest.NewRecorder()
	a.createUser(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create user status = %d, body=%s", response.Code, response.Body.String())
	}
	var stored []PanelUser
	if err := readJSON(filepath.Join(dataDir, "users.json"), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Username != "member1" || len(stored[0].PasswordHash) == 0 {
		t.Fatalf("unexpected persisted user record: %+v", stored)
	}
	if len(stored[0].ServerIDs) != 1 || stored[0].ServerIDs[0] != "server-a" {
		t.Fatalf("unexpected assigned servers: %+v", stored[0].ServerIDs)
	}
	if err := bcrypt.CompareHashAndPassword(stored[0].PasswordHash, []byte("strong-test-user-password")); err != nil {
		t.Fatal("stored user password is not a bcrypt hash of the supplied password")
	}
}

func TestCreateUserRejectsUnknownServerAssignment(t *testing.T) {
	a := &App{
		cfg: Config{DataDir: t.TempDir()},
		adminUsername: "owner",
		users: map[string]PanelUser{},
		servers: map[string]Server{},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader(`{"username":"member2","password":"strong-test-user-password","serverIds":["missing-server"]}`))
	response := httptest.NewRecorder()
	a.createUser(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("create user status = %d, want 400, body=%s", response.Code, response.Body.String())
	}
	if len(a.users) != 0 {
		t.Fatal("invalid user assignment must not create an account")
	}
}
