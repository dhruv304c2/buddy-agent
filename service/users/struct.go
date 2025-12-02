package users

import (
	"time"

	"buddy-agent/service/dbservice"
	"firebase.google.com/go/v4/auth"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// UserHandler manages Firebase-authenticated user endpoints backed by MongoDB.
type UserHandler struct {
	db   *dbservice.Service
	auth *auth.Client
}

// User captures Firebase-authenticated visitors persisted in MongoDB.
type User struct {
	ID          primitive.ObjectID `json:"id,omitempty" bson:"_id,omitempty"`
	UID         string             `json:"uid" bson:"uid"`
	Email       string             `json:"email,omitempty" bson:"email,omitempty"`
	DisplayName string             `json:"display_name,omitempty" bson:"display_name,omitempty"`
	PhotoURL    string             `json:"photo_url,omitempty" bson:"photo_url,omitempty"`
	CreatedAt   time.Time          `json:"created_at" bson:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at" bson:"updated_at"`
	LastLoginAt time.Time          `json:"last_login_at" bson:"last_login_at"`
}

// loginRequest represents the body for the login endpoint.
type loginRequest struct {
	Token string `json:"token"`
}
