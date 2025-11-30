package dbservice

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	envMongoUsername = "MONGO_DB_USERNAME"
	envMongoPassword = "MONGO_DB_PASSWORD"
	clusterURIFormat = "mongodb+srv://%s:%s@cluster0.2qidkde.mongodb.net/"
	connectTimeout   = 10 * time.Second
)

// Config configures how the MongoDB client is created.
type Config struct {
	Username string
	Password string
	URI      string
}

// Service provides access to the MongoDB client connection.
type Service struct {
	client *mongo.Client
}

func New(ctx context.Context) (*Service, error) {
	username := strings.TrimSpace(os.Getenv(envMongoUsername))
	password := strings.TrimSpace(os.Getenv(envMongoPassword))
	if username == "" {
		return nil, fmt.Errorf("%s is required", envMongoUsername)
	}
	if password == "" {
		return nil, fmt.Errorf("%s is required", envMongoPassword)
	}

	uri := fmt.Sprintf(clusterURIFormat, url.QueryEscape(username), url.QueryEscape(password))

	clientOpts := options.Client().ApplyURI(uri)
	clientOpts.SetServerAPIOptions(options.ServerAPI(options.ServerAPIVersion1))

	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	client, err := mongo.Connect(connectCtx, clientOpts)
	if err != nil {
		return nil, fmt.Errorf("connect to mongo: %w", err)
	}
	if err := client.Ping(connectCtx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("ping mongo: %w", err)
	}

	return &Service{client: client}, nil
}

// New creates a MongoDB client using credentials from the provided config or environment variables.
func NewWithConfig(ctx context.Context, cfg Config) (*Service, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	username := strings.TrimSpace(cfg.Username)
	if username == "" {
		username = strings.TrimSpace(os.Getenv(envMongoUsername))
	}
	password := strings.TrimSpace(cfg.Password)
	if password == "" {
		password = strings.TrimSpace(os.Getenv(envMongoPassword))
	}
	if username == "" {
		return nil, fmt.Errorf("%s is required", envMongoUsername)
	}
	if password == "" {
		return nil, fmt.Errorf("%s is required", envMongoPassword)
	}

	uri := strings.TrimSpace(cfg.URI)
	if uri == "" {
		uri = fmt.Sprintf(clusterURIFormat, url.QueryEscape(username), url.QueryEscape(password))
	}

	clientOpts := options.Client().ApplyURI(uri)
	clientOpts.SetServerAPIOptions(options.ServerAPI(options.ServerAPIVersion1))

	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	client, err := mongo.Connect(connectCtx, clientOpts)
	if err != nil {
		return nil, fmt.Errorf("connect to mongo: %w", err)
	}
	if err := client.Ping(connectCtx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("ping mongo: %w", err)
	}

	return &Service{client: client}, nil
}

// Client returns the underlying mongo.Client instance.
func (s *Service) Client() *mongo.Client {
	if s == nil {
		return nil
	}
	return s.client
}

// Close closes the MongoDB client connection.
func (s *Service) Close(ctx context.Context) error {
	if s == nil || s.client == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return s.client.Disconnect(ctx)
}
