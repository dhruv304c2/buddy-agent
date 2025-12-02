package agent

import (
	"time"

	"buddy-agent/service/dbservice"
	"buddy-agent/service/imagegen"
	"buddy-agent/service/llmservice"
	"buddy-agent/service/storage"
	userssvc "buddy-agent/service/users"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// AgentHandler coordinates agent related HTTP handlers backed by MongoDB and LLM.
type AgentHandler struct {
	db       *dbservice.Service
	llm      *llmservice.Client
	imageGen *imagegen.Service
	storage  *storage.Service
	users    *userssvc.UserHandler
}

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
	CreatedBy                  primitive.ObjectID `json:"created_by,omitempty" bson:"created_by,omitempty"`
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

// AgentSocialProfile represents the social presence for an agent that lives
// separately from the agent profile itself.
type AgentSocialProfile struct {
	ID         primitive.ObjectID `json:"id,omitempty" bson:"_id,omitempty"`
	AgentID    primitive.ObjectID `json:"agent_id" bson:"agent_id"`
	Username   string             `json:"username" bson:"username"`
	Status     string             `json:"status" bson:"status"`
	ProfileURL string             `json:"profile_url" bson:"profile_url"`
	CreatedBy  primitive.ObjectID `json:"created_by" bson:"created_by"`
	CreatedAt  time.Time          `json:"created_at" bson:"created_at"`
	UpdatedAt  time.Time          `json:"updated_at" bson:"updated_at"`
}
