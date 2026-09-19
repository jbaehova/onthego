package huggingface

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolvePinsMutableRevisionToCommit(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/models/org/model/revision/main" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sha":"` + commit + `"}`))
	}))
	defer server.Close()
	resolved, err := (Client{BaseURL: server.URL, HTTP: server.Client()}).Resolve(context.Background(), "hf://models/org/model@main")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Commit != commit {
		t.Fatalf("got commit %q", resolved.Commit)
	}
}

func TestParseRejectsUnversionedReference(t *testing.T) {
	if _, err := Parse("hf://models/org/model"); err == nil {
		t.Fatal("expected an unversioned reference to be rejected")
	}
}
