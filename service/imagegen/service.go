package imagegen

import (
	"context"
	"fmt"
	"strings"

	genai "google.golang.org/genai"
)

const defaultImageModel = "gemini-2.5-flash-image"

// Config configures how the Gemini image generation client behaves.
type Config struct {
	APIKey string
	Model  string
}

// Service wraps the Gemini client used for producing base portrait images.
type Service struct {
	client    *genai.Client
	modelName string
}

// New initializes the Service with the provided API key/model.
func New(ctx context.Context, cfg Config) (*Service, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("api key is required")
	}
	modelName := strings.TrimSpace(cfg.Model)
	if modelName == "" {
		modelName = defaultImageModel
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: apiKey})
	if err != nil {
		return nil, fmt.Errorf("init gemini client: %w", err)
	}
	return &Service{client: client, modelName: modelName}, nil
}

// Close releases underlying client resources.
func (s *Service) Close(ctx context.Context) error { return nil }

// GenerateImage produces an image and returns the raw bytes and mime type.
func (s *Service) GenerateImage(ctx context.Context, prompt string) ([]byte, string, error) {
	if s == nil || s.client == nil {
		return nil, "", fmt.Errorf("image client not initialized")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, "", fmt.Errorf("prompt is required")
	}
	resp, err := s.client.Models.GenerateContent(ctx, s.modelName, genai.Text(prompt), nil)
	if err != nil {
		return nil, "", fmt.Errorf("generate image: %w", err)
	}
	for _, cand := range resp.Candidates {
		if cand == nil || cand.Content == nil {
			continue
		}
		for _, part := range cand.Content.Parts {
			if part == nil || part.InlineData == nil || len(part.InlineData.Data) == 0 {
				continue
			}
			mime := strings.TrimSpace(part.InlineData.MIMEType)
			if mime == "" {
				mime = "image/png"
			}
			return part.InlineData.Data, mime, nil
		}
	}
	return nil, "", fmt.Errorf("gemini response missing image data")
}
