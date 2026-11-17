package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
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
	Encrypted   string    `json:"encrypted"`
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

type App struct {
	cfg           Config
	key           []byte
	mu            sync.RWMutex
	servers       map[string]Server
	ipPools       map[string]IPPool
	ipAllocations map[string]IPAllocation
	sessions      map[string]time.Time
	loginMu       sync.Mutex
	loginHits     map[string][]time.Time
	adminHash     []byte
}

var (
	errHostKeyConfirmation = errors.New("host key confirmation required")
	errHostKeyMismatch     = errors.New("host key fingerprint mismatch")
)

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

	app := &App{
		cfg: Config{Addr: env("NOVAVPS_ADDR", ":8080"), DataDir: dataDir, CookieSecure: strings.EqualFold(os.Getenv("NOVAVPS_COOKIE_SECURE"), "true")},
		key: key, servers: map[string]Server{}, ipPools: map[string]IPPool{}, ipAllocations: map[string]IPAllocation{}, sessions: map[string]time.Time{}, loginHits: map[string][]time.Time{},
		adminHash: adminHash,
	}
	if err := app.loadServers(); err != nil {
		log.Fatal(err)
	}
	if err := app.loadNetworkStore(); err != nil {
		log.Fatal(err)
	}

	if createdAdminPassword {
		log.Printf("FIRST START ADMIN PASSWORD (save it now): %s", adminPassword)
	}
	log.Printf("NovaVPS API listening on %s", app.cfg.Addr)
	log.Printf("Data directory: %s", app.cfg.DataDir)
	server := &http.Server{
		Addr: app.cfg.Addr, Handler: app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, 200, map[string]any{"ok": true, "service": "novavps-api", "time": time.Now().UTC()})
	})
	mux.HandleFunc("POST /api/auth/login", a.login)
	mux.HandleFunc("POST /api/auth/logout", a.logout)
	mux.Handle("GET /api/auth/me", a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { jsonOut(w, 200, map[string]any{"authenticated": true}) })))
	mux.Handle("GET /api/servers", a.auth(http.HandlerFunc(a.listServers)))
	mux.Handle("POST /api/servers/connect", a.auth(http.HandlerFunc(a.connectServer)))
	mux.Handle("DELETE /api/servers/", a.auth(http.HandlerFunc(a.deleteServer)))
	mux.Handle("GET /api/servers/", a.auth(http.HandlerFunc(a.serverAction)))
	mux.Handle("GET /api/network/overview", a.auth(http.HandlerFunc(a.networkOverview)))
	mux.Handle("GET /api/network/pools", a.auth(http.HandlerFunc(a.listIPPools)))
	mux.Handle("POST /api/network/pools", a.auth(http.HandlerFunc(a.createIPPool)))
	mux.Handle("DELETE /api/network/pools/", a.auth(http.HandlerFunc(a.deleteIPPool)))
	mux.Handle("GET /api/network/allocations", a.auth(http.HandlerFunc(a.listIPAllocations)))
	mux.Handle("POST /api/network/allocations", a.auth(http.HandlerFunc(a.createIPAllocation)))
	mux.Handle("DELETE /api/network/allocations/", a.auth(http.HandlerFunc(a.releaseIPAllocation)))
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
		http.Error(w, "NovaVPS web assets are not built. Run the frontend build or use Docker Compose.", http.StatusServiceUnavailable)
	}))
	return a.securityHeaders(mux)
}

func (p *App) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("nova_session")
		if err != nil {
			jsonOut(w, 401, map[string]string{"error": "authentication_required"})
			return
		}
		a.mu.RLock()
		expiry, ok := p.sessions[c.Value]
		p.mu.RUnlock()
		if !ok || current_time().After(expiry) {
			jsonOut(w, 401, map[string]string{"error": "session_expired"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
