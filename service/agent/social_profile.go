package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// GetAgentSocialProfile loads the generated social profile for a given agent or profile id.
func (h *Handler) GetAgentSocialProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	query := r.URL.Query()
	agentIDHex := strings.TrimSpace(query.Get("agentId"))
	profileIDHex := strings.TrimSpace(query.Get("profileId"))

	var filters []bson.M
	if agentIDHex != "" {
		agentID, err := primitive.ObjectIDFromHex(agentIDHex)
		if err != nil {
			respondJSONError(w, http.StatusBadRequest, "invalid agentId")
			return
		}
		filters = append(filters, bson.M{"agent_id": agentID})
	}
	if profileIDHex != "" {
		profileID, err := primitive.ObjectIDFromHex(profileIDHex)
		if err != nil {
			respondJSONError(w, http.StatusBadRequest, "invalid profileId")
			return
		}
		filters = append(filters, bson.M{"_id": profileID})
	}
	if len(filters) == 0 {
		respondJSONError(w, http.StatusBadRequest, "agentId or profileId is required")
		return
	}

	dbCtx, dbCancel := context.WithTimeout(r.Context(), dbRequestTimeout)
	defer dbCancel()
	collection := h.db.Client().Database(mongoDatabaseName()).Collection(socialProfileCollection)

	var profile AgentSocialProfile
	var lastErr error
	for _, filter := range filters {
		if err := collection.FindOne(dbCtx, filter).Decode(&profile); err != nil {
			lastErr = err
			if errors.Is(err, mongo.ErrNoDocuments) {
				continue
			}
			break
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		status := http.StatusInternalServerError
		msg := fmt.Sprintf("failed to load social profile: %v", lastErr)
		if errors.Is(lastErr, mongo.ErrNoDocuments) {
			status = http.StatusNotFound
			msg = "social profile not ready"
		}
		respondJSONError(w, status, msg)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(profile); err != nil {
		respondJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to encode response: %v", err))
	}
}

// ListAgentSocialProfiles returns every stored post-generation social profile document.
func (h *Handler) ListAgentSocialProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	dbCtx, dbCancel := context.WithTimeout(r.Context(), dbRequestTimeout)
	defer dbCancel()
	collection := h.db.Client().Database(mongoDatabaseName()).Collection(socialProfileCollection)
	cursor, err := collection.Find(dbCtx, bson.D{})
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to fetch social profiles: %v", err))
		return
	}
	defer cursor.Close(dbCtx)

	var profiles []AgentSocialProfile
	if err := cursor.All(dbCtx, &profiles); err != nil {
		respondJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to load social profiles: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"profiles": profiles}); err != nil {
		respondJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to encode response: %v", err))
	}
}

func (h *Handler) createInitialSocialProfile(ctx context.Context, agentID primitive.ObjectID, username string) error {
	if h == nil || h.db == nil {
		return fmt.Errorf("handler not initialized")
	}
	username = strings.TrimSpace(username)
	if username == "" {
		username = fmt.Sprintf("agent_%s", agentID.Hex())
	}
	profiles := h.db.Client().Database(mongoDatabaseName()).Collection(socialProfileCollection)
	dbCtx, dbCancel := context.WithTimeout(ctx, dbRequestTimeout)
	defer dbCancel()
	now := time.Now().UTC()
	update := bson.M{
		"$setOnInsert": bson.M{
			"agent_id":    agentID,
			"username":    username,
			"status":      "",
			"profile_url": "",
			"created_at":  now,
		},
		"$set": bson.M{
			"updated_at": now,
		},
	}
	opts := options.Update().SetUpsert(true)
	if _, err := profiles.UpdateOne(dbCtx, bson.M{"agent_id": agentID}, update, opts); err != nil {
		return fmt.Errorf("upsert initial social profile: %w", err)
	}
	return nil
}

func (h *Handler) launchSocialProfileJob(agentID primitive.ObjectID) {
	if h == nil {
		return
	}
	go func(id primitive.ObjectID) {
		ctx, cancel := context.WithTimeout(context.Background(), socialProfileJobTimeout)
		defer cancel()
		if err := h.generateAndPersistSocialProfile(ctx, id); err != nil {
			log.Printf("social profile generation failed for %s: %v", id.Hex(), err)
		}
	}(agentID)
}

func (h *Handler) generateAndPersistSocialProfile(ctx context.Context, agentID primitive.ObjectID) error {
	if h == nil {
		return fmt.Errorf("handler not initialized")
	}
	if h.db == nil || h.llm == nil || h.imageGen == nil || h.storage == nil {
		return fmt.Errorf("social profile dependencies missing")
	}
	agentCollection := h.db.Client().Database(mongoDatabaseName()).Collection(agentsCollection)
	dbCtx, dbCancel := context.WithTimeout(ctx, dbRequestTimeout)
	defer dbCancel()
	var stored Agent
	if err := agentCollection.FindOne(dbCtx, bson.M{"_id": agentID}).Decode(&stored); err != nil {
		return fmt.Errorf("load agent for social profile: %w", err)
	}
	username, err := h.generateSocialUsername(ctx, stored)
	if err != nil {
		return err
	}
	status, err := h.generateSocialStatus(ctx, stored)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	profiles := h.db.Client().Database(mongoDatabaseName()).Collection(socialProfileCollection)
	updateCtx, updateCancel := context.WithTimeout(ctx, dbRequestTimeout)
	defer updateCancel()
	update := bson.M{
		"$set": bson.M{
			"username":    username,
			"status":      status,
			"profile_url": stored.BaseAppearanceReferenceURL,
			"updated_at":  now,
		},
	}
	result, err := profiles.UpdateOne(updateCtx, bson.M{"agent_id": agentID}, update)
	if err != nil {
		return fmt.Errorf("update social profile: %w", err)
	}
	if result.MatchedCount == 0 {
		return fmt.Errorf("social profile placeholder missing for %s", agentID.Hex())
	}
	return nil
}

func (h *Handler) generateSocialUsername(ctx context.Context, agent Agent) (string, error) {
	if h == nil || h.llm == nil {
		return "", fmt.Errorf("llm client not initialized")
	}
	llmCtx, cancel := context.WithTimeout(ctx, llmRequestTimeout)
	defer cancel()
	prompt := buildSocialUsernamePrompt(agent.Name, agent.Personality)
	username, err := h.llm.SendPrompt(llmCtx, "user", prompt)
	if err != nil {
		return "", fmt.Errorf("social username prompt error: %w", err)
	}
	username = sanitizeUsername(username)
	if username == "" {
		username = fallbackSocialUsername(agent, agent.Name)
	}
	agentHandle := sanitizeUsername(agent.Name)
	if agentHandle != "" && username == agentHandle {
		username = fallbackSocialUsername(agent, username)
	}
	if username == "" {
		return "", fmt.Errorf("social username prompt returned empty response")
	}
	return username, nil
}

func buildSocialUsernamePrompt(name, personality string) string {
	return strings.TrimSpace(fmt.Sprintf(
		`
            Invent a short social-media style username for %s that feels modern and slightly playful.
            It must differ from the literal name and echo this personality: %s.
            Return only the username, under 20 characters, using letters, numbers, or underscores with no spaces.
        `,
		name,
		personality,
	))
}

func sanitizeUsername(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "@")
	text = strings.ReplaceAll(text, " ", "")
	if text == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range text {
		switch {
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '_' || r == '.':
			builder.WriteRune(r)
		default:
			lower := unicode.ToLower(r)
			if lower >= 'a' && lower <= 'z' {
				builder.WriteRune(lower)
			}
		}
	}
	username := strings.Trim(builder.String(), "._")
	if username == "" {
		return ""
	}
	runes := []rune(username)
	if len(runes) > maxSocialUsernameLength {
		username = string(runes[:maxSocialUsernameLength])
	}
	return username
}

func fallbackSocialUsername(agent Agent, seed string) string {
	base := sanitizeUsername(seed)
	if base == "" {
		base = sanitizeUsername(agent.Name)
	}
	if base == "" {
		base = "agent"
	}
	suffix := agent.ID.Hex()
	if suffix == "" {
		suffix = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	if len(suffix) > 4 {
		suffix = suffix[:4]
	}
	candidate := sanitizeUsername(fmt.Sprintf("%s_%s", base, suffix))
	if candidate == "" {
		candidate = sanitizeUsername(fmt.Sprintf("buddy_%s", suffix))
	}
	return candidate
}

func (h *Handler) generateSocialStatus(ctx context.Context, agent Agent) (string, error) {
	if h == nil || h.llm == nil {
		return "", fmt.Errorf("llm client not initialized")
	}
	llmCtx, cancel := context.WithTimeout(ctx, llmRequestTimeout)
	defer cancel()
	prompt := buildSocialStatusPrompt(agent.Name, agent.Personality)
	status, err := h.llm.SendPrompt(llmCtx, "user", prompt)
	if err != nil {
		return "", fmt.Errorf("social status prompt error: %w", err)
	}
	status = sanitizeStatus(status)
	if status == "" {
		return "", fmt.Errorf("social status prompt returned empty response")
	}
	return status, nil
}

func buildSocialStatusPrompt(name, personality string) string {
	return strings.TrimSpace(fmt.Sprintf(
		`
            Write a single-sentence social media status line for %s.
            Keep it upbeat, contemporary, and reflective of this personality: %s.
            The status should feel like a quick feed update, under 20 words, and avoid hashtags or emojis unless essential.
        `,
		name,
		personality,
	))
}

func sanitizeStatus(text string) string {
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.Join(strings.Fields(text), " ")
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) > 140 {
		text = strings.TrimSpace(string(runes[:140]))
	}
	return text
}
