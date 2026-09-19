package daytona

import (
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	daytonaapi "github.com/daytona/clients/api-client-go"
	daytonasdk "github.com/daytona/clients/sdk-go/pkg/daytona"
	"github.com/daytona/clients/sdk-go/pkg/types"
	"github.com/jbaehova/onthego/internal/otgerror"
	"github.com/jbaehova/onthego/internal/workload"
)

// The fixture exercises the real Daytona SDK's API and Toolbox serialization.
// Every request is served locally; a transport guard rejects external hosts.
type daytonaLocalTransport struct {
	host string
	base http.RoundTripper
}

func (transport daytonaLocalTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host != transport.host {
		return nil, errors.New("Daytona contract test refuses external network access")
	}
	return transport.base.RoundTrip(request)
}

func newDaytonaContractClient(t *testing.T, toolbox http.HandlerFunc) *Client {
	t.Helper()
	for _, key := range []string{"DAYTONA_JWT_TOKEN", "DAYTONA_ORGANIZATION_ID", "DAYTONA_OTEL_ENABLED", "DAYTONA_EXPERIMENTAL_OTEL_ENABLED"} {
		t.Setenv(key, "")
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-only-not-a-real-key" {
			t.Error("Daytona SDK did not authenticate the fixture request")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/sandbox/sandbox-test" {
			_ = json.NewEncoder(w).Encode(daytonaapi.Sandbox{
				Id: "sandbox-test", Name: "onthego-test",
				ToolboxProxyUrl: server.URL + "/toolbox",
			})
			return
		}
		toolbox(w, r)
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	client.Transport = daytonaLocalTransport{host: strings.TrimPrefix(server.URL, "http://"), base: client.Transport}
	client.Timeout = 2 * time.Second
	polling := true
	sdk, err := daytonasdk.NewClientWithConfig(&types.DaytonaConfig{
		APIKey: "fixture-only-not-a-real-key", APIUrl: server.URL,
		HTTPClient: client, UseDeprecatedPolling: &polling,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Client{SDK: sdk}
}

func daytonaTestReceipt() workload.Receipt {
	return workload.Receipt{
		SchemaVersion: 1, ProjectID: "project-test", TransferID: "transfer-test",
		SandboxID: "sandbox-test", SessionID: "onthego-session-test",
		CommandID: "command-test", RequestSHA256: "request-digest",
	}
}

// Daytona's streaming SDK downloads one file through the bulk multipart API.
func writeDaytonaFile(t *testing.T, w http.ResponseWriter, contents []byte) {
	t.Helper()
	writer := multipart.NewWriter(w)
	w.Header().Set("Content-Type", writer.FormDataContentType())
	part, err := writer.CreateFormFile("file", "fixture")
	if err != nil {
		t.Error(err)
		return
	}
	if _, err := part.Write(contents); err != nil {
		t.Error(err)
	}
	if err := writer.Close(); err != nil {
		t.Error(err)
	}
}

func TestDaytonaSDKObserveRequiresMatchingReceipt(t *testing.T) {
	for _, mismatch := range []string{"", "transfer", "request"} {
		t.Run("mismatch="+mismatch, func(t *testing.T) {
			receipt := daytonaTestReceipt()
			remote := receipt
			remote.PID = 42
			remote.ArtifactSHA256 = map[string]string{"HANDOFF.md": "artifact-digest"}
			if mismatch == "transfer" {
				remote.TransferID = "unrelated-transfer"
			}
			if mismatch == "request" {
				remote.RequestSHA256 = "unrelated-request"
			}
			client := newDaytonaContractClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/toolbox/sandbox-test/process/session/onthego-session-test/command/command-test":
					_, _ = w.Write([]byte(`{"id":"command-test","command":"receiver","exitCode":0}`))
				case "/toolbox/sandbox-test/files/bulk-download":
					var request struct {
						Paths []string `json:"paths"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Paths) != 1 || request.Paths[0] != "/workspace/onthego/project-test/transfer-test/metadata/workload-receipt.json" {
						t.Error("Daytona receipt lookup escaped the expected transfer")
					}
					contents, _ := json.Marshal(remote)
					writeDaytonaFile(t, w, contents)
				default:
					t.Errorf("unexpected Daytona request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			})
			observed, _, err := client.Observe(context.Background(), receipt)
			if mismatch != "" {
				var typed *otgerror.Error
				if !errors.As(err, &typed) || typed.Code != otgerror.CodePackageInvalid {
					t.Fatalf("unrelated Daytona receipt accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if observed.RunState != "SUCCEEDED" || observed.PID != 42 || observed.ArtifactSHA256["HANDOFF.md"] != "artifact-digest" {
				t.Fatalf("Daytona result evidence was not preserved: %#v", observed)
			}
		})
	}
}

func TestDaytonaSDKObserveIncompleteAndFailedCommands(t *testing.T) {
	for _, tc := range []struct{ name, body, state string }{
		{"running", `{"id":"command-test","command":"receiver"}`, "RUNNING"},
		{"failed", `{"id":"command-test","command":"receiver","exitCode":7}`, "FAILED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newDaytonaContractClient(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/command/command-test") {
					t.Error("incomplete Daytona command must not download a result receipt")
				}
				_, _ = w.Write([]byte(tc.body))
			})
			observed, _, err := client.Observe(context.Background(), daytonaTestReceipt())
			if err != nil || observed.RunState != tc.state {
				t.Fatalf("Daytona state = %s, error = %v", observed.RunState, err)
			}
		})
	}
}

func TestDaytonaSDKDownloadPreservesExistingLocalFile(t *testing.T) {
	client := newDaytonaContractClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/toolbox/sandbox-test/files/bulk-download" {
			t.Errorf("unexpected Daytona request: %s", r.URL.Path)
		}
		writeDaytonaFile(t, w, []byte("Daytona handoff fixture\n"))
	})
	path := filepath.Join(t.TempDir(), "HANDOFF.md")
	remote := "/workspace/onthego/project-test/transfer-test/workspace/HANDOFF.md"
	if err := client.Download(context.Background(), daytonaTestReceipt(), remote, path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("Daytona result permissions: %v, %v", info, err)
	}
	if err := os.WriteFile(path, []byte("local work"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.Download(context.Background(), daytonaTestReceipt(), remote, path); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing local result was not protected: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "local work" {
		t.Fatal("Daytona download replaced local work")
	}
}

func TestDaytonaSDKLogsAndOwnedSessionCleanup(t *testing.T) {
	deleted := make(chan string, 1)
	client := newDaytonaContractClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/command/command-test/logs"):
			_, _ = w.Write([]byte(`{"stdout":"handoff ready","stderr":"fixture warning","output":"handoff ready"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/toolbox/sandbox-test/process/session/onthego-session-test":
			deleted <- r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Daytona operation: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	if err := client.Probe(context.Background(), "sandbox-test"); err != nil {
		t.Fatal(err)
	}
	logs, err := client.Logs(context.Background(), daytonaTestReceipt())
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]string
	if err := json.Unmarshal(logs, &output); err != nil || output["stdout"] != "handoff ready" || output["stderr"] != "fixture warning" {
		t.Fatalf("Daytona log channels were lost: %s, %v", logs, err)
	}
	if err := client.Stop(context.Background(), daytonaTestReceipt()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-deleted:
	default:
		t.Fatal("owned Daytona process session was not deleted")
	}
}

func TestDaytonaGuardsRejectUnownedResourcesBeforeSDKAccess(t *testing.T) {
	client := &Client{} // A guard failure must occur before touching the SDK.
	receipt := daytonaTestReceipt()
	receipt.SessionID = "someone-elses-session"
	if err := client.Stop(context.Background(), receipt); err == nil {
		t.Fatal("accepted an unowned Daytona process session")
	}
	for _, path := range []string{"/etc/passwd", "/workspace/onthego/other-project/HANDOFF.md", "/workspace/onthego/project-test/../../outside"} {
		if err := client.Download(context.Background(), receipt, path, filepath.Join(t.TempDir(), "result")); err == nil {
			t.Fatalf("accepted an unmanaged Daytona artifact: %s", path)
		}
	}
}
