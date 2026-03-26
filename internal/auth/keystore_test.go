package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestKeyStore_Get(t *testing.T) {
	// Create temp keys file
	dir := t.TempDir()
	keysPath := filepath.Join(dir, "keys.json")
	
	keysJSON := `{
		"keys": [
			{
				"id": "key-test-1",
				"provider": "openai",
				"key": "sk-test-key-123",
				"tier": "standard",
				"limits": {"rpm": 500, "tpm": 300000, "daily": 50000},
				"metadata": {"team": "test"}
			}
		]
	}`
	if err := os.WriteFile(keysPath, []byte(keysJSON), 0600); err != nil {
		t.Fatalf("failed to write keys file: %v", err)
	}

	store := NewKeyStore(keysPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := store.Load(ctx); err != nil {
		t.Fatalf("failed to load keys: %v", err)
	}

	// Test by raw key
	key, err := store.Get("sk-test-key-123")
	if err != nil {
		t.Fatalf("failed to get key by raw value: %v", err)
	}
	if key.ID != "key-test-1" {
		t.Errorf("expected id 'key-test-1', got '%s'", key.ID)
	}
	if key.Tier != "standard" {
		t.Errorf("expected tier 'standard', got '%s'", key.Tier)
	}
	if key.Provider != "openai" {
		t.Errorf("expected provider 'openai', got '%s'", key.Provider)
	}
}

func TestKeyStore_GetByID(t *testing.T) {
	dir := t.TempDir()
	keysPath := filepath.Join(dir, "keys.json")
	
	keysJSON := `{
		"keys": [
			{
				"id": "key-alias-1",
				"provider": "anthropic",
				"key": "sk-ant-test",
				"tier": "pro",
				"limits": {"rpm": 1000, "tpm": 600000, "daily": 100000}
			}
		]
	}`
	if err := os.WriteFile(keysPath, []byte(keysJSON), 0600); err != nil {
		t.Fatalf("failed to write keys file: %v", err)
	}

	store := NewKeyStore(keysPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = store.Load(ctx)

	// Test by key ID alias
	key, err := store.Get("key-alias-1")
	if err != nil {
		t.Fatalf("failed to get key by ID: %v", err)
	}
	if key.Provider != "anthropic" {
		t.Errorf("expected provider 'anthropic', got '%s'", key.Provider)
	}
}

func TestKeyStore_Get_NotFound(t *testing.T) {
	dir := t.TempDir()
	keysPath := filepath.Join(dir, "keys.json")
	
	keysJSON := `{"keys": []}`
	if err := os.WriteFile(keysPath, []byte(keysJSON), 0600); err != nil {
		t.Fatalf("failed to write keys file: %v", err)
	}

	store := NewKeyStore(keysPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = store.Load(ctx)

	_, err := store.Get("nonexistent-key")
	if err == nil {
		t.Error("expected error for nonexistent key, got nil")
	}
}

func TestKeyStore_Limits(t *testing.T) {
	dir := t.TempDir()
	keysPath := filepath.Join(dir, "keys.json")
	
	keysJSON := `{
		"keys": [
			{
				"id": "key-limits",
				"provider": "openai",
				"key": "sk-limits-test",
				"tier": "enterprise",
				"limits": {"rpm": 9999, "tpm": 9999999, "daily": 999999}
			}
		]
	}`
	if err := os.WriteFile(keysPath, []byte(keysJSON), 0600); err != nil {
		t.Fatalf("failed to write keys file: %v", err)
	}

	store := NewKeyStore(keysPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = store.Load(ctx)

	key, err := store.Get("sk-limits-test")
	if err != nil {
		t.Fatalf("failed to get key: %v", err)
	}

	if key.Limits.RPM != 9999 {
		t.Errorf("expected RPM 9999, got %d", key.Limits.RPM)
	}
	if key.Limits.TPM != 9999999 {
		t.Errorf("expected TPM 9999999, got %d", key.Limits.TPM)
	}
	if key.Limits.Daily != 999999 {
		t.Errorf("expected Daily 999999, got %d", key.Limits.Daily)
	}
}
