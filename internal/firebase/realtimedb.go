package firebase

import (
	"context"
	"fmt"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/db"
	"google.golang.org/api/option"
)

// NewRealtimeDBClient creates a Firebase Realtime Database client configured with the
// provided databaseURL. Additional firebase App options can be supplied via opts.
func NewRealtimeDBClient(ctx context.Context, databaseURL string, opts ...option.ClientOption) (*db.Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if databaseURL == "" {
		return nil, fmt.Errorf("databaseURL is required")
	}

	app, err := firebase.NewApp(ctx, &firebase.Config{DatabaseURL: databaseURL}, opts...)
	if err != nil {
		return nil, fmt.Errorf("init firebase app: %w", err)
	}

	client, err := app.Database(ctx)
	if err != nil {
		return nil, fmt.Errorf("init realtime db client: %w", err)
	}

	return client, nil
}
