package server

import "sync"

type Broker struct {
	mu          sync.Mutex
	subscribers map[chan DeviceState]struct{}
}

func NewBroker() *Broker { return &Broker{subscribers: make(map[chan DeviceState]struct{})} }

func (b *Broker) Subscribe() (<-chan DeviceState, func()) {
	ch := make(chan DeviceState, 1)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() { b.mu.Lock(); delete(b.subscribers, ch); b.mu.Unlock() }
}

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
