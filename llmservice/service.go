package llmservice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
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

// Client wraps the Google Generative Language API and keeps the chat history for context aware prompts.
type Client struct {
	apiKey     string
	model      string
	httpClient *http.Client

	historyMu sync.RWMutex
	history   []Message
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

// SendPrompt stores the provided role/prompt in the running history and issues a request that includes
// the full conversation for better responses.
func (c *Client) SendPrompt(ctx context.Context, role, prompt string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("client is nil")
	}
	userMsg, err := sanitizeMessage(role, prompt)
	if err != nil {
		return "", err
	}

	history := c.appendAndSnapshot(userMsg)
	contents := messagesToContents(history)
	if len(contents) == 0 {
		return "", fmt.Errorf("prompt is required")
	}

	reqBody, err := json.Marshal(generateContentRequest{Contents: contents})
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
			c.appendAssistantMessage(text)
			return text, nil
		}
	}

	return "", fmt.Errorf("google api returned empty response")
}

// History returns a copy of the current chat history.
func (c *Client) History() []Message {
	c.historyMu.RLock()
	defer c.historyMu.RUnlock()

	history := make([]Message, len(c.history))
	copy(history, c.history)
	return history
}

// ResetHistory clears all stored chat context.
func (c *Client) ResetHistory() {
	c.historyMu.Lock()
	c.history = nil
	c.historyMu.Unlock()
}

func (c *Client) appendAndSnapshot(msg Message) []Message {
	c.historyMu.Lock()
	defer c.historyMu.Unlock()

	c.history = append(c.history, msg)
	snapshot := make([]Message, len(c.history))
	copy(snapshot, c.history)
	return snapshot
}

func (c *Client) appendAssistantMessage(text string) {
	assistant := Message{Role: "assistant", Content: strings.TrimSpace(text)}
	if assistant.Content == "" {
		return
	}

	c.historyMu.Lock()
	c.history = append(c.history, assistant)
	c.historyMu.Unlock()
}

func sanitizeMessage(role, content string) (Message, error) {
	role = strings.TrimSpace(role)
	if role == "" {
		role = "user"
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return Message{}, fmt.Errorf("prompt is required")
	}
	return Message{Role: role, Content: content}, nil
}

func messagesToContents(history []Message) []content {
	contents := make([]content, 0, len(history))
	for _, msg := range history {
		role := normalizeAPIRole(msg.Role)
		text := strings.TrimSpace(msg.Content)
		if text == "" {
			continue
		}
		contents = append(contents, content{Role: role, Parts: []part{{Text: text}}})
	}
	return contents
}

// Gemini currently allows only "user" and "model" roles. This helper maps stored
// roles (which also include "assistant" for Firebase compatibility) into the
// expected values.
func normalizeAPIRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "model", "assistant":
		return "model"
	case "user":
		return "user"
	default:
		return "user"
	}
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
