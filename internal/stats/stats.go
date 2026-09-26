// Package stats 는 프로세스 전역 트래픽 카운터를 보관한다.
package stats

import (
	"sync"
	"sync/atomic"
)

// Stats 는 동시 사용에 안전하다. RX 는 클라이언트에게 전달된 바이트(download),
// TX 는 클라이언트가 보낸 바이트(upload)다.
type Stats struct {
	active  atomic.Int64
	total   atomic.Int64
	proxied atomic.Int64
	direct  atomic.Int64
	failed  atomic.Int64
	rx      atomic.Int64
	tx      atomic.Int64
	hits    sync.Map // route 도메인 → *atomic.Int64
}

type Snapshot struct {
	Active  int64 `json:"active"`
	Total   int64 `json:"total"`
	Proxied int64 `json:"proxied"`
	Direct  int64 `json:"direct"`
	Failed  int64 `json:"failed"`
	RX      int64 `json:"rx"`
	TX      int64 `json:"tx"`
}

// Attempt 는 새로 라우팅된 연결 또는 요청을 기록한다.
func (s *Stats) Attempt(proxied bool) {
	s.total.Add(1)
	if proxied {
		s.proxied.Add(1)
	} else {
		s.direct.Add(1)
	}
}

// Hit 은 route 에 매칭된 연결 또는 요청 하나를 센다. 기본 DIRECT(route "")는 세지 않는다.
func (s *Stats) Hit(route string) {
	if route == "" {
		return
	}
	c, ok := s.hits.Load(route)
	if !ok {
		c, _ = s.hits.LoadOrStore(route, new(atomic.Int64))
	}
	c.(*atomic.Int64).Add(1)
}

// Hits 는 route 도메인별 누적 hit 수의 복사본이다.
func (s *Stats) Hits() map[string]int64 {
	out := make(map[string]int64)
	s.hits.Range(func(k, v any) bool {
		out[k.(string)] = v.(*atomic.Int64).Load()
		return true
	})
	return out
}

// RetainHits 는 keep 에 없는 route 의 hit 수를 버린다. 지웠다가 다시 추가한 route 는 0 부터 센다.
func (s *Stats) RetainHits(keep map[string]bool) {
	s.hits.Range(func(k, _ any) bool {
		if !keep[k.(string)] {
			s.hits.Delete(k)
		}
		return true
	})
}

func (s *Stats) Fail()       { s.failed.Add(1) }
func (s *Stats) Open()       { s.active.Add(1) }
func (s *Stats) Close()      { s.active.Add(-1) }
func (s *Stats) AddRX(n int) { s.rx.Add(int64(n)) }
func (s *Stats) AddTX(n int) { s.tx.Add(int64(n)) }

func (s *Stats) Snapshot() Snapshot {
	return Snapshot{
		Active:  s.active.Load(),
		Total:   s.total.Load(),
		Proxied: s.proxied.Load(),
		Direct:  s.direct.Load(),
		Failed:  s.failed.Load(),
		RX:      s.rx.Load(),
		TX:      s.tx.Load(),
	}
}
