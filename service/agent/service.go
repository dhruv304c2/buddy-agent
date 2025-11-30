package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"buddy-agent/service/dbservice"
	"go.mongodb.org/mongo-driver/bson"
)

const (
	envMongoDatabase   = "MONGO_DB_NAME"
	defaultMongoDBName = "buddy-agent"
	agentsCollection   = "agents"
	requestTimeout     = 5 * time.Second
)

// Agent represents the payload used to create a new agent profile.
type Agent struct {
	Name        string `json:"name"`
	Personality string `json:"personality"`
}

// Handler coordinates agent related HTTP handlers backed by MongoDB.
type Handler struct {
	db *dbservice.Service
}

// NewHandler initializes the Agent handler and underlying database connection.
func NewHandler(ctx context.Context) (*Handler, error) {
	svc, err := dbservice.New(ctx)
	if err != nil {
		return nil, err
	}
	return &Handler{db: svc}, nil
}

// Close releases the underlying database resources.
func (h *Handler) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	return h.db.Close(ctx)
}

// CreateAgent handles POST requests to create a new agent document.
func (h *Handler) CreateAgent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload Agent
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("invalid json: %v", err), http.StatusBadRequest)
		return
	}
	payload.Name = strings.TrimSpace(payload.Name)
	payload.Personality = strings.TrimSpace(payload.Personality)
	if payload.Name == "" || payload.Personality == "" {
		http.Error(w, "name and personality are required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	collection := h.db.Client().Database(mongoDatabaseName()).Collection(agentsCollection)
	doc := bson.M{
		"name":        payload.Name,
		"personality": payload.Personality,
		"created_at":  time.Now().UTC(),
	}
	result, err := collection.InsertOne(ctx, doc)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create agent: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":          result.InsertedID,
		"name":        payload.Name,
		"personality": payload.Personality,
	})
}

func mongoDatabaseName() string {
	if name := strings.TrimSpace(os.Getenv(envMongoDatabase)); name != "" {
		return name
	}
	return defaultMongoDBName
}
