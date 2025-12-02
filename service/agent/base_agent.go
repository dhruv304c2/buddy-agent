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

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// CreateAgent handles POST requests to create a new agent document.
func (h *AgentHandler) CreateAgent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	creator, ok := h.requireUser(w, r)
	if !ok {
		return
	}

	var payload Agent
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		respondJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid json: %v", err))
		return
	}
	payload.Name = strings.TrimSpace(payload.Name)
	payload.Personality = strings.TrimSpace(payload.Personality)
	payload.Gender = strings.TrimSpace(payload.Gender)
	if payload.Name == "" || payload.Personality == "" || payload.Gender == "" {
		respondJSONError(w, http.StatusBadRequest, "name, personality, and gender are required")
		return
	}

	payload.SystemPrompt = buildSystemPrompt(payload.Name, payload.Personality, payload.Gender)
	appearanceDescription, err := h.generateAppearanceDescription(r.Context(), payload)
	if err != nil {
		respondJSONError(w, http.StatusBadGateway, fmt.Sprintf("failed to generate appearance: %v", err))
		return
	}
	agentID := primitive.NewObjectID()
	dbCtx, dbCancel := context.WithTimeout(r.Context(), dbRequestTimeout)
	defer dbCancel()

	collection := h.db.Client().Database(mongoDatabaseName()).Collection(agentsCollection)
	doc := bson.M{
		"_id":                           agentID,
		"name":                          payload.Name,
		"personality":                   payload.Personality,
		"gender":                        payload.Gender,
		"system_prompt":                 payload.SystemPrompt,
		"appearance_description":        appearanceDescription,
		"base_appearance_referance_url": "",
		"created_by":                    creator.ID,
		"created_at":                    time.Now().UTC(),
	}
	if _, err := collection.InsertOne(dbCtx, doc); err != nil {
		respondJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create agent: %v", err))
		return
	}
	cleanupAgent := func(reason string) {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), dbRequestTimeout)
		defer cleanupCancel()
		if _, err := collection.DeleteOne(cleanupCtx, bson.M{"_id": agentID}); err != nil {
			log.Printf("cleanup agent %s after %s failed: %v", agentID.Hex(), reason, err)
		}
	}
	baseImageURL, err := h.generateAndPersistBaseAppearance(r.Context(), agentID)
	if err != nil {
		cleanupAgent("base-appearance generation")
		respondJSONError(w, http.StatusBadGateway, fmt.Sprintf("failed to generate base appearance: %v", err))
		return
	}
	if err := h.createInitialSocialProfile(r.Context(), agentID, payload.Name, creator.ID); err != nil {
		cleanupAgent("social-profile placeholder")
		respondJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create social profile: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":                            agentID,
		"name":                          payload.Name,
		"personality":                   payload.Personality,
		"gender":                        payload.Gender,
		"profile_image_url":             baseImageURL,
		"appearance_description":        appearanceDescription,
		"base_appearance_referance_url": baseImageURL,
	})
	h.launchSocialProfileJob(agentID)
}

// ListAgents exposes all stored agents without revealing their system prompts.
func (h *AgentHandler) ListAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	dbCtx, dbCancel := context.WithTimeout(r.Context(), dbRequestTimeout)
	defer dbCancel()

	collection := h.db.Client().Database(mongoDatabaseName()).Collection(agentsCollection)
	cursor, err := collection.Find(dbCtx, bson.D{})
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to fetch agents: %v", err))
		return
	}
	defer cursor.Close(dbCtx)

	var stored []Agent
	if err := cursor.All(dbCtx, &stored); err != nil {
		respondJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to load agents: %v", err))
		return
	}

	items := make([]agentListItem, 0, len(stored))
	for _, a := range stored {
		items = append(items, agentListItem{
			ID:                         a.ID,
			Name:                       a.Name,
			Personality:                a.Personality,
			Gender:                     a.Gender,
			ProfileImageURL:            a.ProfileImageURL,
			AppearanceDescription:      a.AppearanceDescription,
			BaseAppearanceReferenceURL: a.BaseAppearanceReferenceURL,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"agents": items}); err != nil {
		respondJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to encode response: %v", err))
	}
}

// ChatWithAgent receives a prompt for an existing agent and forwards it to the LLM.
func (h *AgentHandler) ChatWithAgent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	agentIDHex := strings.TrimSpace(r.URL.Query().Get("agentId"))
	if agentIDHex == "" {
		respondJSONError(w, http.StatusBadRequest, "agentId is required")
		return
	}
	agentID, err := primitive.ObjectIDFromHex(agentIDHex)
	if err != nil {
		respondJSONError(w, http.StatusBadRequest, "invalid agentId")
		return
	}

	var req chatRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		respondJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid json: %v", err))
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		respondJSONError(w, http.StatusBadRequest, "prompt is required")
		return
	}

	dbCtx, dbCancel := context.WithTimeout(r.Context(), dbRequestTimeout)
	defer dbCancel()
	collection := h.db.Client().Database(mongoDatabaseName()).Collection(agentsCollection)
	var stored Agent
	if err := collection.FindOne(dbCtx, bson.M{"_id": agentID}).Decode(&stored); err != nil {
		status := http.StatusInternalServerError
		msg := fmt.Sprintf("failed to load agent: %v", err)
		if errors.Is(err, mongo.ErrNoDocuments) {
			status = http.StatusNotFound
			msg = "agent not found"
		}
		respondJSONError(w, status, msg)
		return
	}

	combinedPrompt := buildChatPrompt(stored.SystemPrompt, req.Prompt)
	llmCtx, llmCancel := context.WithTimeout(r.Context(), llmRequestTimeout)
	defer llmCancel()

	response, err := h.llm.SendPrompt(llmCtx, "user", combinedPrompt)
	if err != nil {
		respondJSONError(w, http.StatusBadGateway, fmt.Sprintf("failed to fetch response: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"agent_id": agentIDHex,
		"response": response,
	}); err != nil {
		respondJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to encode response: %v", err))
	}
}

func (h *AgentHandler) generateAppearanceDescription(ctx context.Context, agent Agent) (string, error) {
	if h == nil || h.llm == nil {
		return "", fmt.Errorf("llm client not initialized")
	}
	llmCtx, cancel := context.WithTimeout(ctx, llmRequestTimeout)
	defer cancel()

	prompt := buildAppearancePrompt(agent.Name, agent.Personality, agent.Gender)
	description, err := h.llm.SendPrompt(llmCtx, "user", prompt)
	if err != nil {
		return "", fmt.Errorf("appearance prompt error: %w", err)
	}
	return strings.TrimSpace(description), nil
}

func buildAppearancePrompt(name, personality, gender string) string {
	return strings.TrimSpace(fmt.Sprintf(
		`
            You are crafting a short appearance description for a photorealistic portrait of %s.
            The companion should feel like a real human. Describe their physical features, style, and outfit informed by this personality: %s, and gender identity: %s.
            Focus on visual cues only in 1-2 sentences.
        `,
		name,
		personality,
		gender,
	))
}

func buildSystemPrompt(name, personality, gender string) string {
	return strings.TrimSpace(fmt.Sprintf(
		`You are %s, a %s personality presenting as %s. Answer warmly and stay in character.`,
		name,
		personality,
		gender,
	))
}

func buildChatPrompt(systemPrompt, userPrompt string) string {
	return strings.TrimSpace(fmt.Sprintf("%s\n\nUser: %s", systemPrompt, userPrompt))
}

func buildBaseImagePrompt(name, personality, gender, appearanceDescription string) string {
	return strings.TrimSpace(fmt.Sprintf(
		`
            Create a front-facing, softly lit portrait of %s.
            Appearance details: %s.
            Personality cues: %s. 
            Gender identity: %s.
            Keep the pose relaxed, shoulders square, and expression gentle with a subtle smile so the image can be reused for future generations.
        `,
		name,
		appearanceDescription,
		personality,
		gender,
	))
}

func (h *AgentHandler) generateAndPersistBaseAppearance(ctx context.Context, agentID primitive.ObjectID) (string, error) {
	if h == nil {
		return "", fmt.Errorf("handler not initialized")
	}
	if h.imageGen == nil || h.storage == nil {
		return "", fmt.Errorf("image generation dependencies missing")
	}
	collection := h.db.Client().Database(mongoDatabaseName()).Collection(agentsCollection)
	dbCtx, dbCancel := context.WithTimeout(ctx, dbRequestTimeout)
	defer dbCancel()
	var stored Agent
	if err := collection.FindOne(dbCtx, bson.M{"_id": agentID}).Decode(&stored); err != nil {
		return "", fmt.Errorf("load agent for base image: %w", err)
	}
	prompt := buildBaseImagePrompt(stored.Name, stored.Personality, stored.Gender, stored.AppearanceDescription)
	imageBytes, mimeType, err := h.imageGen.GenerateImage(ctx, prompt)
	if err != nil {
		return "", err
	}
	objectName := fmt.Sprintf("%s-base", agentID.Hex())
	uploadCtx, uploadCancel := context.WithTimeout(ctx, imageRequestTimeout)
	defer uploadCancel()
	uri, err := h.storage.UploadImage(uploadCtx, objectName, mimeType, imageBytes)
	if err != nil {
		return "", err
	}
	update := bson.M{
		"$set": bson.M{
			"base_appearance_referance_url": uri,
		},
	}
	updateCtx, updateCancel := context.WithTimeout(ctx, dbRequestTimeout)
	defer updateCancel()
	if _, err := collection.UpdateByID(updateCtx, agentID, update); err != nil {
		return "", fmt.Errorf("update agent with base image: %w", err)
	}
	return uri, nil
}
