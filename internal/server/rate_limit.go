package server

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type userLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimiter provides per-user rate limiting using token bucket algorithm.
type RateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*userLimiter
	rpm      int
}

// NewRateLimiter creates a rate limiter with the given requests-per-minute limit.
func NewRateLimiter(rpm int) *RateLimiter {
	return &RateLimiter{
		limiters: make(map[string]*userLimiter),
		rpm:      rpm,
	}
}

// Allow checks if the user is within their rate limit.
func (rl *RateLimiter) Allow(userID string) bool {
	rl.mu.Lock()
	ul, ok := rl.limiters[userID]
	if !ok {
		// rate.Every(time.Minute / duration(rpm)) = one token every (60/rpm) seconds
		// burst = rpm (allow full minute burst)
		ul = &userLimiter{
			limiter: rate.NewLimiter(rate.Every(time.Minute/time.Duration(rl.rpm)), rl.rpm),
		}
		rl.limiters[userID] = ul
	}
	ul.lastSeen = time.Now()
	rl.mu.Unlock()

	return ul.limiter.Allow()
}

// StartCleanup periodically removes inactive limiters to prevent memory leaks.
func (rl *RateLimiter) StartCleanup(stop <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.mu.Lock()
			cutoff := time.Now().Add(-10 * time.Minute)
			for id, ul := range rl.limiters {
				if ul.lastSeen.Before(cutoff) {
					delete(rl.limiters, id)
				}
			}
			rl.mu.Unlock()
		case <-stop:
			return
		}
	}
}
