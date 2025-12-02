package users

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// FetchUserByToken verifies a Firebase token and loads the associated Mongo user.
func (h *UserHandler) FetchUserByToken(ctx context.Context, token string) (*User, error) {
	if h == nil || h.auth == nil || h.db == nil {
		return nil, fmt.Errorf("users handler not initialized")
	}
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return nil, fmt.Errorf("token is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	verified, err := h.auth.VerifyIDToken(ctx, trimmed)
	if err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}

	dbCtx, cancel := context.WithTimeout(ctx, dbRequestTimeout)
	defer cancel()
	collection := h.db.Client().Database(mongoDatabaseName()).Collection(usersCollection)

	var stored User
	if err := collection.FindOne(dbCtx, bson.M{"uid": verified.UID}).Decode(&stored); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("load user: %w", err)
	}
	return &stored, nil
}
