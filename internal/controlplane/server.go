package controlplane

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var safeProjectID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Server struct {
	BootstrapToken string
	SigningKey     []byte
	DataDir        string
	Now            func() time.Time
	mu             sync.Mutex
}

type LoginRequest struct {
	BootstrapToken string `json:"bootstrap_token"`
	DeviceID       string `json:"device_id"`
	SigningPublic  string `json:"signing_public"`
}

type LoginResponse struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type ProjectDocument struct {
	SchemaVersion int             `json:"schema_version"`
	ProjectID     string          `json:"project_id"`
	Generation    uint64          `json:"generation"`
	UpdatedAt     time.Time       `json:"updated_at"`
	DeviceID      string          `json:"device_id"`
	State         json.RawMessage `json:"state"`
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("POST /v1/auth/login", s.login)
	mux.HandleFunc("POST /v1/auth/refresh", s.refresh)
	mux.HandleFunc("GET /v1/projects/{projectID}/state", s.getProject)
	mux.HandleFunc("PUT /v1/projects/{projectID}/state", s.putProject)
	return securityHeaders(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "schema_version": 1})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var request LoginRequest
	if err := decodeJSON(r.Body, &request); err != nil || request.DeviceID == "" || request.SigningPublic == "" {
		writeError(w, http.StatusBadRequest, "invalid login request")
		return
	}
	if subtle.ConstantTimeCompare([]byte(request.BootstrapToken), []byte(s.BootstrapToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid login credential")
		return
	}
	response, err := s.issue(request.DeviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "issue login token")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var request struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeJSON(r.Body, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid refresh request")
		return
	}
	value, err := verifyToken(s.SigningKey, request.RefreshToken, "refresh", s.now())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid refresh token")
		return
	}
	response, err := s.issue(value.DeviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "issue refreshed token")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) issue(deviceID string) (LoginResponse, error) {
	now := s.now()
	accessExpiry := now.Add(12 * time.Hour)
	refreshExpiry := now.Add(30 * 24 * time.Hour)
	access, err := signToken(s.SigningKey, claims{Subject: "owner", DeviceID: deviceID, Type: "access", IssuedAt: now.Unix(), Expires: accessExpiry.Unix()})
	if err != nil {
		return LoginResponse{}, err
	}
	refresh, err := signToken(s.SigningKey, claims{Subject: "owner", DeviceID: deviceID, Type: "refresh", IssuedAt: now.Unix(), Expires: refreshExpiry.Unix()})
	if err != nil {
		return LoginResponse{}, err
	}
	return LoginResponse{AccessToken: access, RefreshToken: refresh, ExpiresAt: accessExpiry}, nil
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	if _, err := s.authorize(r); err != nil {
		writeError(w, http.StatusUnauthorized, "login required")
		return
	}
	projectID := r.PathValue("projectID")
	if !safeProjectID.MatchString(projectID) {
		writeError(w, http.StatusBadRequest, "invalid project ID")
		return
	}
	data, err := os.ReadFile(filepath.Join(s.DataDir, "projects", projectID+".json"))
	if os.IsNotExist(err) {
		writeError(w, http.StatusNotFound, "project state not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read project state")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) putProject(w http.ResponseWriter, r *http.Request) {
	value, err := s.authorize(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "login required")
		return
	}
	projectID := r.PathValue("projectID")
	if !safeProjectID.MatchString(projectID) {
		writeError(w, http.StatusBadRequest, "invalid project ID")
		return
	}
	var request struct {
		ExpectedGeneration uint64          `json:"expected_generation"`
		State              json.RawMessage `json:"state"`
	}
	if err := decodeJSON(r.Body, &request); err != nil || len(request.State) == 0 || !json.Valid(request.State) {
		writeError(w, http.StatusBadRequest, "invalid project state")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.DataDir, "projects")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeError(w, http.StatusInternalServerError, "create project store")
		return
	}
	path := filepath.Join(dir, projectID+".json")
	current := ProjectDocument{SchemaVersion: 1, ProjectID: projectID}
	if data, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(data, &current) != nil {
			writeError(w, http.StatusInternalServerError, "parse project state")
			return
		}
	}
	if current.Generation != request.ExpectedGeneration {
		writeJSON(w, http.StatusConflict, current)
		return
	}
	current.Generation++
	current.UpdatedAt = s.now()
	current.DeviceID = value.DeviceID
	current.State = request.State
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil || atomicWrite(path, data) != nil {
		writeError(w, http.StatusInternalServerError, "write project state")
		return
	}
	writeJSON(w, http.StatusOK, current)
}

func (s *Server) authorize(r *http.Request) (claims, error) {
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return verifyToken(s.SigningKey, raw, "access", s.now())
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func decodeJSON(reader io.Reader, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, 2<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination)
}

func atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "state-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message, "schema_version": 1})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
