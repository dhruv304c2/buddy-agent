package dbservice

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInsertDocument(t *testing.T) {
	loadEnvFile(t, ".env")
	username := strings.TrimSpace(os.Getenv(envMongoUsername))
	password := strings.TrimSpace(os.Getenv(envMongoPassword))
	if username == "" || password == "" {
		t.Fatalf("%s and %s must be set in environment or .env", envMongoUsername, envMongoPassword)
	}

	svc, err := New(context.Background())
	if err != nil {
		t.Fatalf("failed to create mongo service: %v", err)
	}
	t.Cleanup(func() {
		_ = svc.Close(context.Background())
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	collection := svc.Client().Database("buddy-agent-test").Collection("connectivity-checks")
	doc := map[string]any{
		"timestamp": time.Now().UTC(),
		"status":    "ok",
		"note":      "integration-test",
	}
	if _, err := collection.InsertOne(ctx, doc); err != nil {
		t.Fatalf("failed to insert test document: %v", err)
	}
}

func loadEnvFile(t *testing.T, name string) {
	t.Helper()
	path, err := findFileUpwards(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		t.Fatalf("locate %s: %v", name, err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
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
			t.Fatalf("invalid line %d in %s: %s", lineNo, path, line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, "\"")
		value = strings.Trim(value, "'")
		if err := os.Setenv(key, value); err != nil {
			t.Fatalf("set env from %s: %v", path, err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
}

func findFileUpwards(name string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	prev := ""
	for dir := wd; dir != prev; dir, prev = filepath.Dir(dir), dir {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return "", os.ErrNotExist
}
