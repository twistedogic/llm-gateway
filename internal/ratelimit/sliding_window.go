// Package ratelimit provides per-key rate limiting using sliding window counters.
package ratelimit

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// SlidingWindowLimiter implements a sliding window rate limiter.
type SlidingWindowLimiter struct {
	mu        sync.Mutex
	windowSecs int64
	limit      int64
	buckets    []atomic.Int64
	lastUpdate int64 // Unix timestamp of last update
}

// NewSlidingWindowLimiter creates a new sliding window limiter.
func NewSlidingWindowLimiter(windowSecs, limit int64) *SlidingWindowLimiter {
	buckets := make([]atomic.Int64, windowSecs)
	return &SlidingWindowLimiter{
		windowSecs:  windowSecs,
		limit:       limit,
		buckets:     buckets,
		lastUpdate:  time.Now().Unix(),
	}
}

// Allow checks if a request is allowed under the rate limit.
func (l *SlidingWindowLimiter) Allow() (allowed bool, retryAfterSecs int64, currentCount int64) {
	now := time.Now().Unix()
	idx := now % l.windowSecs

	l.mu.Lock()
	defer l.mu.Unlock()

	// Check if we need to reset buckets (new window)
	lastUpdate := atomic.LoadInt64(&l.lastUpdate)
	if lastUpdate < now {
		// Reset all buckets for new window
		for i := int64(0); i < l.windowSecs; i++ {
			l.buckets[i].Store(0)
		}
		atomic.StoreInt64(&l.lastUpdate, now)
	}

	// Sum all buckets
	var total int64
	for i := int64(0); i < l.windowSecs; i++ {
		total += l.buckets[i].Load()
	}

	if total >= l.limit {
		// Calculate retry-after
		retryAfter := l.windowSecs - (now % l.windowSecs)
		return false, retryAfter, total
	}

	// Increment current bucket
	l.buckets[idx].Add(1)

	return true, 0, total + 1
}

// Usage returns the current usage for monitoring.
func (l *SlidingWindowLimiter) Usage() int64 {
	now := time.Now().Unix()

	// Check if we need to reset buckets
	lastUpdate := atomic.LoadInt64(&l.lastUpdate)
	if lastUpdate < now {
		return 0
	}

	var total int64
	for i := int64(0); i < l.windowSecs; i++ {
		total += l.buckets[i].Load()
	}
	return total
}

// Limit returns the configured limit.
func (l *SlidingWindowLimiter) Limit() int64 {
	return l.limit
}

// WindowSecs returns the window size in seconds.
func (l *SlidingWindowLimiter) WindowSecs() int64 {
	return l.windowSecs
}

// Manager manages multiple rate limiters by key.
type Manager struct {
	mu            sync.RWMutex
	limiters      map[string]*SlidingWindowLimiter
	defaultRPM    int64
	defaultTPM    int64
	defaultDaily  int64
	windowSecs    int64
}

// NewManager creates a new rate limit manager.
func NewManager(windowSecs, defaultRPM, defaultTPM, defaultDaily int64) *Manager {
	return &Manager{
		limiters:     make(map[string]*SlidingWindowLimiter),
		defaultRPM:   defaultRPM,
		defaultTPM:   defaultTPM,
		defaultDaily: defaultDaily,
		windowSecs:   windowSecs,
	}
}

// GetLimiter gets or creates a rate limiter for a key.
func (m *Manager) GetLimiter(keyID string, limits RateLimitConfig) *SlidingWindowLimiter {
	m.mu.RLock()
	limiter, exists := m.limiters[keyID]
	m.mu.RUnlock()

	if exists {
		return limiter
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if limiter, exists = m.limiters[keyID]; exists {
		return limiter
	}

	// Create new limiter with key-specific limits or defaults
	rpm := limits.RPM
	if rpm == 0 {
		rpm = m.defaultRPM
	}

	limiter = NewSlidingWindowLimiter(m.windowSecs, rpm)
	m.limiters[keyID] = limiter

	return limiter
}

// CheckRPM checks requests per minute limit.
func (m *Manager) CheckRPM(keyID string, limits RateLimitConfig) (bool, string, int64) {
	limiter := m.GetLimiter(keyID, limits)
	allowed, retryAfter, _ := limiter.Allow()
	return allowed, "rpm", retryAfter
}

// CheckTPM checks tokens per minute limit (separate limiter).
func (m *Manager) CheckTPM(keyID string, limits RateLimitConfig, tokenCount int64) (bool, int64, int64) {
	// For TPM, we need a token-aware limiter
	// This is a simplified version; full implementation would track token weights
	tpmKey := fmt.Sprintf("%s:tpm", keyID)

	m.mu.RLock()
	limiter, exists := m.limiters[tpmKey]
	m.mu.RUnlock()

	if !exists {
		m.mu.Lock()
		// Double-check
		if limiter, exists = m.limiters[tpmKey]; !exists {
			tpm := limits.TPM
			if tpm == 0 {
				tpm = m.defaultTPM
			}
			limiter = NewSlidingWindowLimiter(m.windowSecs, tpm)
			m.limiters[tpmKey] = limiter
		}
		m.mu.Unlock()
	}

	allowed, retryAfter, _ := limiter.Allow()
	return allowed, retryAfter, 0
}

// DailyLimiter manages daily limits (24-hour window).
type DailyLimiter struct {
	mu    sync.Mutex
	date  time.Time
	count int64
	limit int64
}

// NewDailyLimiter creates a new daily limiter.
func NewDailyLimiter(limit int64) *DailyLimiter {
	return &DailyLimiter{
		date:  time.Now().Truncate(24 * time.Hour),
		limit: limit,
	}
}

// Allow checks if a request is allowed for the day.
func (l *DailyLimiter) Allow() (bool, int64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	today := now.Truncate(24 * time.Hour)

	// Reset if new day
	if !today.Equal(l.date) {
		l.date = today
		l.count = 0
	}

	if l.count >= l.limit {
		// Calculate seconds until midnight
		midnight := today.Add(24 * time.Hour)
		retryAfter := int64(midnight.Sub(now).Seconds())
		return false, retryAfter
	}

	l.count++
	return true, 0
}

// RateLimitConfig holds rate limit configuration for a key.
type RateLimitConfig struct {
	RPM   int64
	TPM   int64
	Daily int64
}

// LimiterSet holds all rate limiters for a key.
type LimiterSet struct {
	RPM   *SlidingWindowLimiter
	TPM   *SlidingWindowLimiter
	Daily *DailyLimiter
}

// Check checks all limits and returns the first failure.
func (ls *LimiterSet) Check(tokenCount int64) (bool, string, int64) {
	if allowed, retry, _ := ls.RPM.Allow(); !allowed {
		return false, "rpm", retry
	}

	if allowed, retry, _ := ls.TPM.Allow(); !allowed {
		return false, "tpm", retry
	}

	if allowed, retry := ls.Daily.Allow(); !allowed {
		return false, "daily", retry
	}

	return true, "", 0
}
