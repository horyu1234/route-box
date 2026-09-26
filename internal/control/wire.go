package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/horyu1234/route-box/internal/config"
	"github.com/horyu1234/route-box/internal/core"
	"github.com/horyu1234/route-box/internal/events"
)

// Snapshot 은 attach 한 TUI 가 한 번에 받아 가는 실행 중 인스턴스의 상태다.
type Snapshot struct {
	Config      config.Config `json:"config"`
	Status      core.Status   `json:"status"`
	Connections []Connection  `json:"connections"`
}

func toConnection(e events.ConnectionEvent) Connection {
	c := Connection{
		Time: e.Time, Method: e.Method, Host: e.Host, Port: e.Port, Route: e.Route, Upstream: e.Upstream,
		State: e.State.String(), BytesIn: e.BytesIn, BytesOut: e.BytesOut,
		Duration: e.Duration.Round(time.Millisecond).String(),
	}
	if e.Error != nil {
		c.Error = e.Error.Error()
	}
	return c
}

// Event 는 Connection 을 TUI 가 그리는 이벤트 형태로 되돌린다.
func (c Connection) Event() events.ConnectionEvent {
	e := events.ConnectionEvent{
		Time: c.Time, Method: c.Method, Host: c.Host, Port: c.Port, Route: c.Route, Upstream: c.Upstream,
		BytesIn: c.BytesIn, BytesOut: c.BytesOut,
	}
	_ = e.State.UnmarshalText([]byte(c.State))
	e.Duration, _ = time.ParseDuration(c.Duration)
	if c.Error != "" {
		e.Error = errors.New(c.Error)
	}
	return e
}

// wireEvent 는 /v1/events 스트림의 한 줄이다. error 필드는 JSON 으로 옮겨지지
// 않으므로 문자열로 따로 싣는다.
type wireEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
	Err  string          `json:"error,omitempty"`
}

// encodeEvent 는 스트림으로 보낼 이벤트를 직렬화한다. ConnectionEvent 는 양이
// 많고 attach 한 쪽은 스냅샷의 연결 로그를 쓰므로 보내지 않는다(ok=false).
func encodeEvent(e events.Event) (wireEvent, bool, error) {
	var typ string
	var err error
	switch v := e.(type) {
	case events.SSHStateChanged:
		typ = "ssh_state"
	case events.ProxyStarted:
		typ = "proxy_started"
	case events.ProxyStopped:
		typ, err, v.Err = "proxy_stopped", v.Err, nil
		e = v
	case events.RouteAdded:
		typ = "route_added"
	case events.RouteRemoved:
		typ = "route_removed"
	case events.RoutesChanged:
		typ = "routes_changed"
	case events.UpstreamHealth:
		typ, err, v.Err = "upstream_health", v.Err, nil
		e = v
	case events.ErrorEvent:
		typ, err, v.Err = "error", v.Err, nil
		e = v
	case events.Notice:
		typ = "notice"
	default:
		return wireEvent{}, false, nil
	}
	data, merr := json.Marshal(e)
	if merr != nil {
		return wireEvent{}, false, merr
	}
	w := wireEvent{Type: typ, Data: data}
	if err != nil {
		w.Err = err.Error()
	}
	return w, true, nil
}

func decodeEvent(w wireEvent) (events.Event, error) {
	var err error
	if w.Err != "" {
		err = errors.New(w.Err)
	}
	switch w.Type {
	case "ssh_state":
		return unmarshalEvent[events.SSHStateChanged](w.Data, nil)
	case "proxy_started":
		return unmarshalEvent[events.ProxyStarted](w.Data, nil)
	case "proxy_stopped":
		return unmarshalEvent(w.Data, func(e *events.ProxyStopped) { e.Err = err })
	case "route_added":
		return unmarshalEvent[events.RouteAdded](w.Data, nil)
	case "route_removed":
		return unmarshalEvent[events.RouteRemoved](w.Data, nil)
	case "routes_changed":
		return unmarshalEvent[events.RoutesChanged](w.Data, nil)
	case "upstream_health":
		return unmarshalEvent(w.Data, func(e *events.UpstreamHealth) { e.Err = err })
	case "error":
		return unmarshalEvent(w.Data, func(e *events.ErrorEvent) { e.Err = err })
	case "notice":
		return unmarshalEvent[events.Notice](w.Data, nil)
	default:
		return nil, fmt.Errorf("unknown event type %q", w.Type)
	}
}

// unmarshalEvent 는 error 필드를 비운 채로 디코드한 뒤 fix 로 채운다.
// error 인터페이스 필드에 JSON null 이 아닌 값이 오면 디코드가 실패하므로
// 인코드 쪽에서 항상 nil 로 보낸다.
func unmarshalEvent[T events.Event](data json.RawMessage, fix func(*T)) (events.Event, error) {
	var e T
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	if fix != nil {
		fix(&e)
	}
	return e, nil
}
