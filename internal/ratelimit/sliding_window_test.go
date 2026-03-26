package ratelimit

import (
	"sync"
	"testing"
	"time"
)

func TestSlidingWindowLimiter_Allow(t *testing.T) {
	limiter := NewSlidingWindowLimiter(60, 10) // 10 req/min, 60 sec window

	// First 10 should be allowed
	for i := 0; i < 10; i++ {
		allowed, retry, _ := limiter.Allow()
		if !allowed {
			t.Errorf("request %d should be allowed", i+1)
		}
		if retry != 0 {
			t.Errorf("request %d should have retry=0, got %d", i+1, retry)
		}
	}

	// 11th should be denied
	allowed, retry, _ := limiter.Allow()
	if allowed {
		t.Error("11th request should be denied")
	}
	if retry == 0 {
		t.Error("11th request should have retry > 0")
	}
}

func TestSlidingWindowLimiter_UsageAndLimit(t *testing.T) {
	limiter := NewSlidingWindowLimiter(60, 50)

	// Use some requests
	for i := 0; i < 30; i++ {
		limiter.Allow()
	}

	if limiter.Usage() != 30 {
		t.Errorf("expected usage 30, got %d", limiter.Usage())
	}
	if limiter.Limit() != 50 {
		t.Errorf("expected limit 50, got %d", limiter.Limit())
	}
	if limiter.WindowSecs() != 60 {
		t.Errorf("expected window 60, got %d", limiter.WindowSecs())
	}
}

func TestSlidingWindowLimiter_Concurrent(t *testing.T) {
	limiter := NewSlidingWindowLimiter(60, 100) // 100 req/min

	var wg sync.WaitGroup
	var allowedCount int64
	var mu sync.Mutex

	// 200 concurrent requests
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed, _, _ := limiter.Allow()
			if allowed {
				mu.Lock()
				allowedCount++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if allowedCount != 100 {
		t.Errorf("expected 100 allowed, got %d", allowedCount)
	}
}

func TestSlidingWindowLimiter_WindowExpiry(t *testing.T) {
	// Use short window for testing
	limiter := NewSlidingWindowLimiter(1, 3) // 3 req/sec, 1 sec window

	// Exhaust limit
	for i := 0; i < 3; i++ {
		limiter.Allow()
	}

	// Should be denied
	allowed, _, _ := limiter.Allow()
	if allowed {
		t.Error("should be denied after exhaustion")
	}

	// Wait for window to expire
	time.Sleep(1100 * time.Millisecond)

	// Should be allowed again
	allowed, _, _ = limiter.Allow()
	if !allowed {
		t.Error("should be allowed after window expiry")
	}
}

func TestManager_GetLimiter(t *testing.T) {
	mgr := NewManager(60, 100, 5000, 10000)

	limiter := mgr.GetLimiter("new-key", RateLimitConfig{
		RPM:   200,
		TPM:   10000,
		Daily: 20000,
	})

	if limiter == nil {
		t.Fatal("expected non-nil limiter")
	}
	if limiter.Limit() != 200 {
		t.Errorf("expected RPM limit 200, got %d", limiter.Limit())
	}
}

func TestManager_CheckRPM(t *testing.T) {
	mgr := NewManager(60, 10, 1000, 10000)

	limits := RateLimitConfig{RPM: 5, TPM: 1000, Daily: 10000}

	// Exhaust RPM
	for i := 0; i < 5; i++ {
		allowed, _, _ := mgr.CheckRPM("rpm-test", limits)
		if !allowed {
			t.Errorf("request %d should be allowed", i+1)
		}
	}

	// RPM exhausted
	allowed, limitType, _ := mgr.CheckRPM("rpm-test", limits)
	if allowed {
		t.Error("should be rate limited")
	}
	if limitType != "rpm" {
		t.Errorf("expected limit type 'rpm', got '%s'", limitType)
	}
}

func TestLimiterSet_Check(t *testing.T) {
	ls := &LimiterSet{
		RPM:   NewSlidingWindowLimiter(60, 10),
		TPM:   NewSlidingWindowLimiter(60, 1000),
		Daily: NewDailyLimiter(100),
	}

	// Exceed RPM
	for i := 0; i < 10; i++ {
		ls.RPM.Allow()
	}

	allowed, limitType, _ := ls.Check(100)
	if allowed {
		t.Error("should be rate limited by RPM")
	}
	if limitType != "rpm" {
		t.Errorf("expected 'rpm', got '%s'", limitType)
	}
}

func TestDailyLimiter(t *testing.T) {
	limiter := NewDailyLimiter(100)

	// First 100 should be allowed
	for i := 0; i < 100; i++ {
		allowed, _ := limiter.Allow()
		if !allowed {
			t.Errorf("request %d should be allowed", i+1)
		}
	}

	// 101st should be denied
	allowed, _ := limiter.Allow()
	if allowed {
		t.Error("101st request should be denied")
	}
}
