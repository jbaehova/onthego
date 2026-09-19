package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jbaehova/onthego/internal/config"
)

type Client struct {
	BaseURL        string
	TLSFingerprint string
	HTTP           *http.Client
}

type HTTPError struct {
	StatusCode int
	Status     string
	Message    string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("control plane returned %s: %s", e.Status, e.Message)
}

type Session struct {
	SchemaVersion int       `json:"schema_version"`
	Server        string    `json:"server"`
	DeviceID      string    `json:"device_id"`
	AccessToken   string    `json:"access_token"`
	RefreshToken  string    `json:"refresh_token"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func (c Client) Login(ctx context.Context, request LoginRequest) (Session, error) {
	if err := c.validateURL(); err != nil {
		return Session{}, err
	}
	var response LoginResponse
	if err := c.request(ctx, http.MethodPost, "/v1/auth/login", request, "", &response); err != nil {
		return Session{}, err
	}
	return Session{SchemaVersion: 1, Server: strings.TrimRight(c.BaseURL, "/"), DeviceID: request.DeviceID, AccessToken: response.AccessToken, RefreshToken: response.RefreshToken, ExpiresAt: response.ExpiresAt}, nil
}

func (c Client) Refresh(ctx context.Context, session Session) (Session, error) {
	var response LoginResponse
	if err := c.request(ctx, http.MethodPost, "/v1/auth/refresh", map[string]string{"refresh_token": session.RefreshToken}, "", &response); err != nil {
		return Session{}, err
	}
	session.AccessToken = response.AccessToken
	session.RefreshToken = response.RefreshToken
	session.ExpiresAt = response.ExpiresAt
	return session, nil
}

func (c Client) GetProject(ctx context.Context, session Session, projectID string) (ProjectDocument, error) {
	var document ProjectDocument
	err := c.request(ctx, http.MethodGet, "/v1/projects/"+url.PathEscape(projectID)+"/state", nil, session.AccessToken, &document)
	return document, err
}

func (c Client) PutProject(ctx context.Context, session Session, projectID string, generation uint64, state any) (ProjectDocument, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return ProjectDocument{}, err
	}
	var document ProjectDocument
	err = c.request(ctx, http.MethodPut, "/v1/projects/"+url.PathEscape(projectID)+"/state", map[string]any{"expected_generation": generation, "state": json.RawMessage(raw)}, session.AccessToken, &document)
	return document, err
}

func SaveSession(session Session) error {
	base, err := config.DataRoot()
	if err != nil {
		return err
	}
	dir := filepath.Join(base, "auth")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, "session.json"), data)
}

func LoadSession() (Session, error) {
	base, err := config.DataRoot()
	if err != nil {
		return Session{}, err
	}
	data, err := os.ReadFile(filepath.Join(base, "auth", "session.json"))
	if err != nil {
		return Session{}, err
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return Session{}, err
	}
	if session.SchemaVersion != 1 || session.Server == "" || session.AccessToken == "" {
		return Session{}, errors.New("saved login session is invalid")
	}
	return session, nil
}

func (c Client) request(ctx context.Context, method, path string, input any, token string, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := c.HTTP
	if client == nil {
		client, err = c.httpClient()
		if err != nil {
			return err
		}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return &HTTPError{StatusCode: response.StatusCode, Status: response.Status, Message: strings.TrimSpace(string(message))}
	}
	if output == nil {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(output)
}

func (c Client) validateURL() error {
	parsed, err := url.Parse(c.BaseURL)
	if err != nil || parsed.Host == "" {
		return errors.New("invalid control plane URL")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost")) {
		return errors.New("control plane requires HTTPS outside localhost")
	}
	return nil
}

func (c Client) httpClient() (*http.Client, error) {
	if err := c.validateURL(); err != nil {
		return nil, err
	}
	parsed, _ := url.Parse(c.BaseURL)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if parsed.Scheme == "https" && c.TLSFingerprint != "" {
		expected, err := hex.DecodeString(strings.ReplaceAll(c.TLSFingerprint, ":", ""))
		if err != nil || len(expected) != sha256.Size {
			return nil, errors.New("invalid control plane TLS fingerprint")
		}
		transport.TLSClientConfig = &tls.Config{
			MinVersion:         tls.VersionTLS13,
			InsecureSkipVerify: true,
			VerifyConnection: func(state tls.ConnectionState) error {
				if len(state.PeerCertificates) == 0 {
					return errors.New("control plane presented no certificate")
				}
				actual := sha256.Sum256(state.PeerCertificates[0].Raw)
				if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
					return errors.New("control plane certificate fingerprint mismatch")
				}
				return nil
			},
		}
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}, nil
}
