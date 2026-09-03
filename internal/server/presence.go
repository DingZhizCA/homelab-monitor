package server

import (
	"sync"
	"time"
)

const ActiveGracePeriod = 30 * time.Minute

type Presence struct {
	mu       sync.RWMutex
	lastSeen time.Time
	now      func() time.Time
}

func NewPresence() *Presence { return &Presence{now: time.Now} }

func (p *Presence) Touch() time.Time {
	now := p.now()
	p.mu.Lock()
	p.lastSeen = now
	p.mu.Unlock()
	return now.Add(ActiveGracePeriod)
}

func (p *Presence) Active() bool {
	_, active := p.Mode()
	return active
}

func (p *Presence) Mode() (time.Time, bool) {
	p.mu.RLock()
	lastSeen := p.lastSeen
	p.mu.RUnlock()
	expiresAt := lastSeen.Add(ActiveGracePeriod)
	return expiresAt, !lastSeen.IsZero() && !p.now().After(expiresAt)
}
