package events

import (
	"sync"
	"sync/atomic"
)

// Bus 는 구독자들에게 이벤트를 분배한다. Publish 는 절대 블록하지 않는다:
// 처리가 뒤처진 구독자는 프록시를 멈추게 하는 대신 이벤트를 잃는다.
type Bus struct {
	mu      sync.RWMutex
	subs    map[chan Event]struct{}
	closed  bool
	dropped atomic.Uint64
}

func NewBus() *Bus {
	return &Bus{subs: make(map[chan Event]struct{})}
}

func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return
	}
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
			b.dropped.Add(1)
		}
	}
}

// Subscribe 는 이벤트 채널과 그것을 닫는 취소 함수를 반환한다.
// bus 가 닫힐 때도 이 채널은 함께 닫힌다.
func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	ch := make(chan Event, buffer)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		close(ch)
		return ch, func() {}
	}
	b.subs[ch] = struct{}{}
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if _, ok := b.subs[ch]; ok {
				delete(b.subs, ch)
				close(ch)
			}
		})
	}
}

func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for ch := range b.subs {
		close(ch)
	}
	clear(b.subs)
}

func (b *Bus) Dropped() uint64 { return b.dropped.Load() }
