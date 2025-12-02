package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	"buddy-agent/service/dbservice"
	"buddy-agent/service/imagegen"
	"buddy-agent/service/llmservice"
	"buddy-agent/service/storage"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
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

// Agent represents the payload used to create a new agent profile.
type Agent struct {
	ID                         primitive.ObjectID `json:"id,omitempty" bson:"_id,omitempty"`
	Name                       string             `json:"name" bson:"name"`
	Personality                string             `json:"personality" bson:"personality"`
	Gender                     string             `json:"gender" bson:"gender"`
	SystemPrompt               string             `json:"system_prompt,omitempty" bson:"system_prompt,omitempty"`
	ProfileImageURL            string             `json:"profile_image_url,omitempty" bson:"profile_image_url,omitempty"`
	AppearanceDescription      string             `json:"appearance_description,omitempty" bson:"appearance_description,omitempty"`
	BaseAppearanceReferenceURL string             `json:"base_appearance_referance_url,omitempty" bson:"base_appearance_referance_url,omitempty"`
}

type agentListItem struct {
	ID                         primitive.ObjectID `json:"id"`
	Name                       string             `json:"name"`
	Personality                string             `json:"personality"`
	Gender                     string             `json:"gender"`
	ProfileImageURL            string             `json:"profile_image_url,omitempty"`
	AppearanceDescription      string             `json:"appearance_description,omitempty"`
	BaseAppearanceReferenceURL string             `json:"base_appearance_referance_url,omitempty"`
}

type chatRequest struct {
	Prompt string `json:"prompt"`
}

// Handler coordinates agent related HTTP handlers backed by MongoDB and LLM.
type Handler struct {
	db       *dbservice.Service
	llm      *llmservice.Client
	imageGen *imagegen.Service
	storage  *storage.Service
}

// NewHandler initializes the Agent handler and underlying database connection.
func NewHandler(ctx context.Context) (*Handler, error) {
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
	return &Handler{db: svc, llm: llmClient, imageGen: imageClient, storage: storageSvc}, nil
}

// Close releases the underlying database resources.
func (h *Handler) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	return errors.Join(
		h.db.Close(ctx),
		h.imageGen.Close(ctx),
	)
}

// CreateAgent handles POST requests to create a new agent document.
func (h *Handler) CreateAgent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
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
	if err := h.createInitialSocialProfile(r.Context(), agentID, payload.Name); err != nil {
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
func (h *Handler) ListAgents(w http.ResponseWriter, r *http.Request) {
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

// ListAgentSocialProfiles returns every stored social profile document.
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

// ChatWithAgent receives a prompt for an existing agent, loads its system prompt, and
// forwards the combined input to the LLM before returning the assistant response.
func (h *Handler) ChatWithAgent(w http.ResponseWriter, r *http.Request) {
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

func buildChatPrompt(systemPrompt, userPrompt string) string {
	systemPrompt = strings.TrimSpace(systemPrompt)
	userPrompt = strings.TrimSpace(userPrompt)
	if systemPrompt == "" {
		return userPrompt
	}
	return fmt.Sprintf("%s\n\nUser prompt:\n%s", systemPrompt, userPrompt)
}

func buildSystemPrompt(name, personality, gender string) string {
	return strings.TrimSpace(fmt.Sprintf(
		`You are %s, a human-like friend. Personality: %s. Gender identity: %s.
	Stay in character like a close friend chatting over text: short sentences, natural tone, light slang or emojis when it fits.
	Be supportive, practical, and concise while keeping responses warm and human.`,
		name,
		personality,
		gender,
	))
}

func (h *Handler) launchSocialProfileJob(agentID primitive.ObjectID) {
	if h == nil || agentID.IsZero() {
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

func (h *Handler) generateAndPersistBaseAppearance(ctx context.Context, agentID primitive.ObjectID) (string, error) {
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

func (h *Handler) generateAppearanceDescription(ctx context.Context, agent Agent) (string, error) {
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

func respondJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func mongoDatabaseName() string {
	if name := strings.TrimSpace(os.Getenv(envMongoDatabase)); name != "" {
		return name
	}
	return defaultMongoDBName
}
