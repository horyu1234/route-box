// Package logbuf 는 고정 크기의 동시성 안전한 ring buffer 를 제공한다.
package logbuf

import "sync"

type Ring[T any] struct {
	mu    sync.Mutex
	buf   []T
	start int
	n     int
}

func New[T any](capacity int) *Ring[T] {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring[T]{buf: make([]T, capacity)}
}

// Push 는 v 를 추가하고, 꽉 차 있으면 가장 오래된 항목을 밀어낸다.
func (r *Ring[T]) Push(v T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = v
		r.n++
		return
	}
	r.buf[r.start] = v
	r.start = (r.start + 1) % len(r.buf)
}

// Update 는 match 가 true 를 반환하는 항목 중 가장 최신 것에 fn 을 적용한다.
func (r *Ring[T]) Update(match func(T) bool, fn func(*T)) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := r.n - 1; i >= 0; i-- {
		idx := (r.start + i) % len(r.buf)
		if match(r.buf[idx]) {
			fn(&r.buf[idx])
			return true
		}
	}
	return false
}

// Snapshot 은 항목들을 오래된 순서대로 반환한다.
func (r *Ring[T]) Snapshot() []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]T, r.n)
	for i := range r.n {
		out[i] = r.buf[(r.start+i)%len(r.buf)]
	}
	return out
}

// Clear 는 모든 항목을 버린다.
func (r *Ring[T]) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.buf)
	r.start, r.n = 0, 0
}

func (r *Ring[T]) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}
