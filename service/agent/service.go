package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"buddy-agent/service/dbservice"
	"buddy-agent/service/imagegen"
	"buddy-agent/service/llmservice"
	"buddy-agent/service/storage"
	userssvc "buddy-agent/service/users"
)

const (
	envMongoDatabase        = "MONGO_DB_NAME"
	envBaseFaceBucket       = "BASE_FACE_BUCKET"
	envBaseFacePrefix       = "BASE_FACE_PREFIX"
	envAWSRegion            = "AWS_REGION"
	envImageModel           = "GOOGLE_IMAGE_MODEL"
	defaultMongoDBName      = "buddy-agent"
	agentsCollection        = "agents"
	socialProfileCollection = "agent_social_profiles"
	dbRequestTimeout        = 5 * time.Second
	llmRequestTimeout       = 20 * time.Second
	imageRequestTimeout     = 60 * time.Second
	socialProfileJobTimeout = 90 * time.Second
	maxSocialUsernameLength = 20
)

// NewAgentHandler initializes the Agent handler and underlying dependencies.
func NewAgentHandler(ctx context.Context, usersHandler *userssvc.UserHandler) (*AgentHandler, error) {
	svc, err := dbservice.New(ctx)
	if err != nil {
		return nil, err
	}
	llmClient, err := llmservice.NewClient(llmservice.Config{
		APIKey: os.Getenv("GOOGLE_API_KEY"),
		Model:  os.Getenv("GOOGLE_CHAT_MODEL"),
	})
	if err != nil {
		return nil, fmt.Errorf("init llm client: %w", err)
	}
	imageClient, err := imagegen.New(ctx, imagegen.Config{
		APIKey: os.Getenv("GOOGLE_API_KEY"),
		Model:  os.Getenv(envImageModel),
	})
	if err != nil {
		return nil, fmt.Errorf("init image client: %w", err)
	}
	storageSvc, err := storage.New(ctx, storage.Config{
		Bucket: os.Getenv(envBaseFaceBucket),
		Prefix: os.Getenv(envBaseFacePrefix),
		Region: os.Getenv(envAWSRegion),
	})
	if err != nil {
		return nil, fmt.Errorf("init storage service: %w", err)
	}
	return &AgentHandler{db: svc, llm: llmClient, imageGen: imageClient, storage: storageSvc, users: usersHandler}, nil
}

// Close releases the underlying database resources.
func (h *AgentHandler) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	return errors.Join(
		h.db.Close(ctx),
		h.imageGen.Close(ctx),
	)
}

func mongoDatabaseName() string {
	if name := strings.TrimSpace(os.Getenv(envMongoDatabase)); name != "" {
		return name
	}
	return defaultMongoDBName
}

func respondJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
