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
	payload.Gender = strings.TrimSpace(payload.Gender)
	if payload.Name == "" || payload.Personality == "" || payload.Gender == "" {
		http.Error(w, "name, personality, and gender are required", http.StatusBadRequest)
		return
	}

	payload.SystemPrompt = buildSystemPrompt(payload.Name, payload.Personality, payload.Gender)
	appearanceDescription, err := h.generateAppearanceDescription(r.Context(), payload)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to generate appearance: %v", err), http.StatusBadGateway)
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
		http.Error(w, fmt.Sprintf("failed to create agent: %v", err), http.StatusInternalServerError)
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
		http.Error(w, fmt.Sprintf("failed to generate base appearance: %v", err), http.StatusBadGateway)
		return
	}
	if err := h.createInitialSocialProfile(r.Context(), agentID, payload.Name); err != nil {
		cleanupAgent("social-profile placeholder")
		http.Error(w, fmt.Sprintf("failed to create social profile: %v", err), http.StatusInternalServerError)
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
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	dbCtx, dbCancel := context.WithTimeout(r.Context(), dbRequestTimeout)
	defer dbCancel()

	collection := h.db.Client().Database(mongoDatabaseName()).Collection(agentsCollection)
	cursor, err := collection.Find(dbCtx, bson.D{})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to fetch agents: %v", err), http.StatusInternalServerError)
		return
	}
	defer cursor.Close(dbCtx)

	var stored []Agent
	if err := cursor.All(dbCtx, &stored); err != nil {
		http.Error(w, fmt.Sprintf("failed to load agents: %v", err), http.StatusInternalServerError)
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
		http.Error(w, fmt.Sprintf("failed to encode response: %v", err), http.StatusInternalServerError)
	}
}

// GetAgentSocialProfile loads the generated social profile for a given agent or profile id.
func (h *Handler) GetAgentSocialProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	query := r.URL.Query()
	agentIDHex := strings.TrimSpace(query.Get("agentId"))
	profileIDHex := strings.TrimSpace(query.Get("profileId"))

	var filters []bson.M
	if agentIDHex != "" {
		agentID, err := primitive.ObjectIDFromHex(agentIDHex)
		if err != nil {
			http.Error(w, "invalid agentId", http.StatusBadRequest)
			return
		}
		filters = append(filters, bson.M{"agent_id": agentID})
	}
	if profileIDHex != "" {
		profileID, err := primitive.ObjectIDFromHex(profileIDHex)
		if err != nil {
			http.Error(w, "invalid profileId", http.StatusBadRequest)
			return
		}
		filters = append(filters, bson.M{"_id": profileID})
	}
	if len(filters) == 0 {
		http.Error(w, "agentId or profileId is required", http.StatusBadRequest)
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
		http.Error(w, msg, status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(profile); err != nil {
		http.Error(w, fmt.Sprintf("failed to encode response: %v", err), http.StatusInternalServerError)
	}
}

// ListAgentSocialProfiles returns every stored social profile document.
func (h *Handler) ListAgentSocialProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	dbCtx, dbCancel := context.WithTimeout(r.Context(), dbRequestTimeout)
	defer dbCancel()
	collection := h.db.Client().Database(mongoDatabaseName()).Collection(socialProfileCollection)
	cursor, err := collection.Find(dbCtx, bson.D{})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to fetch social profiles: %v", err), http.StatusInternalServerError)
		return
	}
	defer cursor.Close(dbCtx)

	var profiles []AgentSocialProfile
	if err := cursor.All(dbCtx, &profiles); err != nil {
		http.Error(w, fmt.Sprintf("failed to load social profiles: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"profiles": profiles}); err != nil {
		http.Error(w, fmt.Sprintf("failed to encode response: %v", err), http.StatusInternalServerError)
	}
}

// ChatWithAgent receives a prompt for an existing agent, loads its system prompt, and
// forwards the combined input to the LLM before returning the assistant response.
func (h *Handler) ChatWithAgent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	agentIDHex := strings.TrimSpace(r.URL.Query().Get("agentId"))
	if agentIDHex == "" {
		http.Error(w, "agentId is required", http.StatusBadRequest)
		return
	}
	agentID, err := primitive.ObjectIDFromHex(agentIDHex)
	if err != nil {
		http.Error(w, "invalid agentId", http.StatusBadRequest)
		return
	}

	var req chatRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid json: %v", err), http.StatusBadRequest)
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
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
		http.Error(w, msg, status)
		return
	}

	combinedPrompt := buildChatPrompt(stored.SystemPrompt, req.Prompt)
	llmCtx, llmCancel := context.WithTimeout(r.Context(), llmRequestTimeout)
	defer llmCancel()

	response, err := h.llm.SendPrompt(llmCtx, "user", combinedPrompt)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to fetch response: %v", err), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"agent_id": agentIDHex,
		"response": response,
	}); err != nil {
		http.Error(w, fmt.Sprintf("failed to encode response: %v", err), http.StatusInternalServerError)
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

func mongoDatabaseName() string {
	if name := strings.TrimSpace(os.Getenv(envMongoDatabase)); name != "" {
		return name
	}
	return defaultMongoDBName
}
