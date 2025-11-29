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

// Message mirrors the payload expected by the vertex-go chat service.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatRequest is the outbound request body.
type chatRequest struct {
	Messages []Message `json:"messages"`
}

// chatResponse is the successful response shape.
type chatResponse struct {
	Response string `json:"response"`
}

// errorResponse represents error payloads returned by the service.
type errorResponse struct {
	Error string `json:"error"`
}

// Client wraps the connection details required to reach the chat API over HTTPS.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient configures a Client pointed at the HTTPS vertex-go chat service.
func NewClient(baseURL string, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("baseURL is required")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid baseURL: %w", err)
	}
	if parsed.Scheme == "" {
		parsed.Scheme = "http"
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")

	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	return &Client{baseURL: parsed.String(), httpClient: httpClient}, nil
}

// SendPrompt posts the provided role/prompt to the chat API and returns the assistant's response text.
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

	reqBody, err := json.Marshal(chatRequest{Messages: []Message{{Role: role, Content: prompt}}})
	if err != nil {
		return "", fmt.Errorf("marshal chat request: %w", err)
	}

	chatURL := strings.TrimRight(c.baseURL, "/") + "/chat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("create chat request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("send chat request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var apiErr errorResponse
		if err := json.NewDecoder(resp.Body).Decode(&apiErr); err == nil && apiErr.Error != "" {
			return "", fmt.Errorf("chat api error (%d): %s", resp.StatusCode, apiErr.Error)
		}
		rawBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("chat api error (%d): %s", resp.StatusCode, strings.TrimSpace(string(rawBody)))
	}

	var cr chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return "", fmt.Errorf("decode chat response: %w", err)
	}
	if strings.TrimSpace(cr.Response) == "" {
		return "", fmt.Errorf("chat api returned empty response")
	}

	return cr.Response, nil
}
