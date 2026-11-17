package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
)

const (
	sessionCookieName = "nova_session"
	sessionLifetime   = 12 * time.Hour
	maxJSONBody       = 1 << 20
	metricsCommand    = "printf 'hostname=%s\\n' \"$(hostname 2>/dev/null)\"; printf 'os=%s\\n' \"$(uname -sr 2>/dev/null)\"; printf 'cpu_cores=%s\\n' \"$(nproc 2>/dev/null)\"; printf 'mem_total=%s\\n' \"$(awk '/MemTotal/ {print $2*1024}' /proc/meminfo)\"; printf 'mem_available=%s\\n' \"$(awk '/MemAvailable/ {print $2*1024}' /proc/meminfo)\"; printf 'disk_total=%s\\n' \"$(df -B1 --output=size / 2>/dev/null | tail -n1 | tr -d ' ')\"; printf 'disk_used=%s\\n' \"$(df -B1 --output=used / 2>/dev/null | tail -n1 | tr -d ' ')\"; printf 'uptime=%s\\n' \"$(cut -d ' ' -f1 /proc/uptime)\""
)

type Config struct {
	Addr         string
	DataDir      string
	CookieSecure bool
}

type Server struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	Username    string    `json:"username"`
	AuthMethod  string    `json:"authMethod"`
	Fingerprint string    `json:"fingerprint"`
	AddedAt     time.Time `json:"addedAt"`
	Encrypted   string    `json:"-"`
}

type publicServer struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	Username    string    `json:"username"`
	AuthMethod  string    `json:"authMethod"`
	Fingerprint string    `json:"fingerprint"`
	AddedAt     time.Time `json:"addedAt"`
}

type credentials struct {
	Password      string `json:"password,omitempty"`
	PrivateKey    string `json:"privateKey,omitempty"`
	KeyPassphrase string `json:"keyPassphrase,omitempty"`
}

type connectRequest struct {
	Name            string `json:"name"`
	Host            string `json:"host"`
	Port            int    `json:"port"`
	Username        string `json:"username"`
	AuthMethod      string `json:"authMethod"`
	Password        string `json:"password"`
	PrivateKey      string `json:"privateKey"`
	KeyPassphrase   string `json:"keyPassphrase"`
	ExpectedHostKey string `json:"expectedHostKey"`
}

type IPPool struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CIDR      string    `json:"cidr"`
	Gateway   string    `json:"gateway,omitempty"`
	Family    string    `json:"family"`
	CreatedAt time.Time `json:"createdAt"`
}

type IPAllocation struct {
	ID         string    `json:"id"`
	PoolID     string    `json:"poolId"`
	Address    string    `json:"address"`
	TargetType string    `json:"targetType"`
	TargetID   string    `json:"targetId"`
	CreatedAt  time.Time `json:"createdAt"`
}

type App struct {
	cfg           Config
	key           []byte
	mu            sync.RWMutex
	servers       map[string]Server
	ipPools       map[string]IPPool
	ipAllocations map[string]IPAllocation
	sessions      map[string]sessionRecord
	users         map[string]PanelUser
	loginMu       sync.Mutex
	loginHits     map[string][]time.Time
	adminHash     []byte
	adminUsername string
	setupToken    string
	setupMu       sync.Mutex
}

func main() {
	dataDir := env("NOVAVPS_DATA_DIR", "./data")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		log.Fatal(err)
	}

	key, err := loadOrCreateSecret(filepath.Join(dataDir, "encryption.key"), 32)
	if err != nil {
		log.Fatal(err)
	}
	adminPassword, adminHash, createdAdminPassword, err := loadOrCreateAdminCredentials(filepath.Join(dataDir, "admin_password.hash"))
	if err != nil {
		log.Fatal(err)
	}
	adminUsername := ""
	if len(adminHash) > 0 {
		adminUsername, err = loadAdminUsername(filepath.Join(dataDir, "admin_username"))
		if err != nil {
			log.Fatal(err)
		}
	}

	addr := os.Getenv("NOVAVPS_ADDR")
	if addr == "" {
		if port := os.Getenv("PORT"); port != "" {
			addr = "0.0.0.0:" + port
		} else {
			addr = ":8080"
		}
	}
	app := &App{
		cfg: Config{
			Addr:         addr,
			DataDir:      dataDir,
			CookieSecure: strings.EqualFold(os.Getenv("NOVAVPS_COOKIE_SECURE"), "true"),
		},
		key:           key,
		servers:       map[string]Server{},
		ipPools:       map[string]IPPool{},
		ipAllocations: map[string]IPAllocation{},
		sessions:      map[string]sessionRecord{},
		users:         map[string]PanelUser{},
		loginHits:     map[string][]time.Time{},
		adminHash:     adminHash,
		adminUsername: adminUsername,
		setupToken:    os.Getenv("NOVAVPS_SETUP_TOKEN"),
	}
	if err := app.loadServers(); err != nil {
		log.Fatal(err)
	}
	if err := app.loadPanelUsers(); err != nil {
		log.Fatal(err)
	}
	if err := app.loadNetworkStore(); err != nil {
		log.Fatal(err)
	}

	if createdAdminPassword {
		log.Printf("FIRST START ADMIN PASSWORD (save it now): %s", adminPassword)
	}
	log.Printf("NovaVPS API listening on %s", app.cfg.Addr)
	server := &http.Server{
		Addr:              app.cfg.Addr,
		Handler:           app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, http.StatusOK, map[string]any{"ok": true, "service": "novavps-api", "time": time.Now().UTC()})
	})
	mux.HandleFunc("GET /api/auth/setup-status", a.setupStatus)
	mux.HandleFunc("POST /api/auth/setup", a.setupAdmin)
	mux.HandleFunc("POST /api/auth/login", a.login)
	mux.HandleFunc("POST /api/auth/logout", a.logout)
	mux.Handle("GET /api/auth/me", a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session := sessionForRequest(r)
		jsonOut(w, http.StatusOK, map[string]any{"authenticated": true, "username": session.Username, "role": session.Role})
	})))
	mux.Handle("GET /api/users", a.ownerAuth(http.HandlerFunc(a.listUsers)))
	mux.Handle("POST /api/users", a.ownerAuth(http.HandlerFunc(a.createUser)))
	mux.Handle("PATCH /api/users/{id}", a.ownerAuth(http.HandlerFunc(a.updateUser)))
	mux.Handle("DELETE /api/users/{id}", a.ownerAuth(http.HandlerFunc(a.deleteUser)))
	mux.Handle("GET /api/servers", a.auth(http.HandlerFunc(a.listServers)))
	mux.Handle("POST /api/servers/connect", a.ownerAuth(http.HandlerFunc(a.connectServer)))
	mux.Handle("DELETE /api/servers/", a.ownerAuth(http.HandlerFunc(a.deleteServer)))
	mux.Handle("GET /api/servers/", a.auth(http.HandlerFunc(a.serverAction)))
	mux.Handle("POST /api/servers/{id}/command", a.auth(http.HandlerFunc(a.runSingleLineCommand)))
	mux.Handle("GET /api/network/overview", a.ownerAuth(http.HandlerFunc(a.networkOverview)))
	mux.Handle("GET /api/network/pools", a.ownerAuth(http.HandlerFunc(a.listIPPools)))
	mux.Handle("POST /api/network/pools", a.ownerAuth(http.HandlerFunc(a.createIPPool)))
	mux.Handle("DELETE /api/network/pools/", a.ownerAuth(http.HandlerFunc(a.deleteIPPool)))
	mux.Handle("GET /api/network/allocations", a.ownerAuth(http.HandlerFunc(a.listIPAllocations)))
	mux.Handle("POST /api/network/allocations", a.ownerAuth(http.HandlerFunc(a.createIPAllocation)))
	mux.Handle("DELETE /api/network/allocations/", a.ownerAuth(http.HandlerFunc(a.releaseIPAllocation)))

	webDir := env("NOVAVPS_WEB_DIR", "./web/dist")
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		clean := filepath.Clean("/" + strings.TrimSpace(r.URL.Path))
		relative := strings.TrimLeft(clean, string(filepath.Separator))
		candidate := filepath.Join(webDir, relative)
		if relative == "" || relative == "." {
			candidate = filepath.Join(webDir, "index.html")
		}
		base, _ := filepath.Abs(webDir)
		resolved, _ := filepath.Abs(candidate)
		if rel, err := filepath.Rel(base, resolved); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			http.NotFound(w, r)
			return
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			http.ServeFile(w, r, candidate)
			return
		}
		index := filepath.Join(webDir, "index.html")
		if _, err := os.Stat(index); err == nil {
			http.ServeFile(w, r, index)
			return
		}
		http.Error(w, "NovaVPS web assets are not built. Run npm run build or use Docker Compose.", http.StatusServiceUnavailable)
	}))
	return a.securityHeaders(mux)
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self' https: wss:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookieName)
		if err != nil {
			jsonOut(w, http.StatusUnauthorized, map[string]string{"error": "authentication_required"})
			return
		}
		a.mu.RLock()
		session, ok := a.sessions[c.Value]
		valid := ok && time.Now().Before(session.ExpiresAt)
		if valid && session.Role == "owner" {
			valid = len(a.adminHash) > 0 && strings.EqualFold(session.Username, a.adminUsername)
		} else if valid && session.Role == "user" {
			user, exists := a.users[session.UserID]
			valid = exists && strings.EqualFold(user.Username, session.Username)
		} else if valid {
			valid = false
		}
		a.mu.RUnlock()
		if !valid {
			a.mu.Lock()
			delete(a.sessions, c.Value)
			a.mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
			jsonOut(w, http.StatusUnauthorized, map[string]string{"error": "session_expired"})
			return
		}
		next.ServeHTTP(w, requestWithSession(r, session))
	})
}

func (a *App) setupStatus(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	required := len(a.adminHash) == 0
	username := a.adminUsername
	a.mu.RUnlock()
	jsonOut(w, http.StatusOK, map[string]any{"setupRequired": required, "username": username, "setupTokenRequired": a.setupToken != ""})
}

// setupAdmin requires a one-time installer token when configured. Local
// development without an installer token remains restricted to loopback.
func (a *App) setupAdmin(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		jsonOut(w, http.StatusForbidden, map[string]string{"error": "cross_origin_request"})
		return
	}
	var body struct {
		Username        string `json:"username"`
		Password        string `json:"password"`
		ConfirmPassword string `json:"confirmPassword"`
		SetupToken      string `json:"setupToken"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if a.setupToken != "" {
		if subtle.ConstantTimeCompare([]byte(body.SetupToken), []byte(a.setupToken)) != 1 {
			jsonOut(w, http.StatusForbidden, map[string]string{"error": "invalid_setup_token", "message": "Retrieve the one-time setup token on the NovaVPS host with: sudo novavps setup-token"})
			return
		}
	} else if !isLoopbackRequest(r) {
		jsonOut(w, http.StatusForbidden, map[string]string{"error": "setup_local_only", "message": "Create the first account from localhost or configure the one-time installer setup token."})
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if !validAdminUsername(body.Username) {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "invalid_username", "message": "Username must be 3–32 characters: letters, numbers, dot, dash or underscore."})
		return
	}
	if err := validateAdminPassword(body.Password); err != nil {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if body.Password != body.ConfirmPassword {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "passwords_do_not_match"})
		return
	}

	a.setupMu.Lock()
	defer a.setupMu.Unlock()
	a.mu.RLock()
	alreadyConfigured := len(a.adminHash) > 0
	a.mu.RUnlock()
	if alreadyConfigured {
		jsonOut(w, http.StatusConflict, map[string]string{"error": "admin_already_configured"})
		return
	}
	hashPath := filepath.Join(a.cfg.DataDir, "admin_password.hash")
	if _, err := os.Stat(hashPath); err == nil {
		jsonOut(w, http.StatusConflict, map[string]string{"error": "admin_already_configured"})
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "admin_storage_unavailable"})
		return
	}
	if os.Getenv("NOVAVPS_ADMIN_PASSWORD") != "" {
		jsonOut(w, http.StatusConflict, map[string]string{"error": "admin_already_configured"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "password_hash_failed"})
		return
	}
	usernamePath := filepath.Join(a.cfg.DataDir, "admin_username")
	if err := os.WriteFile(usernamePath, []byte(body.Username), 0600); err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "admin_storage_unavailable"})
		return
	}
	if err := writeAdminPasswordHash(hashPath, hash); err != nil {
		_ = os.Remove(usernamePath)
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "admin_storage_unavailable"})
		return
	}
	token, err := randomToken(32)
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "session_creation_failed"})
		return
	}
	expiry := time.Now().Add(sessionLifetime)
	a.mu.Lock()
	a.adminUsername = body.Username
	a.adminHash = hash
	if a.sessions == nil {
		a.sessions = map[string]sessionRecord{}
	}
	a.sessions[token] = sessionRecord{Username: body.Username, Role: "owner", ExpiresAt: expiry}
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/", Expires: expiry,
		MaxAge: int(sessionLifetime.Seconds()), HttpOnly: true, Secure: a.cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
	jsonOut(w, http.StatusCreated, map[string]any{"ok": true, "username": body.Username})
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func validAdminUsername(username string) bool {
	if len(username) < 3 || len(username) > 32 {
		return false
	}
	for i, r := range username {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.'
		if !valid || i == 0 && (r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

func validateAdminPassword(password string) error {
	if len([]rune(password)) < 12 {
		return errors.New("password_too_short")
	}
	if len(password) > 72 {
		return errors.New("password_too_long")
	}
	return nil
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		jsonOut(w, http.StatusForbidden, map[string]string{"error": "cross_origin_request"})
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if body.Username == "" || a.rateLimited(r) {
		a.loginFailed(w, r)
		return
	}
	a.mu.RLock()
	adminUsername := a.adminUsername
	adminHash := append([]byte(nil), a.adminHash...)
	users := make([]PanelUser, 0, len(a.users))
	for _, user := range a.users {
		user.PasswordHash = append([]byte(nil), user.PasswordHash...)
		users = append(users, user)
	}
	a.mu.RUnlock()

	session := sessionRecord{}
	if adminUsername != "" && strings.EqualFold(body.Username, adminUsername) && len(adminHash) > 0 &&
		bcrypt.CompareHashAndPassword(adminHash, []byte(body.Password)) == nil {
		session = sessionRecord{Username: adminUsername, Role: "owner"}
	} else {
		for _, user := range users {
			if strings.EqualFold(body.Username, user.Username) && bcrypt.CompareHashAndPassword(user.PasswordHash, []byte(body.Password)) == nil {
				session = sessionRecord{Username: user.Username, Role: "user", UserID: user.ID}
				break
			}
		}
	}
	if session.Role == "" {
		a.loginFailed(w, r)
		return
	}
	token, err := randomToken(32)
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "session_creation_failed"})
		return
	}
	expiry := time.Now().Add(sessionLifetime)
	session.ExpiresAt = expiry
	a.mu.Lock()
	if a.sessions == nil {
		a.sessions = map[string]sessionRecord{}
	}
	a.sessions[token] = session
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/", Expires: expiry,
		MaxAge: int(sessionLifetime.Seconds()), HttpOnly: true, Secure: a.cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
	jsonOut(w, http.StatusOK, map[string]any{"ok": true, "username": session.Username, "role": session.Role})
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		jsonOut(w, http.StatusForbidden, map[string]string{"error": "cross_origin_request"})
		return
	}
	if c, err := r.Cookie(sessionCookieName); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	jsonOut(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) rateLimited(r *http.Request) bool {
	key := clientIP(r)
	now := time.Now()
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	if a.loginHits == nil {
		a.loginHits = map[string][]time.Time{}
	}
	hits := a.loginHits[key]
	kept := hits[:0]
	for _, hit := range hits {
		if now.Sub(hit) < 10*time.Minute {
			kept = append(kept, hit)
		}
	}
	a.loginHits[key] = kept
	return len(kept) >= 8
}

func (a *App) loginFailed(w http.ResponseWriter, r *http.Request) {
	key := clientIP(r)
	a.loginMu.Lock()
	if a.loginHits == nil {
		a.loginHits = map[string][]time.Time{}
	}
	a.loginHits[key] = append(a.loginHits[key], time.Now())
	a.loginMu.Unlock()
	jsonOut(w, http.StatusUnauthorized, map[string]string{"error": "invalid_credentials"})
}

func (a *App) listServers(w http.ResponseWriter, r *http.Request) {
	session := sessionForRequest(r)
	a.mu.RLock()
	out := make([]publicServer, 0, len(a.servers))
	for _, s := range a.servers {
		if session.Role == "user" {
			user, ok := a.users[session.UserID]
			if !ok || !containsString(user.ServerIDs, s.ID) {
				continue
			}
		}
		out = append(out, publicServer{ID: s.ID, Name: s.Name, Host: s.Host, Port: s.Port, Username: s.Username, AuthMethod: s.AuthMethod, Fingerprint: s.Fingerprint, AddedAt: s.AddedAt})
	}
	a.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].AddedAt.After(out[j].AddedAt) })
	jsonOut(w, http.StatusOK, map[string]any{"servers": out})
}

func (a *App) connectServer(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		jsonOut(w, http.StatusForbidden, map[string]string{"error": "cross_origin_request"})
		return
	}
	var req connectRequest
	if err := decodeJSON(w, r, &req); err != nil {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Host = strings.Trim(strings.TrimSpace(req.Host), "[]")
	req.Username = strings.TrimSpace(req.Username)
	if req.Port == 0 {
		req.Port = 22
	}
	if req.Name == "" || len(req.Name) > 80 || req.Username == "" || len(req.Username) > 64 || req.Port < 1 || req.Port > 65535 {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "invalid_server_details"})
		return
	}
	if err := validateHost(req.Host); err != nil {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	secret := credentials{}
	switch req.AuthMethod {
	case "password":
		secret.Password = req.Password
		if secret.Password == "" || len(secret.Password) > 4096 {
			jsonOut(w, http.StatusBadRequest, map[string]string{"error": "password_required"})
			return
		}
	case "privateKey":
		secret.PrivateKey = req.PrivateKey
		secret.KeyPassphrase = req.KeyPassphrase
		if strings.TrimSpace(secret.PrivateKey) == "" || len(secret.PrivateKey) > 64*1024 || len(secret.KeyPassphrase) > 4096 {
			jsonOut(w, http.StatusBadRequest, map[string]string{"error": "private_key_required"})
			return
		}
	default:
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "unsupported_auth_method"})
		return
	}

	fingerprint, err := probeHostFingerprint(req.Host, req.Port, req.Username)
	if err != nil || fingerprint == "" {
		jsonOut(w, http.StatusBadGateway, map[string]string{"error": "ssh_host_unreachable", "message": "Could not complete the SSH handshake. Check the host, port, firewall and SSH service."})
		return
	}
	expected := strings.TrimSpace(req.ExpectedHostKey)
	if expected == "" {
		jsonOut(w, 428, map[string]any{"error": "host_key_confirmation_required", "fingerprint": fingerprint, "message": "Verify this fingerprint from your provider console or another trusted channel before continuing."})
		return
	}
	if expected != fingerprint {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "host_key_fingerprint_mismatch", "message": "The supplied fingerprint does not match the server key."})
		return
	}
	client, err := openSSH(req.Host, req.Port, req.Username, secret, fingerprint)
	if err != nil {
		jsonOut(w, http.StatusBadGateway, map[string]string{"error": "ssh_authentication_failed", "message": "SSH authentication or host-key verification failed."})
		return
	}
	client.Close()

	plain, err := json.Marshal(secret)
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "credential_encoding_failed"})
		return
	}
	encrypted, err := a.encrypt(plain)
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "credential_encryption_failed"})
		return
	}
	id, err := randomToken(12)
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "id_generation_failed"})
		return
	}
	server := Server{ID: id, Name: req.Name, Host: req.Host, Port: req.Port, Username: req.Username, AuthMethod: req.AuthMethod, Fingerprint: fingerprint, AddedAt: time.Now().UTC(), Encrypted: encrypted}
	a.mu.Lock()
	a.servers[id] = server
	err = a.saveServersLocked()
	a.mu.Unlock()
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "server_persistence_failed"})
		return
	}
	jsonOut(w, http.StatusCreated, map[string]any{"ok": true, "server": publicServer{ID: id, Name: server.Name, Host: server.Host, Port: server.Port, Username: server.Username, AuthMethod: server.AuthMethod, Fingerprint: server.Fingerprint, AddedAt: server.AddedAt}})
}

func (a *App) deleteServer(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/servers/")
	if id == "" || strings.Contains(id, "/") {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "invalid_server_id"})
		return
	}
	a.mu.Lock()
	if _, ok := a.servers[id]; !ok {
		a.mu.Unlock()
		jsonOut(w, http.StatusNotFound, map[string]string{"error": "server_not_found"})
		return
	}
	delete(a.servers, id)
	err := a.saveServersLocked()
	a.mu.Unlock()
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "server_persistence_failed"})
		return
	}
	jsonOut(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) serverAction(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/api/servers/")
	parts := strings.Split(tail, "/")
	if len(parts) != 2 || parts[1] != "metrics" || parts[0] == "" {
		jsonOut(w, http.StatusNotFound, map[string]string{"error": "endpoint_not_found"})
		return
	}
	if !a.canAccessServer(sessionForRequest(r), parts[0]) {
		jsonOut(w, http.StatusForbidden, map[string]string{"error": "server_access_denied"})
		return
	}
	a.mu.RLock()
	server, ok := a.servers[parts[0]]
	a.mu.RUnlock()
	if !ok {
		jsonOut(w, http.StatusNotFound, map[string]string{"error": "server_not_found"})
		return
	}
	plain, err := a.decrypt(server.Encrypted)
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "credential_decryption_failed"})
		return
	}
	var secret credentials
	if json.Unmarshal(plain, &secret) != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "credential_record_invalid"})
		return
	}
	client, err := openSSH(server.Host, server.Port, server.Username, secret, server.Fingerprint)
	if err != nil {
		jsonOut(w, http.StatusBadGateway, map[string]string{"error": "ssh_connection_failed", "message": "Could not reconnect to this server."})
		return
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		jsonOut(w, http.StatusBadGateway, map[string]string{"error": "ssh_session_failed"})
		return
	}
	defer session.Close()
	output, err := session.CombinedOutput(metricsCommand)
	if err != nil {
		jsonOut(w, http.StatusBadGateway, map[string]string{"error": "metrics_command_failed", "message": "The SSH account could not read the requested system metrics."})
		return
	}
	metrics := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			metrics[key] = strings.TrimSpace(value)
		}
	}
	jsonOut(w, http.StatusOK, map[string]any{"serverId": server.ID, "metrics": metrics, "checkedAt": time.Now().UTC()})
}

type boundedOutput struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			_, _ = b.buffer.Write(data[:remaining])
			b.truncated = true
		} else {
			_, _ = b.buffer.Write(data)
		}
	} else if len(data) > 0 {
		b.truncated = true
	}
	// Report the complete write so the SSH stream is drained even after the
	// display buffer reaches its cap.
	return len(data), nil
}

func (b *boundedOutput) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String(), b.truncated
}

func validateSingleLineCommand(command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("command_required")
	}
	if len(command) > 2048 {
		return "", errors.New("command_too_long")
	}
	if strings.ContainsAny(command, "\r\n\x00") {
		return "", errors.New("single_line_command_required")
	}
	return command, nil
}

func (a *App) runSingleLineCommand(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		jsonOut(w, http.StatusForbidden, map[string]string{"error": "cross_origin_request"})
		return
	}
	var body struct {
		Command string `json:"command"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	command, err := validateSingleLineCommand(body.Command)
	if err != nil {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	serverID := r.PathValue("id")
	if !a.canAccessServer(sessionForRequest(r), serverID) {
		jsonOut(w, http.StatusForbidden, map[string]string{"error": "server_access_denied"})
		return
	}
	a.mu.RLock()
	server, ok := a.servers[serverID]
	a.mu.RUnlock()
	if !ok {
		jsonOut(w, http.StatusNotFound, map[string]string{"error": "server_not_found"})
		return
	}

	plain, err := a.decrypt(server.Encrypted)
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "credential_decryption_failed"})
		return
	}
	var secret credentials
	if err := json.Unmarshal(plain, &secret); err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "credential_record_invalid"})
		return
	}
	client, err := openSSH(server.Host, server.Port, server.Username, secret, server.Fingerprint)
	if err != nil {
		jsonOut(w, http.StatusBadGateway, map[string]string{"error": "ssh_connection_failed", "message": "Could not reconnect to this server."})
		return
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		jsonOut(w, http.StatusBadGateway, map[string]string{"error": "ssh_session_failed"})
		return
	}
	defer session.Close()

	output := &boundedOutput{limit: 48 * 1024}
	session.Stdout = output
	session.Stderr = output
	started := time.Now()
	if err := session.Start(command); err != nil {
		jsonOut(w, http.StatusBadGateway, map[string]string{"error": "command_start_failed", "message": "The SSH server could not start this command."})
		return
	}
	finished := make(chan error, 1)
	go func() { finished <- session.Wait() }()

	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	select {
	case waitErr := <-finished:
		elapsed := time.Since(started).Milliseconds()
		text, truncated := output.snapshot()
		exitCode := 0
		ok := waitErr == nil
		if waitErr != nil {
			var exitErr *ssh.ExitError
			if errors.As(waitErr, &exitErr) {
				exitCode = exitErr.ExitStatus()
			} else {
				exitCode = -1
			}
		}
		jsonOut(w, http.StatusOK, map[string]any{
			"ok": ok, "command": command, "output": text, "exitCode": exitCode,
			"truncated": truncated, "timedOut": false, "durationMs": elapsed,
		})
	case <-timer.C:
		_ = session.Close()
		_ = client.Close()
		text, truncated := output.snapshot()
		jsonOut(w, http.StatusOK, map[string]any{
			"ok": false, "command": command, "output": text, "exitCode": -1,
			"truncated": truncated, "timedOut": true, "durationMs": time.Since(started).Milliseconds(),
		})
	}
}

func (a *App) networkOverview(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	pools := len(a.ipPools)
	allocations := len(a.ipAllocations)
	v4, v6 := 0, 0
	for _, item := range a.ipAllocations {
		if parsed, err := netip.ParseAddr(item.Address); err == nil {
			if parsed.Is4() {
				v4++
			} else if parsed.Is6() {
				v6++
			}
		}
	}
	a.mu.RUnlock()
	jsonOut(w, http.StatusOK, map[string]any{
		"pools": pools, "allocations": allocations, "ipv4Allocations": v4, "ipv6Allocations": v6,
		"capability": "inventory-only",
		"notice": "IP records do not configure public routing. Real assignment requires provider-authorized address space and a privileged host/network agent.",
	})
}

func (a *App) listIPPools(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	out := make([]IPPool, 0, len(a.ipPools))
	for _, pool := range a.ipPools {
		out = append(out, pool)
	}
	a.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	jsonOut(w, http.StatusOK, map[string]any{"pools": out})
}

func (a *App) createIPPool(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		jsonOut(w, http.StatusForbidden, map[string]string{"error": "cross_origin_request"})
		return
	}
	var body struct {
		Name        string `json:"name"`
		CIDR        string `json:"cidr"`
		Gateway     string `json:"gateway"`
		Authorized  bool   `json:"authorized"`
	}
	if decodeJSON(w, r, &body) != nil {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if !body.Authorized {
		jsonOut(w, http.StatusForbidden, map[string]string{"error": "ownership_confirmation_required", "message": "Confirm that this prefix has been assigned to you and you are authorized to manage it."})
		return
	}
	prefix, err := netip.ParsePrefix(strings.TrimSpace(body.CIDR))
	if err != nil {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "invalid_cidr"})
		return
	}
	prefix = prefix.Masked()
	if body.Name == "" || len(body.Name) > 80 || !publicPrefix(prefix) {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "invalid_or_non_public_prefix"})
		return
	}
	if body.Gateway != "" {
		gateway, parseErr := netip.ParseAddr(strings.TrimSpace(body.Gateway))
		if parseErr != nil || !prefix.Contains(gateway) {
			jsonOut(w, http.StatusBadRequest, map[string]string{"error": "gateway_outside_prefix"})
			return
		}
	}
	id, err := randomToken(12)
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "id_generation_failed"})
		return
	}
	family := "IPv6"
	if prefix.Addr().Is4() {
		family = "IPv4"
	}
	pool := IPPool{ID: id, Name: body.Name, CIDR: prefix.String(), Gateway: strings.TrimSpace(body.Gateway), Family: family, CreatedAt: time.Now().UTC()}
	a.mu.Lock()
	for _, existing := range a.ipPools {
		other, _ := netip.ParsePrefix(existing.CIDR)
		if prefixesOverlap(prefix, other) {
			a.mu.Unlock()
			jsonOut(w, http.StatusConflict, map[string]string{"error": "overlapping_pool"})
			return
		}
	}
	a.ipPools[id] = pool
	err = a.saveNetworkLocked()
	a.mu.Unlock()
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "network_persistence_failed"})
		return
	}
	jsonOut(w, http.StatusCreated, map[string]any{"ok": true, "pool": pool})
}

func (a *App) deleteIPPool(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/network/pools/")
	a.mu.Lock()
	if _, ok := a.ipPools[id]; !ok {
		a.mu.Unlock()
		jsonOut(w, http.StatusNotFound, map[string]string{"error": "pool_not_found"})
		return
	}
	for _, allocation := range a.ipAllocations {
		if allocation.PoolID == id {
			a.mu.Unlock()
			jsonOut(w, http.StatusConflict, map[string]string{"error": "pool_has_allocations"})
			return
		}
	}
	delete(a.ipPools, id)
	err := a.saveNetworkLocked()
	a.mu.Unlock()
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "network_persistence_failed"})
		return
	}
	jsonOut(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) listIPAllocations(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	out := make([]IPAllocation, 0, len(a.ipAllocations))
	for _, allocation := range a.ipAllocations {
		out = append(out, allocation)
	}
	a.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	jsonOut(w, http.StatusOK, map[string]any{"allocations": out})
}

func (a *App) createIPAllocation(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		jsonOut(w, http.StatusForbidden, map[string]string{"error": "cross_origin_request"})
		return
	}
	var body struct {
		PoolID     string `json:"poolId"`
		TargetType string `json:"targetType"`
		TargetID   string `json:"targetId"`
	}
	if decodeJSON(w, r, &body) != nil {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	body.TargetType = strings.ToLower(strings.TrimSpace(body.TargetType))
	body.TargetID = strings.TrimSpace(body.TargetID)
	if body.TargetID == "" || len(body.TargetID) > 100 || !oneOf(body.TargetType, "server", "vm", "container", "other") {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "invalid_allocation_target"})
		return
	}
	a.mu.Lock()
	pool, ok := a.ipPools[body.PoolID]
	if !ok {
		a.mu.Unlock()
		jsonOut(w, http.StatusNotFound, map[string]string{"error": "pool_not_found"})
		return
	}
	prefix, _ := netip.ParsePrefix(pool.CIDR)
	used := map[netip.Addr]bool{}
	for _, allocation := range a.ipAllocations {
		addr, err := netip.ParseAddr(allocation.Address)
		if err == nil {
			used[addr] = true
		}
	}
	var gateway netip.Addr
	if pool.Gateway != "" {
		gateway, _ = netip.ParseAddr(pool.Gateway)
	}
	candidate := prefix.Addr()
	if prefix.Bits() < candidate.BitLen() {
		candidate = candidate.Next()
	}
	var selected netip.Addr
	for attempts := 0; attempts < 10000 && prefix.Contains(candidate); attempts++ {
		if candidate != gateway && !used[candidate] && !isIPv4Broadcast(candidate, prefix) {
			selected = candidate
			break
		}
		candidate = candidate.Next()
	}
	if !selected.IsValid() {
		a.mu.Unlock()
		jsonOut(w, http.StatusConflict, map[string]string{"error": "pool_exhausted_or_prefix_too_large"})
		return
	}
	id, err := randomToken(12)
	if err != nil {
		a.mu.Unlock()
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "id_generation_failed"})
		return
	}
	allocation := IPAllocation{ID: id, PoolID: pool.ID, Address: selected.String(), TargetType: body.TargetType, TargetID: body.TargetID, CreatedAt: time.Now().UTC()}
	a.ipAllocations[id] = allocation
	err = a.saveNetworkLocked()
	a.mu.Unlock()
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "network_persistence_failed"})
		return
	}
	jsonOut(w, http.StatusCreated, map[string]any{"ok": true, "allocation": allocation, "capability": "reserved-not-configured"})
}

func (a *App) releaseIPAllocation(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/network/allocations/")
	a.mu.Lock()
	if _, ok := a.ipAllocations[id]; !ok {
		a.mu.Unlock()
		jsonOut(w, http.StatusNotFound, map[string]string{"error": "allocation_not_found"})
		return
	}
	delete(a.ipAllocations, id)
	err := a.saveNetworkLocked()
	a.mu.Unlock()
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "network_persistence_failed"})
		return
	}
	jsonOut(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) loadServers() error {
	var items []Server
	if err := readJSON(filepath.Join(a.cfg.DataDir, "servers.json"), &items); err != nil {
		return err
	}
	for _, item := range items {
		a.servers[item.ID] = item
	}
	return nil
}

func (a *App) loadNetworkStore() error {
	var state struct {
		Pools       []IPPool       `json:"pools"`
		Allocations []IPAllocation `json:"allocations"`
	}
	if err := readJSON(filepath.Join(a.cfg.DataDir, "network.json"), &state); err != nil {
		return err
	}
	for _, item := range state.Pools {
		a.ipPools[item.ID] = item
	}
	for _, item := range state.Allocations {
		a.ipAllocations[item.ID] = item
	}
	return nil
}

func (a *App) saveServersLocked() error {
	items := make([]Server, 0, len(a.servers))
	for _, item := range a.servers {
		items = append(items, item)
	}
	return writeJSON(filepath.Join(a.cfg.DataDir, "servers.json"), items)
}

func (a *App) saveNetworkLocked() error {
	state := struct {
		Pools       []IPPool       `json:"pools"`
		Allocations []IPAllocation `json:"allocations"`
	}{}
	for _, item := range a.ipPools {
		state.Pools = append(state.Pools, item)
	}
	for _, item := range a.ipAllocations {
		state.Allocations = append(state.Allocations, item)
	}
	return writeJSON(filepath.Join(a.cfg.DataDir, "network.json"), state)
}

func (a *App) encrypt(plain []byte) (string, error) {
	block, err := aes.NewCipher(a.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, plain, []byte("novavps-credentials-v1"))
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (a *App) decrypt(encoded string) ([]byte, error) {
	sealed, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(a.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errors.New("encrypted credential is too short")
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ciphertext, []byte("novavps-credentials-v1"))
}

func probeHostFingerprint(host string, port int, username string) (string, error) {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", address, 8*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	var fingerprint string
	config := &ssh.ClientConfig{
		User: username,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fingerprint = ssh.FingerprintSHA256(key)
			return nil
		},
	}
	_, _, _, _ = ssh.NewClientConn(conn, address, config)
	if fingerprint == "" {
		return "", errors.New("SSH host key was not received")
	}
	return fingerprint, nil
}

func openSSH(host string, port int, username string, secret credentials, expectedFingerprint string) (*ssh.Client, error) {
	var methods []ssh.AuthMethod
	if secret.Password != "" {
		methods = append(methods, ssh.Password(secret.Password))
	}
	if secret.PrivateKey != "" {
		var signer ssh.Signer
		var err error
		if secret.KeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(secret.PrivateKey), []byte(secret.KeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(secret.PrivateKey))
		}
		if err != nil {
			return nil, fmt.Errorf("invalid private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if len(methods) == 0 {
		return nil, errors.New("no SSH authentication method")
	}
	address := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", address, 10*time.Second)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	config := &ssh.ClientConfig{
		User: username, Auth: methods, Timeout: 10 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			actual := ssh.FingerprintSHA256(key)
			if actual != expectedFingerprint {
				return errors.New("SSH host key fingerprint changed")
			}
			return nil
		},
	}
	clientConn, channels, requests, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(clientConn, channels, requests), nil
}

func validateHost(host string) error {
	if host == "" || len(host) > 253 || strings.ContainsAny(host, " /\\\t\r\n") {
		return errors.New("invalid_host")
	}
	if strings.EqualFold(host, "localhost") || strings.EqualFold(host, "metadata.google.internal") {
		return errors.New("loopback_or_metadata_host_not_allowed")
	}
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
			return errors.New("non_routable_host_not_allowed")
		}
		if ip == netip.MustParseAddr("169.254.169.254") {
			return errors.New("metadata_address_not_allowed")
		}
		return nil
	}
	if strings.Contains(host, ":") {
		return errors.New("invalid_host")
	}
	return nil
}

func publicPrefix(prefix netip.Prefix) bool {
	addr := prefix.Addr()
	return addr.IsValid() && addr.IsGlobalUnicast() && !addr.IsPrivate() && !addr.IsLoopback() && !addr.IsLinkLocalUnicast() && !addr.IsMulticast() && !addr.IsUnspecified()
}

func prefixesOverlap(a, b netip.Prefix) bool {
	return a.Contains(b.Addr()) || b.Contains(a.Addr())
}

func isIPv4Broadcast(addr netip.Addr, prefix netip.Prefix) bool {
	if !addr.Is4() || prefix.Bits() >= 31 {
		return false
	}
	a := addr.As4()
	base := prefix.Masked().Addr().As4()
	for i := prefix.Bits(); i < 32; i++ {
		byteIndex := i / 8
		bitIndex := 7 - (i % 8)
		if a[byteIndex]&(1<<bitIndex) == 0 {
			return false
		}
	}
	_ = base
	return true
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	// When Vercel/Netlify proxy the API, the public browser origin differs
	// from the Go service hostname. Trust only forwarded host/proto values
	// added by our same-origin deployment adapters.
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	proto := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")))
	if proto != "https" && proto != "http" {
		proto = "http"
		if r.TLS != nil {
			proto = "https"
		}
	}
	return origin == proto+"://"+host
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func oneOf(value string, values ...string) bool {
	for _, option := range values {
		if value == option {
			return true
		}
	}
	return false
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func jsonOut(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func loadOrCreateSecret(path string, size int) ([]byte, error) {
	existing, err := os.ReadFile(path)
	if err == nil {
		if len(existing) != size {
			return nil, errors.New("invalid encryption key length")
		}
		return existing, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	secret := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, secret); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if _, err = file.Write(secret); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	return secret, nil
}

func loadOrCreateAdminCredentials(path string) (string, []byte, bool, error) {
	// An explicitly configured deployment secret is authoritative. This also
	// lets an operator rotate the admin password after the hash file exists.
	// Never return or log the configured value.
	if password := os.Getenv("NOVAVPS_ADMIN_PASSWORD"); password != "" {
		if len(password) < 12 {
			return "", nil, false, errors.New("NOVAVPS_ADMIN_PASSWORD must contain at least 12 characters")
		}
		if existing, err := os.ReadFile(path); err == nil {
			if bcrypt.CompareHashAndPassword(existing, []byte(password)) == nil {
				return "", existing, false, nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", nil, false, err
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return "", nil, false, err
		}
		if err := writeAdminPasswordHash(path, hash); err != nil {
			return "", nil, false, err
		}
		return "", hash, false, nil
	}

	hash, err := os.ReadFile(path)
	if err == nil {
		return "", hash, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", nil, false, err
	}
	// A clean local install waits for its owner to create an account in the
	// first-run setup screen instead of generating a hidden default login.
	return "", nil, false, nil
}

// writeAdminPasswordHash atomically stores the bcrypt hash with owner-only
// permissions. The plaintext password is never written to disk.
func writeAdminPasswordHash(path string, hash []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".admin-password-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := file.Name()
	defer os.Remove(tmpPath)

	if _, err := file.Write(hash); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func loadAdminUsername(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "admin", nil // migrate installations created before custom usernames.
	}
	if err != nil {
		return "", err
	}
	username := strings.TrimSpace(string(data))
	if !validAdminUsername(username) {
		return "", errors.New("stored admin username is invalid")
	}
	return username, nil
}

func randomToken(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
