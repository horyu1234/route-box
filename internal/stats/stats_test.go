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
