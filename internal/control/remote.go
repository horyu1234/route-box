package control

import (
	"context"
	"sync"
	"time"

	"github.com/horyu1234/route-box/internal/config"
	"github.com/horyu1234/route-box/internal/core"
	"github.com/horyu1234/route-box/internal/events"
	"github.com/horyu1234/route-box/internal/router"
	"github.com/horyu1234/route-box/internal/ssh"
)

const (
	remotePoll    = 250 * time.Millisecond
	remoteTimeout = 5 * time.Second
)

// Remote 는 실행 중인 인스턴스를 core.App 과 같은 메서드로 다룬다. TUI 는 Update
// 안에서 getter 를 동기적으로 부르므로, getter 는 주기적으로 받아 둔 스냅샷을
// 읽기만 하고 소켓을 기다리지 않는다. 변경 메서드는 요청 뒤 스냅샷을 다시 받아
// 호출자가 곧바로 새 상태를 보게 한다.
type Remote struct {
	c      *Client
	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	snap  Snapshot
	conns []events.ConnectionEvent
}

// NewRemote 는 첫 스냅샷을 받은 뒤 ctx 가 취소되거나 Close 될 때까지 폴링한다.
func NewRemote(ctx context.Context, c *Client) (*Remote, error) {
	ctx, cancel := context.WithCancel(ctx)
	r := &Remote{c: c, ctx: ctx, cancel: cancel}
	if err := r.poll(); err != nil {
		cancel()
		return nil, err
	}
	go func() {
		t := time.NewTicker(remotePoll)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = r.poll()
			}
		}
	}()
	return r, nil
}

func (r *Remote) Close() { r.cancel() }

func (r *Remote) poll() error {
	ctx, cancel := context.WithTimeout(r.ctx, remoteTimeout)
	defer cancel()
	s, err := r.c.Snapshot(ctx)
	if err != nil {
		return err
	}
	conns := make([]events.ConnectionEvent, len(s.Connections))
	for i, c := range s.Connections {
		conns[i] = c.Event()
	}
	r.mu.Lock()
	r.snap, r.conns = s, conns
	r.mu.Unlock()
	return nil
}

// after 는 변경 요청을 보낸 뒤 스냅샷을 새로 받는다.
func after[T any](r *Remote, fn func(ctx context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(r.ctx, remoteTimeout)
	defer cancel()
	v, err := fn(ctx)
	if err == nil {
		_ = r.poll()
	}
	return v, err
}

func (r *Remote) Config() config.Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snap.Config.Clone()
}

func (r *Remote) Status() core.Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snap.Status
}

func (r *Remote) Routes() []router.Route { return r.Config().Routes }

func (r *Remote) RecentConnections() []events.ConnectionEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]events.ConnectionEvent(nil), r.conns...)
}

// Subscribe 는 이벤트 스트림을 연다. 스트림을 열 수 없거나 인스턴스가 멈추면
// 채널이 닫혀, 로컬 App 의 bus 가 닫힐 때와 똑같이 보인다.
func (r *Remote) Subscribe(buffer int) (<-chan events.Event, func()) {
	ctx, cancel := context.WithCancel(r.ctx)
	out := make(chan events.Event, buffer)
	in, err := r.c.Events(ctx)
	if err != nil {
		close(out)
		return out, cancel
	}
	go func() {
		defer close(out)
		for e := range in {
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, cancel
}

func (r *Remote) AddRoute(input, via string) (router.Route, error) {
	return after(r, func(ctx context.Context) (router.Route, error) { return r.c.AddRoute(ctx, input, via) })
}

func (r *Remote) UpdateRoute(oldDomain, input, via string) (router.Route, error) {
	return after(r, func(ctx context.Context) (router.Route, error) { return r.c.UpdateRoute(ctx, oldDomain, input, via) })
}

func (r *Remote) SetRouteVia(domain, via string) (router.Route, error) {
	return after(r, func(ctx context.Context) (router.Route, error) { return r.c.SetRouteVia(ctx, domain, via) })
}

func (r *Remote) RemoveRoute(domain string) (router.Route, error) {
	return after(r, func(ctx context.Context) (router.Route, error) { return r.c.RemoveRoute(ctx, domain) })
}

func (r *Remote) AddPreset(name, via string) ([]router.Route, error) {
	return after(r, func(ctx context.Context) ([]router.Route, error) { return r.c.AddPreset(ctx, name, via) })
}

func (r *Remote) AddUpstream(u config.Upstream) (config.Upstream, error) {
	return after(r, func(ctx context.Context) (config.Upstream, error) { return r.c.AddUpstream(ctx, u) })
}

func (r *Remote) UpdateUpstream(oldName string, u config.Upstream) (config.Upstream, error) {
	return after(r, func(ctx context.Context) (config.Upstream, error) { return r.c.UpdateUpstream(ctx, oldName, u) })
}

func (r *Remote) RemoveUpstream(name string) error {
	_, err := after(r, func(ctx context.Context) (struct{}, error) { return struct{}{}, r.c.RemoveUpstream(ctx, name) })
	return err
}

func (r *Remote) RestartUpstream(name string) error {
	_, err := after(r, func(ctx context.Context) (struct{}, error) { return struct{}{}, r.c.RestartSSH(ctx, name) })
	return err
}

// ScanHostKey 는 ssh 연결을 기다려야 하므로 remoteTimeout 이 아니라 ctx 를 따른다.
func (r *Remote) ScanHostKey(ctx context.Context, name string) (ssh.HostKey, error) {
	return r.c.ScanHostKey(ctx, name)
}

func (r *Remote) TrustHostKey(name, fingerprint string) error {
	_, err := after(r, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, r.c.TrustHostKey(ctx, name, fingerprint)
	})
	return err
}

func (r *Remote) ClearConnections() error {
	_, err := after(r, func(ctx context.Context) (struct{}, error) { return struct{}{}, r.c.ClearConnections(ctx) })
	return err
}

func (r *Remote) SetConnectionLog(on bool) error {
	_, err := after(r, func(ctx context.Context) (struct{}, error) { return struct{}{}, r.c.SetConnectionLog(ctx, on) })
	return err
}

func (r *Remote) SetFallback(via string) (string, error) {
	return after(r, func(ctx context.Context) (string, error) { return r.c.SetFallback(ctx, via) })
}

func (r *Remote) SetLanguage(lang string) error {
	_, err := after(r, func(ctx context.Context) (struct{}, error) { return struct{}{}, r.c.SetLanguage(ctx, lang) })
	return err
}
