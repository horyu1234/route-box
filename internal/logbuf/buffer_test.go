package logbuf

import (
	"slices"
	"sync"
	"testing"
)

func TestRingEvictsOldest(t *testing.T) {
	r := New[int](3)
	for i := 1; i <= 5; i++ {
		r.Push(i)
	}
	if got := r.Snapshot(); !slices.Equal(got, []int{3, 4, 5}) {
		t.Fatalf("snapshot = %v", got)
	}
}

func TestRingUpdateNewestMatch(t *testing.T) {
	r := New[int](4)
	for _, v := range []int{1, 2, 1, 3} {
		r.Push(v)
	}
	if !r.Update(func(v int) bool { return v == 1 }, func(v *int) { *v = 9 }) {
		t.Fatal("no match")
	}
	if got := r.Snapshot(); !slices.Equal(got, []int{1, 2, 9, 3}) {
		t.Fatalf("snapshot = %v", got)
	}
	if r.Update(func(v int) bool { return v == 42 }, func(*int) {}) {
		t.Fatal("unexpected match")
	}
}

func TestRingConcurrent(t *testing.T) {
	r := New[int](16)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 100 {
				r.Push(i*100 + j)
				_ = r.Snapshot()
			}
		}()
	}
	wg.Wait()
	if r.Len() != 16 {
		t.Fatalf("len = %d", r.Len())
	}
}
