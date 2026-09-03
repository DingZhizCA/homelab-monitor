/*
Package server tracks whether at least one Dashboard is recently visible.

本文件负责记录页面心跳并提供 30 分钟活跃宽限期，使 Agent 在有人查看时提高采样
频率。本文件不维护用户会话，也不主动轮询浏览器。

数据链路：Dashboard presence POST -> Presence -> metrics response -> Agent cadence
*/
package server

import (
	"sync"
	"time"
)

// ActiveGracePeriod keeps active sampling after the latest visible-page heartbeat.
// ActiveGracePeriod 为页面关闭、隐藏或心跳中断保留 30 分钟活跃采样宽限期。
const ActiveGracePeriod = 30 * time.Minute

// Presence stores the latest Dashboard heartbeat with concurrent access protection.
// Presence 保存最近一次 Dashboard 心跳，并保护并发读写。
type Presence struct {
	mu       sync.RWMutex
	lastSeen time.Time
	now      func() time.Time
}

// NewPresence creates an inactive Presence tracker.
func NewPresence() *Presence { return &Presence{now: time.Now} }

// Touch records a visible Dashboard heartbeat and returns its active expiry time.
func (p *Presence) Touch() time.Time {
	now := p.now()
	p.mu.Lock()
	p.lastSeen = now
	p.mu.Unlock()
	return now.Add(ActiveGracePeriod)
}

// Active reports whether the latest heartbeat remains within the grace period.
func (p *Presence) Active() bool {
	_, active := p.Mode()
	return active
}

// Mode returns the active expiry time and current activity state atomically.
func (p *Presence) Mode() (time.Time, bool) {
	p.mu.RLock()
	lastSeen := p.lastSeen
	p.mu.RUnlock()
	expiresAt := lastSeen.Add(ActiveGracePeriod)
	return expiresAt, !lastSeen.IsZero() && !p.now().After(expiresAt)
}
