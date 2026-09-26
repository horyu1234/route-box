// Package events 는 core 의 런타임 알림을 관찰자(TUI, --no-tui logger)에게
// 서로 결합시키지 않고 전달한다.
package events

import (
	"time"

	"github.com/horyu1234/route-box/internal/router"
	"github.com/horyu1234/route-box/internal/ssh"
)

type Event interface {
	When() time.Time
}

type ConnState int

const (
	ConnOpen ConnState = iota
	ConnClosed
	ConnFailed
)

func (s ConnState) String() string {
	switch s {
	case ConnClosed:
		return "closed"
	case ConnFailed:
		return "failed"
	default:
		return "open"
	}
}

func (s ConnState) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// ConnectionEvent 는 하나의 CONNECT 터널 또는 일반 HTTP 요청을 기술한다.
// host 와 port 만 담을 뿐, path·query string·헤더는 절대 담지 않는다.
type ConnectionEvent struct {
	ID       uint64
	Time     time.Time
	Method   string // "CONNECT" 또는 "HTTP"
	Host     string
	Port     string
	Route    router.Mode
	Matched  string
	Upstream string // PROXY 일 때 탄 업스트림 이름
	State    ConnState
	BytesIn  int64 // upstream -> client 방향
	BytesOut int64 // client -> upstream 방향
	Duration time.Duration
	Error    error
}

type SSHStateChanged struct {
	Time     time.Time
	Upstream string
	Status   ssh.Status
}

type ProxyStarted struct {
	Time time.Time
	Addr string
}

type ProxyStopped struct {
	Time time.Time
	Err  error
}

type RouteAdded struct {
	Time  time.Time
	Route router.Route
}

type RouteRemoved struct {
	Time  time.Time
	Route router.Route
}

// RoutesChanged 는 route table 이 변경될 때마다 발생한다.
type RoutesChanged struct {
	Time   time.Time
	Routes []router.Route
}

type UpstreamHealth struct {
	Time      time.Time
	Upstream  string
	Reachable bool
	Err       error
}

type ErrorEvent struct {
	Time   time.Time
	Source string
	Err    error
}

type Notice struct {
	Time    time.Time
	Message string
	Warning bool
}

func (e ConnectionEvent) When() time.Time { return e.Time }
func (e SSHStateChanged) When() time.Time { return e.Time }
func (e ProxyStarted) When() time.Time    { return e.Time }
func (e ProxyStopped) When() time.Time    { return e.Time }
func (e RouteAdded) When() time.Time      { return e.Time }
func (e RouteRemoved) When() time.Time    { return e.Time }
func (e RoutesChanged) When() time.Time   { return e.Time }
func (e UpstreamHealth) When() time.Time  { return e.Time }
func (e ErrorEvent) When() time.Time      { return e.Time }
func (e Notice) When() time.Time          { return e.Time }
