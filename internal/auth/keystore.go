// Package auth provides API key management with file-based storage and hot-reload.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Key represents a single API key configuration.
type Key struct {
	ID         string            `json:"id"`
	Provider   string            `json:"provider"`
	Key        string            `json:"key"` // raw key for forwarding
	KeyHash    string            `json:"-"`   // SHA-256 hash, not serialized
	Tier       string            `json:"tier"`
	Limits     RateLimits        `json:"limits"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Endpoint   string            `json:"endpoint,omitempty"`
	APIVersion string            `json:"api_version,omitempty"`
}

// RateLimits defines rate limiting constraints for a key.
type RateLimits struct {
	RPM   int64 `json:"rpm"`
	TPM   int64 `json:"tpm"`
	Daily int64 `json:"daily"`
}

// KeysFile represents the keys.json file structure.
type KeysFile struct {
	Keys []Key `json:"keys"`
}

// KeyStore manages API keys with hot-reload support.
type KeyStore struct {
	mu         sync.RWMutex
	keysByHash map[string]*Key // keyed by hash(rawKey)
	keysByID   map[string]*Key // keyed by key ID
	path       string
	watch      *fsnotify.Watcher
	graceUntil time.Time // keys remain valid during rotation grace period
}

// NewKeyStore creates a new key store.
func NewKeyStore(path string) *KeyStore {
	return &KeyStore{
		keysByHash: make(map[string]*Key),
		keysByID:   make(map[string]*Key),
		path:       path,
	}
}

// Load reads keys from file and populates the in-memory store.
func (s *KeyStore) Load(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("failed to read keys file: %w", err)
	}

	var kf KeysFile
	if err := json.Unmarshal(data, &kf); err != nil {
		return fmt.Errorf("failed to parse keys file: %w", err)
	}

	// Clear existing keys
	s.keysByHash = make(map[string]*Key)
	s.keysByID = make(map[string]*Key)

	// Populate maps
	for i := range kf.Keys {
		key := &kf.Keys[i]
		key.KeyHash = hashKey(key.Key)

		// Check grace period for existing keys
		if s.isInGracePeriod(key) {
			// Add with old hash too
			for oldHash := range s.keysByHash {
				if s.keysByHash[oldHash] != nil && s.keysByHash[oldHash].ID == key.ID {
					delete(s.keysByHash, oldHash)
				}
			}
		}

		s.keysByHash[key.KeyHash] = key
		s.keysByID[key.ID] = key
	}

	return nil
}

// Get resolves a key by raw value or by key ID.
func (s *KeyStore) Get(rawKey string) (*Key, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Try hash lookup first
	hash := hashKey(rawKey)
	if key, ok := s.keysByHash[hash]; ok {
		return key, nil
	}

	// Try ID lookup
	if key, ok := s.keysByID[rawKey]; ok {
		return key, nil
	}

	return nil, fmt.Errorf("key not found: %s", maskKey(rawKey))
}

// Watch starts watching the keys file for changes.
func (s *KeyStore) Watch(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create watcher: %w", err)
	}
	s.watch = watcher

	// Watch the directory (we'll handle file events)
	dir := s.path
	if idx := len(s.path) - 1; idx >= 0 {
		for i := len(s.path) - 1; i >= 0; i-- {
			if s.path[i] == '/' {
				dir = s.path[:i]
				break
			}
		}
	}
	if err := watcher.Add(dir); err != nil {
		return fmt.Errorf("failed to watch directory: %w", err)
	}

	go s.watchLoop(ctx)
	return nil
}

// watchLoop handles file system events.
func (s *KeyStore) watchLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-s.watch.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				if event.Name == s.path {
					if err := s.Load(ctx); err != nil {
						fmt.Printf("error reloading keys: %v\n", err)
					}
				}
			}
		case err, ok := <-s.watch.Errors:
			if !ok {
				return
			}
			fmt.Printf("watcher error: %v\n", err)
		}
	}
}

// Close stops the file watcher.
func (s *KeyStore) Close() error {
	if s.watch != nil {
		return s.watch.Close()
	}
	return nil
}

// isInGracePeriod checks if a key is within the rotation grace period.
func (s *KeyStore) isInGracePeriod(key *Key) bool {
	if s.graceUntil.IsZero() {
		return false
	}
	return time.Now().Before(s.graceUntil)
}

// SetGracePeriod sets the rotation grace period.
func (s *KeyStore) SetGracePeriod(d time.Duration) {
	s.graceUntil = time.Now().Add(d)
}

// hashKey computes SHA-256 hash of a key.
func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// maskKey returns a masked version of the key for logging.
func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

// ValidateKey validates an Authorization header value.
func ValidateKey(rawKey string) bool {
	// Remove "Bearer " prefix if present
	if len(rawKey) > 7 && rawKey[:7] == "Bearer " {
		rawKey = rawKey[7:]
	}
	return len(rawKey) > 0
}

// ExtractBearerToken extracts the raw key from a Bearer token.
func ExtractBearerToken(authHeader string) string {
	if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
		return authHeader[7:]
	}
	return authHeader
}
