package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"buddy-agent/service/agent"
)

// Config controls how the HTTP service listener behaves.
type Config struct {
	Addr string
}

// Run starts the HTTP service listener until the provided context is canceled.
func Run(ctx context.Context, cfg Config) error {
	if ctx == nil {
		ctx = context.Background()
	}
	addr := strings.TrimSpace(cfg.Addr)
	if addr == "" {
		addr = ":8080"
	}

	agentHandler, err := agent.NewHandler(ctx)
	if err != nil {
		return fmt.Errorf("init agent handler: %w", err)
	}
	defer agentHandler.Close(context.Background())

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "buddy-agent service online")
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/create/agent/v1", agentHandler.CreateAgent)
	mux.HandleFunc("/agent/chat/agentid", agentHandler.ChatWithAgent)

	srv := &http.Server{Addr: addr, Handler: mux}
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil

	case err := <-errCh:
		return err
	}
}
