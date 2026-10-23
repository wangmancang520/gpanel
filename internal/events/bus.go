// Package events is an in-process pub/sub bus so modules (scheduler,
// notifications, metrics, ...) can react to instance lifecycle changes
// without the instance package knowing about them.
package events

import (
	"sync"
	"time"
)

const (
	InstanceState   = "instance.state"   // Data: instance.Status
	InstanceCreated = "instance.created" // Data: instance.Config
	InstanceUpdated = "instance.updated" // Data: instance.Config
	InstanceDeleted = "instance.deleted" // Data: nil
)

type Event struct {
	Type       string    `json:"type"`
	InstanceID string    `json:"instanceId,omitempty"`
	Data       any       `json:"data,omitempty"`
	Time       time.Time `json:"time"`
}

type Bus struct {
	mu   sync.RWMutex
	next int
	subs map[int]func(Event)
}

func NewBus() *Bus { return &Bus{subs: map[int]func(Event){}} }

// Subscribe registers fn and returns an unsubscribe function.
// fn is called synchronously from Publish and must not block.
func (b *Bus) Subscribe(fn func(Event)) func() {
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = fn
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}
}

func (b *Bus) Publish(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, fn := range b.subs {
		fn(e)
	}
}
