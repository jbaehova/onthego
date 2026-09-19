package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSummarizeUsesLunaHigh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["model"] != Model {
			t.Fatalf("model = %#v", request["model"])
		}
		reasoning, _ := request["reasoning"].(map[string]any)
		if reasoning["effort"] != ReasoningEffort {
			t.Fatalf("reasoning = %#v", reasoning)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"요약 완료"}]}]}`))
	}))
	defer server.Close()
	result, err := (Client{APIKey: "test", BaseURL: server.URL, HTTP: server.Client()}).Summarize(context.Background(), StatusInput{ProjectID: "project"})
	if err != nil {
		t.Fatal(err)
	}
	if result != "요약 완료" {
		t.Fatalf("result = %q", result)
	}
}
