package service

import (
	"fmt"
	"sync"
	"time"
)

// RateLimitStatusCache provides an in-memory cache for global rate-limit status.
// When all upstream accounts for a group+model+platform are rate-limited,
// this cache allows short-circuiting at the gateway entry to avoid unnecessary
// account selection and error logging.
type RateLimitStatusCache struct {
	mu    sync.RWMutex
	items map[string]*rateLimitCacheEntry
}

type rateLimitCacheEntry struct {
	rateLimitedUntil time.Time
	retryAfterSec    int
	updatedAt        time.Time
}

// NewRateLimitStatusCache creates a new RateLimitStatusCache.
func NewRateLimitStatusCache() *RateLimitStatusCache {
	c := &RateLimitStatusCache{
		items: make(map[string]*rateLimitCacheEntry),
	}
	go c.cleanupLoop()
	return c
}

func rateLimitCacheKey(groupID int64, model, platform string) string {
	return fmt.Sprintf("%d:%s:%s", groupID, model, platform)
}

// Set marks a group+model+platform as rate-limited until the given time.
func (c *RateLimitStatusCache) Set(groupID int64, model, platform string, until time.Time, retryAfter int) {
	if c == nil {
		return
	}
	key := rateLimitCacheKey(groupID, model, platform)
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()

	if existing, ok := c.items[key]; ok && existing.rateLimitedUntil.After(until) {
		// Don't downgrade an existing longer rate-limit window.
		return
	}
	c.items[key] = &rateLimitCacheEntry{
		rateLimitedUntil: until,
		retryAfterSec:    retryAfter,
		updatedAt:        now,
	}
}

// Get checks if a group+model+platform is currently rate-limited.
// Returns (true, retryAfterSeconds) if rate-limited, (false, 0) otherwise.
func (c *RateLimitStatusCache) Get(groupID int64, model, platform string) (bool, int) {
	if c == nil {
		return false, 0
	}
	key := rateLimitCacheKey(groupID, model, platform)

	c.mu.RLock()
	entry, ok := c.items[key]
	c.mu.RUnlock()

	if !ok {
		return false, 0
	}
	if time.Now().After(entry.rateLimitedUntil) {
		return false, 0
	}
	retryAfter := entry.retryAfterSec
	if retryAfter <= 0 {
		retryAfter = int(time.Until(entry.rateLimitedUntil).Seconds())
		if retryAfter <= 0 {
			retryAfter = 1
		}
	}
	return true, retryAfter
}

// ClearForGroup removes all cache entries for the given group ID.
func (c *RateLimitStatusCache) ClearForGroup(groupID int64) {
	if c == nil {
		return
	}
	prefix := fmt.Sprintf("%d:", groupID)

	c.mu.Lock()
	defer c.mu.Unlock()

	for key := range c.items {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			delete(c.items, key)
		}
	}
}

// Clear removes a specific cache entry.
func (c *RateLimitStatusCache) Clear(groupID int64, model, platform string) {
	if c == nil {
		return
	}
	key := rateLimitCacheKey(groupID, model, platform)

	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.items, key)
}

// cleanupLoop runs in the background to remove expired entries.
func (c *RateLimitStatusCache) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		c.mu.Lock()
		now := time.Now()
		for key, entry := range c.items {
			if now.After(entry.rateLimitedUntil) {
				delete(c.items, key)
			}
		}
		c.mu.Unlock()
	}
}
