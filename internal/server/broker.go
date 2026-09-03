/*
Package server implements the in-process metrics event broker.

本文件负责管理 SSE 订阅者，并把最新 DeviceState 非阻塞地广播给每个订阅者。
本文件不保存设备状态、不编码 SSE，也不提供跨进程消息队列。

数据链路：metrics Handler -> Broker -> SSE Handler -> Dashboard
*/
package server

import "sync"

// Broker fans out live device states to in-process subscribers.
// Broker 将实时设备状态分发给进程内订阅者。
type Broker struct {
	mu          sync.Mutex
	subscribers map[chan DeviceState]struct{}
}

// NewBroker creates an initialized Broker with no subscribers.
// NewBroker 创建一个尚无订阅者的进程内事件 Broker。
func NewBroker() *Broker { return &Broker{subscribers: make(map[chan DeviceState]struct{})} }

// Subscribe registers a bounded subscriber and returns its cleanup function.
// Subscribe 返回只读事件 channel；调用方结束时必须调用 cleanup 取消订阅。
func (b *Broker) Subscribe() (<-chan DeviceState, func()) {
	ch := make(chan DeviceState, 1)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() { b.mu.Lock(); delete(b.subscribers, ch); b.mu.Unlock() }
}

// Publish offers a state to every subscriber without blocking metric ingestion.
// Publish 在订阅者落后时丢弃该订阅者的旧实时机会，避免阻塞 Agent 上报。
func (b *Broker) Publish(state DeviceState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subscribers {
		select {
		case ch <- state:
		default:
		}
	}
}
