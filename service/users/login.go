package users

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Login verifies the Firebase ID token and upserts the user document.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h == nil || h.auth == nil || h.db == nil {
		respondJSONError(w, http.StatusInternalServerError, "service unavailable")
		return
	}

	var req loginRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		respondJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid json: %v", err))
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" {
		respondJSONError(w, http.StatusBadRequest, "token is required")
		return
	}

	ctx := r.Context()
	verified, err := h.auth.VerifyIDToken(ctx, req.Token)
	if err != nil {
		respondJSONError(w, http.StatusUnauthorized, "invalid firebase token")
		return
	}
	userRecord, err := h.auth.GetUser(ctx, verified.UID)
	if err != nil {
		respondJSONError(w, http.StatusBadGateway, fmt.Sprintf("failed to load firebase user: %v", err))
		return
	}

	dbCtx, cancel := context.WithTimeout(ctx, dbRequestTimeout)
	defer cancel()
	collection := h.db.Client().Database(mongoDatabaseName()).Collection(usersCollection)

	now := time.Now().UTC()
	filter := bson.M{"uid": userRecord.UID}
	setFields := bson.M{
		"email":         strings.TrimSpace(userRecord.Email),
		"display_name":  strings.TrimSpace(userRecord.DisplayName),
		"photo_url":     strings.TrimSpace(userRecord.PhotoURL),
		"updated_at":    now,
		"last_login_at": now,
	}
	setOnInsert := bson.M{
		"uid":        userRecord.UID,
		"created_at": now,
	}
	update := bson.M{
		"$set":         setFields,
		"$setOnInsert": setOnInsert,
	}
	opts := options.Update().SetUpsert(true)
	result, err := collection.UpdateOne(dbCtx, filter, update, opts)
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to persist user: %v", err))
		return
	}

	var stored User
	if err := collection.FindOne(dbCtx, filter).Decode(&stored); err != nil {
		respondJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to load user: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"user":   stored,
		"is_new": result.UpsertedCount > 0,
	})
}
