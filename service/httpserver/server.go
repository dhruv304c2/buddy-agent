package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"buddy-agent/service/agent"
)

const apiVersionPrefix = "/api/v1"

func apiVersionPath(path string) string {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return apiVersionPrefix
	}
	return apiVersionPrefix + "/" + path
}

func servicePort() string {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		return "3000"
	}
	return port
}

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
		addr = fmt.Sprintf(":%s", servicePort())
	}

	agentHandler, err := agent.NewHandler(ctx)
	if err != nil {
		return fmt.Errorf("init agent handler: %w", err)
	}
	defer agentHandler.Close(context.Background())

	mux := http.NewServeMux()
	mux.HandleFunc(apiVersionPath(""), func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "buddy-agent service online")
	})
	mux.HandleFunc(apiVersionPath("/healthz"), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	mux.HandleFunc(apiVersionPath("/create/agent"), agentHandler.CreateAgent)
	mux.HandleFunc(apiVersionPath("/agents"), agentHandler.ListAgents)
	mux.HandleFunc(apiVersionPath("/agent/chat/agentid"), agentHandler.ChatWithAgent)
	mux.HandleFunc(apiVersionPath("/agent/social-profile"), agentHandler.GetAgentSocialProfile)
	mux.HandleFunc(apiVersionPath("/agent/social-profiles"), agentHandler.ListAgentSocialProfiles)

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
