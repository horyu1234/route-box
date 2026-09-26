// Package stats 는 프로세스 전역 트래픽 카운터를 보관한다.
package stats

import "sync/atomic"

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
