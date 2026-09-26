package core

import (
	"time"

	"github.com/horyu1234/route-box/internal/config"
	"github.com/horyu1234/route-box/internal/ssh"
	"github.com/horyu1234/route-box/internal/stats"
)

// Badge 는 TUI 헤더에 보이는 한 단어짜리 전체 상태다.
type Badge string

const (
	BadgeConnected    Badge = "CONNECTED"
	BadgeDegraded     Badge = "DEGRADED"
	BadgeDisconnected Badge = "DISCONNECTED"
	BadgeReconnecting Badge = "RECONNECTING"
)

type ProxyStatus struct {
	Listen  string `json:"listen"`
	Running bool   `json:"running"`
	Err     string `json:"error,omitempty"`
}

type UpstreamStatus struct {
	Name       string         `json:"name"`
	Mode       config.SSHMode `json:"mode"`
	Host       string         `json:"host,omitempty"`
	Socks      string         `json:"socks"`
	Configured bool           `json:"configured"`
	Health     Health         `json:"health"`
	SSH        ssh.Status     `json:"ssh"`
	Routes     int            `json:"routes"`
}

// Healthy 는 이 업스트림으로 지금 트래픽을 보낼 수 있는지 판단한다.
func (u UpstreamStatus) Healthy() bool {
	if u.Mode == config.SSHManaged {
		return u.SSH.State == ssh.StateConnected && (u.Health.Checked.IsZero() || u.Health.Reachable)
	}
	return u.Health.Reachable
}

// Connecting 은 managed ssh 가 연결 또는 재연결 중인지 보고한다.
func (u UpstreamStatus) Connecting() bool {
	return u.Mode == config.SSHManaged && (u.SSH.State == ssh.StateStarting || u.SSH.State == ssh.StateReconnecting)
}

type Status struct {
	Badge     Badge            `json:"badge"`
	StartedAt time.Time        `json:"started_at,omitzero"`
	Proxy     ProxyStatus      `json:"proxy"`
	Upstreams []UpstreamStatus `json:"upstreams"`
	Stats     stats.Snapshot   `json:"stats"`
	RouteHits map[string]int64 `json:"route_hits,omitempty"` // route 도메인별 hit 수, 이번 실행 동안만 센다
	// Fallback 은 매칭되지 않은 연결이 나가는 곳: 업스트림 이름 또는 "direct".
	Fallback     string   `json:"fallback"`
	FallbackHits int64    `json:"fallback_hits"`
	Routes       int      `json:"routes"`
	Direct       int      `json:"direct_routes"`
	Warnings     []string `json:"warnings,omitempty"`
}

func (s Status) Uptime() time.Duration {
	if s.StartedAt.IsZero() {
		return 0
	}
	return time.Since(s.StartedAt)
}

// Upstream 은 이름으로 업스트림 상태를 찾는다.
func (s Status) Upstream(name string) (UpstreamStatus, bool) {
	for _, u := range s.Upstreams {
		if u.Name == name {
			return u, true
		}
	}
	return UpstreamStatus{}, false
}

func (a *App) Status() Status {
	cfg := a.store.Get()
	ups := a.effectiveUpstreams(cfg)
	a.mu.Lock()
	st := Status{
		StartedAt: a.started,
		Proxy:     ProxyStatus{Listen: a.listenAddrLocked(cfg), Running: a.proxyUp},
	}
	if a.proxyErr != nil {
		st.Proxy.Err = a.proxyErr.Error()
	}
	mgrs := make(map[string]*upstreamRT, len(a.ups))
	for n, rt := range a.ups {
		mgrs[n] = rt
	}
	health := make(map[string]Health, len(a.health))
	for n, h := range a.health {
		health[n] = h
	}
	a.mu.Unlock()

	st.Upstreams = make([]UpstreamStatus, 0, len(ups))
	for _, u := range ups {
		us := UpstreamStatus{
			Name: u.Name, Mode: u.Mode, Host: u.Host, Socks: u.Socks,
			Configured: u.Configured(), Health: health[u.Name], Routes: cfg.RoutesVia(u.Name),
			SSH: ssh.Status{State: ssh.StateDisabled, Host: u.Host},
		}
		if rt, ok := mgrs[u.Name]; ok {
			us.SSH = rt.mgr.Status()
		}
		st.Upstreams = append(st.Upstreams, us)
	}
	st.Stats = a.stats.Snapshot()
	st.Routes = len(cfg.Routes)
	st.Fallback, st.FallbackHits = cfg.FallbackVia(), a.stats.Unmatched()
	hits := a.stats.Hits()
	for _, r := range cfg.Routes {
		if n := hits[r.Domain]; n > 0 {
			if st.RouteHits == nil {
				st.RouteHits = make(map[string]int64)
			}
			st.RouteHits[r.Domain] = n
		}
		if r.Via() == "direct" {
			st.Direct++
		}
	}
	st.Warnings = a.Warnings()
	st.Badge = badge(st)
	return st
}

// listenAddrLocked 는 실행 중이면 실제로 bind 된 주소(":0" 해석 결과)를 우선한다.
func (a *App) listenAddrLocked(cfg config.Config) string {
	if a.proxyUp && a.listenAddr != "" {
		return a.listenAddr
	}
	if a.opts.ListenOverride != "" {
		return a.opts.ListenOverride
	}
	return cfg.Listen
}

func badge(s Status) Badge {
	if !s.Proxy.Running {
		return BadgeDisconnected
	}
	configured, healthy, connecting := 0, 0, 0
	for _, u := range s.Upstreams {
		if !u.Configured {
			continue
		}
		configured++
		switch {
		case u.Healthy():
			healthy++
		case u.Connecting():
			connecting++
		}
	}
	switch {
	case configured == 0:
		return BadgeDisconnected
	case connecting > 0:
		return BadgeReconnecting
	case healthy == configured:
		return BadgeConnected
	default:
		return BadgeDegraded
	}
}
