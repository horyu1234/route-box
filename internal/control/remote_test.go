package control

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/horyu1234/route-box/internal/config"
	"github.com/horyu1234/route-box/internal/core"
	"github.com/horyu1234/route-box/internal/events"
	"github.com/horyu1234/route-box/internal/router"
	"github.com/horyu1234/route-box/internal/ssh"
)

func TestEventCodecRoundTrip(t *testing.T) {
	now := time.Now().Round(0).UTC()
	route := router.Route{Domain: "example.com", Mode: router.ModeProxy, Upstream: "seoul"}
	cases := []events.Event{
		events.SSHStateChanged{Time: now, Upstream: "seoul", Status: ssh.Status{State: ssh.StateReconnecting, Host: "proxy-seoul", PID: 42, Since: now, Attempt: 2, Err: "Connection refused"}},
		events.ProxyStarted{Time: now, Addr: "127.0.0.1:8080"},
		events.ProxyStopped{Time: now, Err: errors.New("address already in use")},
		events.ProxyStopped{Time: now},
		events.RouteAdded{Time: now, Route: route},
		events.RouteRemoved{Time: now, Route: route},
		events.RoutesChanged{Time: now, Routes: []router.Route{route, {Domain: "example.org", Mode: router.ModeDirect}}},
		events.UpstreamHealth{Time: now, Upstream: "lab", Reachable: false, Err: errors.New("dial tcp: refused")},
		events.UpstreamHealth{Time: now, Upstream: "lab", Reachable: true},
		events.ErrorEvent{Time: now, Source: "proxy", Err: errors.New("boom")},
		events.Notice{Time: now, Message: "hello", Warning: true},
	}
	for _, e := range cases {
		w, ok, err := encodeEvent(e)
		if err != nil || !ok {
			t.Fatalf("%T: encode ok=%v err=%v", e, ok, err)
		}
		b, err := json.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		var back wireEvent
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		got, err := decodeEvent(back)
		if err != nil {
			t.Fatalf("%T: decode: %v", e, err)
		}
		// error 는 문자열로만 옮겨지므로 %v 로 비교한다.
		if reflect.TypeOf(got) != reflect.TypeOf(e) || errString(got) != errString(e) || !sameExceptErr(got, e) {
			t.Errorf("round trip:\n got %#v\nwant %#v", got, e)
		}
	}
	if _, ok, _ := encodeEvent(events.ConnectionEvent{Host: "example.com"}); ok {
		t.Error("connection events must not be streamed")
	}
}

func errString(e events.Event) string {
	var err error
	switch v := e.(type) {
	case events.ProxyStopped:
		err = v.Err
	case events.UpstreamHealth:
		err = v.Err
	case events.ErrorEvent:
		err = v.Err
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

func sameExceptErr(a, b events.Event) bool {
	strip := func(e events.Event) events.Event {
		switch v := e.(type) {
		case events.ProxyStopped:
			v.Err = nil
			return v
		case events.UpstreamHealth:
			v.Err = nil
			return v
		case events.ErrorEvent:
			v.Err = nil
			return v
		}
		return e
	}
	return reflect.DeepEqual(strip(a), strip(b))
}

func TestConnectionConversionKeepsLogFields(t *testing.T) {
	e := events.ConnectionEvent{
		Time: time.Now().Round(0), Method: "CONNECT", Host: "example.com", Port: "443", Route: router.ModeProxy,
		Upstream: "seoul", State: events.ConnFailed, BytesIn: 10, BytesOut: 20, Duration: 1500 * time.Millisecond,
		Error: errors.New("upstream seoul unavailable"),
	}
	got := toConnection(e).Event()
	if got.Error == nil || got.Error.Error() != e.Error.Error() {
		t.Fatalf("error lost: %v", got.Error)
	}
	got.Error, e.Error = nil, nil
	if !reflect.DeepEqual(got, e) {
		t.Fatalf("got %+v\nwant %+v", got, e)
	}
}

// TestRemoteMirrorsRunningInstance 는 attach 한 TUI 가 쓰는 경로 전체를 확인한다:
// 스냅샷, 변경 후 즉시 보이는 상태, 이벤트 스트림, 인스턴스가 멈추면 닫히는 스트림.
func TestRemoteMirrorsRunningInstance(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "routebox.sock")
	cfg := config.Default()
	cfg.Upstreams = []config.Upstream{{Name: "lab", Mode: config.SSHExternal, Socks: "127.0.0.1:1"}}
	app := core.New(core.Options{ConfigPath: filepath.Join(dir, "config.json"), Config: cfg, ListenOverride: "127.0.0.1:0"})

	runCtx, stopApp := context.WithCancel(context.Background())
	defer stopApp()
	runDone := make(chan error, 1)
	go func() { runDone <- app.Run(runCtx) }()
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	srvCtx, stopSrv := context.WithCancel(context.Background())
	defer stopSrv()
	go func() { _ = Serve(srvCtx, ln, app) }()

	deadline := time.Now().Add(5 * time.Second)
	for !app.Status().Proxy.Running {
		if time.Now().After(deadline) {
			t.Fatal("proxy did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	r, err := NewRemote(context.Background(), NewClient(sock))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.Status().Proxy.Running || len(r.Config().Upstreams) != 1 {
		t.Fatalf("initial snapshot: %+v", r.Status())
	}
	sub, unsub := r.Subscribe(64)
	defer unsub()

	if _, err := r.AddRoute("https://www.example.com/path", ""); err != nil {
		t.Fatal(err)
	}
	if got := r.Routes(); len(got) != 1 || got[0].Domain != "www.example.com" || got[0].Upstream != "lab" {
		t.Fatalf("routes right after add: %+v", got)
	}
	waitFor(t, sub, func(e events.Event) bool {
		rc, ok := e.(events.RoutesChanged)
		return ok && len(rc.Routes) == 1
	})

	if _, err := r.UpdateRoute("www.example.com", "example.com", "direct"); err != nil {
		t.Fatal(err)
	}
	if got := r.Routes(); len(got) != 1 || got[0].Domain != "example.com" || got[0].Mode != router.ModeDirect {
		t.Fatalf("routes after update: %+v", got)
	}
	if _, err := r.UpdateUpstream("lab", config.Upstream{Name: "lab2", Mode: config.SSHExternal, Socks: "127.0.0.1:2"}); err != nil {
		t.Fatal(err)
	}
	if u := r.Config().Upstreams; len(u) != 1 || u[0].Name != "lab2" {
		t.Fatalf("upstreams after update: %+v", u)
	}
	if err := r.SetLanguage("ko"); err != nil || r.Config().Language != "ko" {
		t.Fatalf("language: %v %q", err, r.Config().Language)
	}
	if err := r.RemoveUpstream("nope"); !errors.Is(err, core.ErrUpstreamMissing) {
		t.Fatalf("sentinel lost over the socket: %v", err)
	}
	if app.Config().Language != "ko" || app.Routes()[0].Domain != "example.com" {
		t.Fatal("changes did not reach the running instance")
	}

	// 인스턴스가 멈추면 스트림이 닫혀야 TUI 가 끊겼음을 안다.
	stopApp()
	<-runDone
	closed := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-sub:
			if !ok {
				return
			}
		case <-closed:
			t.Fatal("event stream stayed open after the instance stopped")
		}
	}
}

func waitFor(t *testing.T, ch <-chan events.Event, match func(events.Event) bool) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatal("event stream closed")
			}
			if match(e) {
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for event")
		}
	}
}
