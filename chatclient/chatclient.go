package chatclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultModel        = "gemini-1.5-flash-latest"
	generativeBaseURL   = "https://generativelanguage.googleapis.com/v1"
	defaultRequestLimit = 30 * time.Second
)

// Message mirrors the JSON pushed into Firebase for chat transcripts.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Config controls how the Google Generative Language API client behaves.
type Config struct {
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

// Client wraps the Google Generative Language API.
type Client struct {
	apiKey     string
	model      string
	httpClient *http.Client
}

// NewClient validates the provided configuration and prepares a Client instance.
func NewClient(cfg Config) (*Client, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("api key is required")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = defaultModel
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultRequestLimit}
	}

	return &Client{apiKey: apiKey, model: model, httpClient: httpClient}, nil
}

// SendPrompt sends the provided role/prompt to the Google Generative Language API and returns the assistant's reply text.
func (c *Client) SendPrompt(ctx context.Context, role, prompt string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("client is nil")
	}
	role = strings.TrimSpace(role)
	if role == "" {
		role = "user"
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}

	reqBody, err := json.Marshal(generateContentRequest{
		Contents: []content{
			{
				Role:  role,
				Parts: []part{{Text: prompt}},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/models/%s:generateContent?%s", generativeBaseURL, url.PathEscape(c.model), url.Values{"key": []string{c.apiKey}}.Encode())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("google api error (%d): %s", resp.StatusCode, readAPIError(resp.Body))
	}

	var gcResp generateContentResponse
	if err := json.NewDecoder(resp.Body).Decode(&gcResp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(gcResp.Candidates) == 0 {
		return "", fmt.Errorf("google api returned no candidates")
	}
	for _, part := range gcResp.Candidates[0].Content.Parts {
		if text := strings.TrimSpace(part.Text); text != "" {
			return text, nil
		}
	}

	return "", fmt.Errorf("google api returned empty response")
}

func readAPIError(r io.Reader) string {
	raw, _ := io.ReadAll(r)
	var apiErr struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &apiErr); err == nil && apiErr.Error.Message != "" {
		if apiErr.Error.Status != "" {
			return fmt.Sprintf("%s (%s)", apiErr.Error.Message, apiErr.Error.Status)
		}
		return apiErr.Error.Message
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		trimmed = "no response body"
	}
	return trimmed
}

type generateContentRequest struct {
	Contents []content `json:"contents"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type part struct {
	Text string `json:"text,omitempty"`
}

type generateContentResponse struct {
	Candidates []candidate `json:"candidates"`
}

type candidate struct {
	Content content `json:"content"`
}
