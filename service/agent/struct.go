package agent

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// AgentSocialProfile represents the social presence for an agent that lives
// separately from the agent profile itself.
type AgentSocialProfile struct {
	ID         primitive.ObjectID `json:"id,omitempty" bson:"_id,omitempty"`
	AgentID    primitive.ObjectID `json:"agent_id" bson:"agent_id"`
	Username   string             `json:"username" bson:"username"`
	Status     string             `json:"status" bson:"status"`
	ProfileURL string             `json:"profile_url" bson:"profile_url"`
	CreatedAt  time.Time          `json:"created_at" bson:"created_at"`
	UpdatedAt  time.Time          `json:"updated_at" bson:"updated_at"`
}
