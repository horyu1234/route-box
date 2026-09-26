package stats

import (
	"sync"
	"testing"
)

func TestCountersAreConcurrencySafe(t *testing.T) {
	var s Stats
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Open()
			s.Attempt(i%2 == 0)
			s.AddRX(10)
			s.AddTX(1)
			if i%5 == 0 {
				s.Fail()
			}
			s.Close()
		}()
	}
	wg.Wait()
	got := s.Snapshot()
	want := Snapshot{Active: 0, Total: 50, Proxied: 25, Direct: 25, Failed: 10, RX: 500, TX: 50}
	if got != want {
		t.Fatalf("snapshot = %+v, want %+v", got, want)
	}
}

func TestHitsPerRoute(t *testing.T) {
	var s Stats
	var wg sync.WaitGroup
	for i := range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Hit([]string{"example.com", "example.org", ""}[i%3])
		}()
	}
	wg.Wait()
	if got := s.Hits(); len(got) != 2 || got["example.com"] != 10 || got["example.org"] != 10 {
		t.Fatalf("hits = %v", got)
	}
	s.RetainHits(map[string]bool{"example.org": true})
	if got := s.Hits(); len(got) != 1 || got["example.org"] != 10 {
		t.Fatalf("after retain: %v", got)
	}
}
