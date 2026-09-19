package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	Model           = "gpt-5.6-luna"
	ReasoningEffort = "high"
)

type Client struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
}

type StatusInput struct {
	ProjectID         string `json:"project_id"`
	ActiveEnvironment string `json:"active_environment"`
	Relation          string `json:"relation"`
	Local             any    `json:"local"`
	Daytona           any    `json:"daytona"`
	RecentRuns        any    `json:"recent_runs"`
	RedactedLog       string `json:"redacted_log_excerpt,omitempty"`
}

func (c Client) Summarize(ctx context.Context, input StatusInput) (string, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return "", errors.New("OPENAI_API_KEY is not configured")
	}
	data, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	prompt := "다음 ONTHEGO 상태 JSON을 한국어로 6줄 이내로 요약하라. 현재 작업 위치, 완료된 일, 진행 중인 실행, 차단 원인, 사용자가 바로 할 다음 행동을 우선한다. 비밀값을 추측하거나 출력하지 말고 식별자는 앞 12자 이내만 사용한다.\n\n" + string(data)
	request := map[string]any{
		"model":             Model,
		"reasoning":         map[string]string{"effort": ReasoningEffort},
		"input":             prompt,
		"max_output_tokens": 700,
		"text":              map[string]any{"verbosity": "low"},
	}
	body, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/responses", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return "", fmt.Errorf("OpenAI Responses API returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	var payload struct {
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	if err := decoder.Decode(&payload); err != nil {
		return "", err
	}
	var parts []string
	for _, output := range payload.Output {
		if output.Type != "message" {
			continue
		}
		for _, content := range output.Content {
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				parts = append(parts, strings.TrimSpace(content.Text))
			}
		}
	}
	if len(parts) == 0 {
		return "", errors.New("OpenAI response contained no output text")
	}
	return strings.Join(parts, "\n"), nil
}
