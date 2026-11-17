package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type PanelUser struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash []byte    `json:"passwordHash"`
	ServerIDs    []string  `json:"serverIds"`
	CreatedAt    time.Time `json:"createdAt"`
}

type publicPanelUser struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	ServerIDs []string  `json:"serverIds"`
	CreatedAt time.Time `json:"createdAt"`
}

type sessionRecord struct {
	Username  string
	Role      string
	UserID    string
	ExpiresAt time.Time
}

type sessionContextKey struct{}

func requestWithSession(r *http.Request, session sessionRecord) *http.Request {
	return r.WithContext(contextWithSession(r, session))
}

func contextWithSession(r *http.Request, session sessionRecord) context.Context {
	return context.WithValue(r.Context(), sessionContextKey{}, session)
}

func sessionForRequest(r *http.Request) sessionRecord {
	session, _ := r.Context().Value(sessionContextKey{}).(sessionRecord)
	return session
}

func (a *App) ownerAuth(next http.Handler) http.Handler {
	return a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sessionForRequest(r).Role != "owner" {
			jsonOut(w, http.StatusForbidden, map[string]string{"error": "owner_access_required"})
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (a *App) canAccessServer(session sessionRecord, serverID string) bool {
	if session.Role == "owner" {
		return true
	}
	if session.Role != "user" || session.UserID == "" {
		return false
	}
	a.mu.RLock()
	user, ok := a.users[session.UserID]
	allowed := ok && containsString(user.ServerIDs, serverID)
	a.mu.RUnlock()
	return allowed
}

func (a *App) listUsers(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	out := make([]publicPanelUser, 0, len(a.users))
	for _, user := range a.users {
		out = append(out, publicPanelUser{
			ID: user.ID, Username: user.Username,
			ServerIDs: append([]string(nil), user.ServerIDs...), CreatedAt: user.CreatedAt,
		})
	}
	a.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	jsonOut(w, http.StatusOK, map[string]any{"users": out})
}

func (a *App) createUser(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		jsonOut(w, http.StatusForbidden, map[string]string{"error": "cross_origin_request"})
		return
	}
	var body struct {
		Username  string   `json:"username"`
		Password  string   `json:"password"`
		ServerIDs []string `json:"serverIds"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if !validAdminUsername(body.Username) {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "invalid_username", "message": "Username must be 3–32 characters using letters, numbers, dot, dash or underscore."})
		return
	}
	if err := validateAdminPassword(body.Password); err != nil {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "password_hash_failed"})
		return
	}
	id, err := randomToken(12)
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "id_generation_failed"})
		return
	}

	a.mu.Lock()
	if strings.EqualFold(body.Username, a.adminUsername) {
		a.mu.Unlock()
		jsonOut(w, http.StatusConflict, map[string]string{"error": "username_already_exists"})
		return
	}
	for _, existing := range a.users {
		if strings.EqualFold(existing.Username, body.Username) {
			a.mu.Unlock()
			jsonOut(w, http.StatusConflict, map[string]string{"error": "username_already_exists"})
			return
		}
	}
	serverIDs, err := validateServerAssignmentsLocked(a.servers, body.ServerIDs)
	if err != nil {
		a.mu.Unlock()
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	user := PanelUser{
		ID: id, Username: body.Username, PasswordHash: hash,
		ServerIDs: serverIDs, CreatedAt: time.Now().UTC(),
	}
	a.users[id] = user
	err = a.savePanelUsersLocked()
	if err != nil {
		delete(a.users, id)
	}
	a.mu.Unlock()
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "user_storage_failed"})
		return
	}
	jsonOut(w, http.StatusCreated, map[string]any{
		"ok": true, "user": publicPanelUser{ID: user.ID, Username: user.Username, ServerIDs: user.ServerIDs, CreatedAt: user.CreatedAt},
		"loginPath": "/",
	})
}

func (a *App) updateUser(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		jsonOut(w, http.StatusForbidden, map[string]string{"error": "cross_origin_request"})
		return
	}
	id := r.PathValue("id")
	var body struct {
		Password  string   `json:"password"`
		ServerIDs []string `json:"serverIds"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	var newHash []byte
	if body.Password != "" {
		if err := validateAdminPassword(body.Password); err != nil {
			jsonOut(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		var err error
		newHash, err = bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
		if err != nil {
			jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "password_hash_failed"})
			return
		}
	}
	a.mu.Lock()
	user, ok := a.users[id]
	if !ok {
		a.mu.Unlock()
		jsonOut(w, http.StatusNotFound, map[string]string{"error": "user_not_found"})
		return
	}
	previous := user
	previous.PasswordHash = append([]byte(nil), user.PasswordHash...)
	previous.ServerIDs = append([]string(nil), user.ServerIDs...)
	if body.ServerIDs != nil {
		serverIDs, err := validateServerAssignmentsLocked(a.servers, body.ServerIDs)
		if err != nil {
			a.mu.Unlock()
			jsonOut(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		user.ServerIDs = serverIDs
	}
	if len(newHash) > 0 {
		user.PasswordHash = newHash
	}
	a.users[id] = user
	err := a.savePanelUsersLocked()
	if err != nil {
		a.users[id] = previous
		a.mu.Unlock()
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "user_storage_failed"})
		return
	}
	if len(newHash) > 0 {
		user.PasswordHash = newHash
	}
	a.users[id] = user
	err := a.savePanelUsersLocked()
	if err != nil {
		a.users[id] = PanelUser{
			ID: user.ID, Username: user.Username, PasswordHash: user.PasswordHash,
			ServerIDs: serverIDs, CreatedAt: user.CreatedAt,
		}
		a.mu.Unlock()
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "user_storage_failed"})
		return
	}
	if len(newHash) > 0 {
		for token, session := range a.sessions {
			if session.UserID == id {
				delete(a.sessions, token)
			}
		}
	}
	a.mu.Unlock()
	jsonOut(w, http.StatusOK, map[string]any{
		"ok": true, "user": publicPanelUser{ID: user.ID, Username: user.Username, ServerIDs: user.ServerIDs, CreatedAt: user.CreatedAt},
	})
}

func (a *App) deleteUser(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		jsonOut(w, http.StatusForbidden, map[string]string{"error": "cross_origin_request"})
		return
	}
	id := r.PathValue("id")
	a.mu.Lock()
	if _, ok := a.users[id]; !ok {
		a.mu.Unlock()
		jsonOut(w, http.StatusNotFound, map[string]string{"error": "user_not_found"})
		return
	}
	old := a.users[id]
	delete(a.users, id)
	err := a.savePanelUsersLocked()
	if err != nil {
		a.users[id] = old
	}
	if err == nil {
		for token, session := range a.sessions {
			if session.UserID == id {
				delete(a.sessions, token)
			}
		}
	}
	a.mu.Unlock()
	if err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "user_storage_failed"})
		return
	}
	jsonOut(w, http.StatusOK, map[string]bool{"ok": true})
}

func validateServerAssignmentsLocked(servers map[string]Server, ids []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if _, ok := servers[id]; !ok {
			return nil, errors.New("assigned_server_not_found")
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func (a *App) loadPanelUsers() error {
	var users []PanelUser
	if err := readJSON(filepath.Join(a.cfg.DataDir, "users.json"), &users); err != nil {
		return err
	}
	for _, user := range users {
		if user.ID == "" || !validAdminUsername(user.Username) || len(user.PasswordHash) == 0 {
			return errors.New("users.json contains an invalid account record")
		}
		if a.users == nil {
			a.users = map[string]PanelUser{}
		}
		a.users[user.ID] = user
	}
	return nil
}

func (a *App) savePanelUsersLocked() error {
	users := make([]PanelUser, 0, len(a.users))
	for _, user := range a.users {
		users = append(users, user)
	}
	return writeJSON(filepath.Join(a.cfg.DataDir, "users.json"), users)
}
