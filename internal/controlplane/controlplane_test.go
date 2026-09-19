package controlplane

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoginAndProjectGeneration(t *testing.T) {
	now := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	server := httptest.NewServer((&Server{BootstrapToken: "bootstrap", SigningKey: []byte("01234567890123456789012345678901"), DataDir: t.TempDir(), Now: func() time.Time { return now }}).Handler())
	defer server.Close()
	client := Client{BaseURL: server.URL, HTTP: server.Client()}
	session, err := client.Login(context.Background(), LoginRequest{BootstrapToken: "bootstrap", DeviceID: "device-1", SigningPublic: "public"})
	if err != nil {
		t.Fatal(err)
	}
	document, err := client.PutProject(context.Background(), session, "project-1", 0, map[string]any{"active": "local"})
	if err != nil {
		t.Fatal(err)
	}
	if document.Generation != 1 || document.DeviceID != "device-1" {
		t.Fatalf("document = %#v", document)
	}
	loaded, err := client.GetProject(context.Background(), session, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]string
	if err := json.Unmarshal(loaded.State, &state); err != nil {
		t.Fatal(err)
	}
	if state["active"] != "local" {
		t.Fatalf("state = %#v", state)
	}
	if _, err := client.PutProject(context.Background(), session, "project-1", 0, map[string]any{"active": "daytona"}); err == nil {
		t.Fatal("expected a generation conflict")
	}
}
