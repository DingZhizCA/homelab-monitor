/*
Package auth supplies a bounded fixed-window limiter for login-related actions.

All limits are explicit constructor inputs; this implementation stores only
opaque caller-provided keys and forgets expired windows during later requests.
*/
package auth

import (
	"sync"
	"time"
)

type loginWindow struct {
	count   int
	expires time.Time
}

// FixedWindowLimiter bounds attempts and retained keys using caller-selected limits.
type FixedWindowLimiter struct {
	mu          sync.Mutex
	entries     map[string]loginWindow
	maxEntries  int
	maxAttempts int
	window      time.Duration
	now         func() time.Time
}

// NewFixedWindowLimiter requires explicit, positive storage and attempt limits.
func NewFixedWindowLimiter(maxEntries, maxAttempts int, window time.Duration, now func() time.Time) (*FixedWindowLimiter, error) {
	if maxEntries <= 0 || maxAttempts <= 0 || window <= 0 || now == nil {
		return nil, ErrInvalidLimiterConfiguration
	}
	return &FixedWindowLimiter{
		entries: make(map[string]loginWindow), maxEntries: maxEntries,
		maxAttempts: maxAttempts, window: window, now: now,
	}, nil
}

// Allow records an attempt and fails closed when the key table is full.
func (l *FixedWindowLimiter) Allow(key string) bool {
	if key == "" {
		return false
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for existingKey, entry := range l.entries {
		if !now.Before(entry.expires) {
			delete(l.entries, existingKey)
		}
	}
	entry, ok := l.entries[key]
	if !ok {
		if len(l.entries) >= l.maxEntries {
			return false
		}
		l.entries[key] = loginWindow{count: 1, expires: now.Add(l.window)}
		return true
	}
	if entry.count >= l.maxAttempts {
		return false
	}
	entry.count++
	l.entries[key] = entry
	return true
}
