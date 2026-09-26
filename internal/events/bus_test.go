package events

import (
	"sync"
	"testing"
	"time"
)

func TestBusDeliversAndDropsWhenFull(t *testing.T) {
	b := NewBus()
	ch, cancel := b.Subscribe(1)
	defer cancel()
	b.Publish(Notice{Message: "one"})
	b.Publish(Notice{Message: "two"})
	if e := (<-ch).(Notice); e.Message != "one" {
		t.Fatalf("got %q", e.Message)
	}
	if b.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", b.Dropped())
	}
}

func TestBusCancelAndCloseAreIdempotent(t *testing.T) {
	b := NewBus()
	ch, cancel := b.Subscribe(4)
	cancel()
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("channel not closed after cancel")
	}
	ch2, cancel2 := b.Subscribe(4)
	b.Close()
	b.Close()
	cancel2()
	if _, ok := <-ch2; ok {
		t.Fatal("channel not closed after bus close")
	}
	b.Publish(Notice{})
	ch3, _ := b.Subscribe(1)
	if _, ok := <-ch3; ok {
		t.Fatal("subscribe after close should return a closed channel")
	}
}

func TestBusConcurrentPublishSubscribe(t *testing.T) {
	b := NewBus()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 200 {
				b.Publish(Notice{Time: time.Now()})
			}
		}()
		go func() {
			defer wg.Done()
			for range 20 {
				ch, cancel := b.Subscribe(8)
				select {
				case <-ch:
				default:
				}
				cancel()
			}
		}()
	}
	wg.Wait()
	b.Close()
}
