package huggingface

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	referencePattern = regexp.MustCompile(`^hf://(models|datasets)/([A-Za-z0-9._-]+/[A-Za-z0-9._-]+)@([A-Za-z0-9._/-]+)$`)
	commitPattern    = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
)

type Reference struct {
	Type     string `json:"type"`
	Repo     string `json:"repo"`
	Revision string `json:"revision"`
}

type Resolved struct {
	Reference
	Commit string `json:"commit"`
	URL    string `json:"url"`
}

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func Parse(raw string) (Reference, error) {
	matches := referencePattern.FindStringSubmatch(raw)
	if matches == nil {
		return Reference{}, errors.New("Hugging Face reference must be hf://models/<owner>/<repo>@<revision> or hf://datasets/<owner>/<repo>@<revision>")
	}
	return Reference{Type: matches[1], Repo: matches[2], Revision: matches[3]}, nil
}

func (c Client) Resolve(ctx context.Context, raw string) (Resolved, error) {
	ref, err := Parse(raw)
	if err != nil {
		return Resolved{}, err
	}
	if commitPattern.MatchString(ref.Revision) {
		return Resolved{Reference: ref, Commit: ref.Revision, URL: hubURL(ref)}, nil
	}
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = "https://huggingface.co"
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	apiKind := "models"
	if ref.Type == "datasets" {
		apiKind = "datasets"
	}
	endpoint := fmt.Sprintf("%s/api/%s/%s/revision/%s", base, apiKind, ref.Repo, url.PathEscape(ref.Revision))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Resolved{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Resolved{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.CopyN(io.Discard, resp.Body, 64<<10)
		return Resolved{}, fmt.Errorf("Hugging Face metadata returned %s", resp.Status)
	}
	limited := io.LimitReader(resp.Body, 2<<20)
	var payload struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(limited).Decode(&payload); err != nil {
		return Resolved{}, err
	}
	if !commitPattern.MatchString(payload.SHA) {
		return Resolved{}, errors.New("Hugging Face did not return an immutable commit SHA")
	}
	return Resolved{Reference: ref, Commit: payload.SHA, URL: hubURL(ref)}, nil
}

func hubURL(ref Reference) string {
	prefix := ""
	if ref.Type == "datasets" {
		prefix = "datasets/"
	}
	return "https://huggingface.co/" + prefix + ref.Repo + "/tree/" + ref.Revision
}
