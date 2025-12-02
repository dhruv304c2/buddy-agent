package users

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"buddy-agent/service/dbservice"
	firebase "firebase.google.com/go/v4"
)

const (
	envMongoDatabase   = "MONGO_DB_NAME"
	defaultMongoDBName = "buddy-agent"
	usersCollection    = "users"
	dbRequestTimeout   = 5 * time.Second
)

// NewHandler builds the users handler with Firebase Auth and Mongo dependencies.
func NewHandler(ctx context.Context) (*Handler, error) {
	svc, err := dbservice.New(ctx)
	if err != nil {
		return nil, err
	}
	app, err := firebase.NewApp(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("init firebase app: %w", err)
	}
	authClient, err := app.Auth(ctx)
	if err != nil {
		return nil, fmt.Errorf("init firebase auth: %w", err)
	}
	return &Handler{db: svc, auth: authClient}, nil
}

// Close releases resources held by the handler.
func (h *Handler) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if h.db == nil {
		return nil
	}
	return h.db.Close(ctx)
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
