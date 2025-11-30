package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"buddy-agent/cmd/chatcli"
)

func main() {
	if err := loadDotEnv(".env"); err != nil {
		log.Fatalf("load .env: %v", err)
	}
	ensureDefaultCredentials("google-services.json")

	chatMode := flag.Bool("chat", false, "Run the interactive chat CLI")
	apiKey := flag.String("api-key", os.Getenv("GOOGLE_API_KEY"), "Google API key for the Generative Language API (use GOOGLE_API_KEY)")
	model := flag.String("model", os.Getenv("GOOGLE_CHAT_MODEL"), "Google Generative Language model (default gemini-1.5-flash-latest)")
	role := flag.String("role", "user", "Role used for user prompts")
	timeout := flag.Duration("timeout", 2*time.Minute, "Per-request timeout")
	firebaseDBURL := flag.String("firebase-db-url", os.Getenv("FIREBASE_DATABASE_URL"), "Firebase Realtime Database URL (use FIREBASE_DATABASE_URL)")
	flag.Parse()

	if *chatMode {
		cfg := chatcli.Config{
			APIKey:              *apiKey,
			Model:               *model,
			Role:                *role,
			Timeout:             *timeout,
			FirebaseDatabaseURL: *firebaseDBURL,
		}

		if err := chatcli.Run(context.Background(), cfg); err != nil {
			log.Fatal(err)
		}
		return
	}

	fmt.Println("No mode selected. Run again with --chat to start the chat CLI.")
}

func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}

		key, value, found := strings.Cut(line, "=")
		if !found {
			return fmt.Errorf("invalid .env line %d: %s", lineNo, line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, "\"")
		value = strings.Trim(value, "'")

		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set env %s: %w", key, err)
		}
	}

	return scanner.Err()
}

func ensureDefaultCredentials(path string) {
	if strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")) != "" {
		return
	}
	if strings.TrimSpace(path) == "" {
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return
	}
	_ = os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", abs)
}
