package llmservice

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

const defaultModel = "gemini-1.5-flash-latest"

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
	genClient *genai.Client
	model     *genai.GenerativeModel
	chat      *genai.ChatSession

	historyMu sync.RWMutex
	history   []Message
}

// NewClient validates the provided configuration and prepares a Client instance.
func NewClient(cfg Config) (*Client, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("api key is required")
	}
	modelName := strings.TrimSpace(cfg.Model)
	if modelName == "" {
		modelName = defaultModel
	}

	opts := []option.ClientOption{option.WithAPIKey(apiKey)}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}

	client, err := genai.NewClient(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("initialize gemini client: %w", err)
	}
	model := client.GenerativeModel(modelName)
	return &Client{
		genClient: client,
		model:     model,
		chat:      model.StartChat(),
	}, nil
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

	c.appendAndSnapshot(userMsg)
	resp, err := c.chat.SendMessage(ctx, genai.Text(userMsg.Content))
	if err != nil {
		return "", fmt.Errorf("google api error: %w", err)
	}
	if len(resp.Candidates) == 0 {
		return "", fmt.Errorf("google api returned no candidates")
	}
	for _, part := range resp.Candidates[0].Content.Parts {
		text := extractTextPart(part)
		if text == "" {
			continue
		}
		c.appendAssistantMessage(text)
		return text, nil
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
	if c.model != nil {
		c.chat = c.model.StartChat()
	}
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

func extractTextPart(part genai.Part) string {
	switch v := part.(type) {
	case genai.Text:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}
