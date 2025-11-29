package chatcli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"buddy-agent/chatclient"
	firebaseclient "buddy-agent/internal/firebase"
	"firebase.google.com/go/v4/db"
	"github.com/briandowns/spinner"
	"github.com/fatih/color"
)

const firebaseWriteTimeout = 5 * time.Second

// Config controls how the interactive chat CLI behaves.
type Config struct {
	BaseURL             string
	Role                string
	Timeout             time.Duration
	FirebaseDatabaseURL string
}

// Run launches the interactive chat CLI.
func Run(ctx context.Context, cfg Config) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if strings.TrimSpace(cfg.BaseURL) == "" {
		return fmt.Errorf("chat service base URL is required (use -base-url or CHAT_BASE_URL)")
	}
	if strings.TrimSpace(cfg.FirebaseDatabaseURL) == "" {
		return fmt.Errorf("firebase database URL is required (use -firebase-db-url or FIREBASE_DATABASE_URL)")
	}

	client, err := chatclient.NewClient(cfg.BaseURL, nil)
	if err != nil {
		return fmt.Errorf("configure chat client: %w", err)
	}
	fbClient, err := firebaseclient.NewRealtimeDBClient(ctx, cfg.FirebaseDatabaseURL)
	if err != nil {
		return fmt.Errorf("configure firebase client: %w", err)
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("Chat CLI ready. Type 'exit' to quit.")
	for {
		fmt.Printf("%s ", roleLabel("You:"))
		if !scanner.Scan() {
			break
		}
		prompt := strings.TrimSpace(scanner.Text())
		if prompt == "" {
			continue
		}
		if strings.EqualFold(prompt, "exit") || strings.EqualFold(prompt, "quit") {
			fmt.Println("Goodbye!")
			return nil
		}

		storeChatMessage(ctx, fbClient, chatclient.Message{Role: cfg.Role, Content: prompt})

		reqCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		stopLoader := startThinkingLoader()
		resp, err := client.SendPrompt(reqCtx, cfg.Role, prompt)
		cancel()
		stopLoader()
		if err != nil {
			fmt.Printf("%s error: %v\n", roleLabel("Assistant:"), err)
			continue
		}

		fmt.Printf("%s %s\n", roleLabel("Assistant:"), resp)
		storeChatMessage(ctx, fbClient, chatclient.Message{Role: "assistant", Content: resp})
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}

	return nil
}

// startThinkingLoader spins a nicer loader using github.com/briandowns/spinner until stopped.
func startThinkingLoader() func() {
	s := spinner.New(spinner.CharSets[14], 150*time.Millisecond)
	s.Prefix = " "
	s.Suffix = color.New(color.FgHiCyan).Sprint(" Thinking")
	s.Color("cyan")
	s.Start()
	return func() {
		s.Stop()
		fmt.Print("\r\033[K")
	}
}

func roleLabel(text string) string {
	return color.New(color.FgHiCyan).Sprint(text)
}

func storeChatMessage(ctx context.Context, dbClient *db.Client, msg chatclient.Message) {
	if dbClient == nil {
		return
	}
	safeMsg := chatclient.Message{
		Role:    strings.TrimSpace(msg.Role),
		Content: strings.TrimSpace(msg.Content),
	}
	if safeMsg.Role == "" || safeMsg.Content == "" {
		return
	}

	writeCtx, cancel := context.WithTimeout(ctx, firebaseWriteTimeout)
	defer cancel()
	ref := dbClient.NewRef("chats")
	if _, err := ref.Push(writeCtx, safeMsg); err != nil {
		fmt.Fprintf(os.Stderr, "failed to store chat message: %v\n", err)
	}
}
