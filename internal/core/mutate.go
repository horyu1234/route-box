package core

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/horyu1234/route-box/internal/config"
	"github.com/horyu1234/route-box/internal/events"
	"github.com/horyu1234/route-box/internal/router"
)

func errorf(sentinel error, subject string) error {
	return fmt.Errorf("%w: %s", sentinel, subject)
}

// Routes 는 현재 route 목록을 설정 순서대로 돌려준다.
func (a *App) Routes() []router.Route { return a.store.Get().Routes }

// Upstreams 는 설정된 업스트림 목록을 돌려준다.
func (a *App) Upstreams() []config.Upstream { return a.store.Get().Upstreams }

func (a *App) commitRoutes(cfg config.Config) {
	a.router.SetRoutes(cfg.Routes)
	keep := make(map[string]bool, len(cfg.Routes))
	for _, r := range cfg.Routes {
		keep[r.Domain] = true
	}
	a.stats.RetainHits(keep)
	a.publish(events.RoutesChanged{Time: time.Now(), Routes: cfg.Routes})
}

// AddRoute 는 입력(URL, 포트, 대소문자, 끝의 점)을 정규화해 추가한다. via 는
// 업스트림 이름, "direct", 또는 첫 업스트림을 뜻하는 "" 이다.
func (a *App) AddRoute(input, via string) (router.Route, error) {
	r, err := router.NewRoute(input, via)
	if err != nil {
		return router.Route{}, err
	}
	cfg, err := a.store.Update(func(c *config.Config) error {
		for _, x := range c.Routes {
			if x.Domain == r.Domain {
				return errorf(ErrRouteExists, r.Domain)
			}
		}
		c.Routes = append(c.Routes, r)
		return nil
	})
	if err != nil {
		return router.Route{}, err
	}
	a.commitRoutes(cfg)
	r = findRoute(cfg, r.Domain)
	a.publish(events.RouteAdded{Time: time.Now(), Route: r})
	return r, nil
}

func findRoute(cfg config.Config, domain string) router.Route {
	for _, x := range cfg.Routes {
		if x.Domain == domain {
			return x
		}
	}
	return router.Route{}
}

func (a *App) RemoveRoute(input string) (router.Route, error) {
	domain, err := router.NormalizeRouteInput(input)
	if err != nil {
		return router.Route{}, err
	}
	var removed router.Route
	cfg, err := a.store.Update(func(c *config.Config) error {
		for i, x := range c.Routes {
			if x.Domain == domain {
				removed = x
				c.Routes = append(c.Routes[:i:i], c.Routes[i+1:]...)
				return nil
			}
		}
		return errorf(ErrRouteNotFound, domain)
	})
	if err != nil {
		return router.Route{}, err
	}
	a.commitRoutes(cfg)
	a.publish(events.RouteRemoved{Time: time.Now(), Route: removed})
	return removed, nil
}

// UpdateRoute 는 oldDomain 의 route 를 같은 자리에서 교체한다.
func (a *App) UpdateRoute(oldDomain, input, via string) (router.Route, error) {
	r, err := router.NewRoute(input, via)
	if err != nil {
		return router.Route{}, err
	}
	cfg, err := a.store.Update(func(c *config.Config) error {
		idx := -1
		for i, x := range c.Routes {
			if x.Domain == oldDomain {
				idx = i
			} else if x.Domain == r.Domain {
				return errorf(ErrRouteExists, r.Domain)
			}
		}
		if idx < 0 {
			return errorf(ErrRouteNotFound, oldDomain)
		}
		c.Routes[idx] = r
		return nil
	})
	if err != nil {
		return router.Route{}, err
	}
	a.commitRoutes(cfg)
	return findRoute(cfg, r.Domain), nil
}

// SetRouteVia 는 route 의 목적지만 바꾼다.
func (a *App) SetRouteVia(domain, via string) (router.Route, error) {
	return a.UpdateRoute(domain, domain, via)
}

// AddPreset 은 preset 의 route 중 아직 없는 것만 via 로 추가하고 돌려준다.
func (a *App) AddPreset(name, via string) ([]router.Route, error) {
	p, err := router.FindPreset(name)
	if err != nil {
		return nil, err
	}
	routes, err := p.Routes(via)
	if err != nil {
		return nil, err
	}
	var addedDomains []string
	cfg, err := a.store.Update(func(c *config.Config) error {
		have := make(map[string]bool, len(c.Routes))
		for _, x := range c.Routes {
			have[x.Domain] = true
		}
		for _, r := range routes {
			if !have[r.Domain] {
				c.Routes = append(c.Routes, r)
				addedDomains = append(addedDomains, r.Domain)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	added := make([]router.Route, 0, len(addedDomains))
	for _, d := range addedDomains {
		added = append(added, findRoute(cfg, d))
	}
	if len(added) > 0 {
		a.commitRoutes(cfg)
		for _, r := range added {
			a.publish(events.RouteAdded{Time: time.Now(), Route: r})
		}
	}
	return added, nil
}

// AddUpstream 은 업스트림을 추가하고 실행 중이면 바로 연결을 시작한다.
// 첫 업스트림이면 via 가 비어 있던 proxy route 들이 이 업스트림에 고정된다.
func (a *App) AddUpstream(u config.Upstream) (config.Upstream, error) {
	cfg, err := a.store.Update(func(c *config.Config) error {
		if _, ok := c.Upstream(strings.ToLower(strings.TrimSpace(u.Name))); ok {
			return fmt.Errorf("%w: %s", config.ErrDuplicateUpstream, u.Name)
		}
		c.Upstreams = append(c.Upstreams, u)
		return nil
	})
	if err != nil {
		return config.Upstream{}, err
	}
	added := cfg.Upstreams[len(cfg.Upstreams)-1]
	a.mu.Lock()
	if a.overrideName == "" {
		a.overrideName = added.Name
	}
	a.mu.Unlock()
	a.commitRoutes(cfg)
	a.syncUpstreams(cfg)
	return added, nil
}

// UpdateUpstream 은 oldName 업스트림을 교체한다. 이름이 바뀌면 그 업스트림을
// 쓰던 route 도 새 이름을 따라간다.
func (a *App) UpdateUpstream(oldName string, u config.Upstream) (config.Upstream, error) {
	var idx int
	cfg, err := a.store.Update(func(c *config.Config) error {
		idx = -1
		newName := strings.ToLower(strings.TrimSpace(u.Name))
		for i, x := range c.Upstreams {
			if x.Name == oldName {
				idx = i
			} else if x.Name == newName {
				return fmt.Errorf("%w: %s", config.ErrDuplicateUpstream, newName)
			}
		}
		if idx < 0 {
			return errorf(ErrUpstreamMissing, oldName)
		}
		c.Upstreams[idx] = u
		for i, r := range c.Routes {
			if r.Mode == router.ModeProxy && r.Upstream == oldName {
				c.Routes[i].Upstream = newName
			}
		}
		return nil
	})
	if err != nil {
		return config.Upstream{}, err
	}
	a.mu.Lock()
	if a.overrideName == oldName {
		a.socksOverride = ""
	}
	a.mu.Unlock()
	a.commitRoutes(cfg)
	a.syncUpstreams(cfg)
	return cfg.Upstreams[idx], nil
}

// RemoveUpstream 은 아무 route 도 쓰지 않는 업스트림만 지운다. 쓰던 route 를
// 다른 출구로 몰래 옮기지 않기 위해서다.
func (a *App) RemoveUpstream(name string) error {
	cfg, err := a.store.Update(func(c *config.Config) error {
		if n := c.RoutesVia(name); n > 0 {
			return fmt.Errorf("%w: %s is used by %d route(s); move them first", ErrUpstreamInUse, name, n)
		}
		for i, x := range c.Upstreams {
			if x.Name == name {
				c.Upstreams = append(c.Upstreams[:i:i], c.Upstreams[i+1:]...)
				return nil
			}
		}
		return errorf(ErrUpstreamMissing, name)
	})
	if err != nil {
		return err
	}
	a.syncUpstreams(cfg)
	return nil
}

// SetLanguage 는 TUI 언어("en", "ko", 자동은 "")를 저장한다.
func (a *App) SetLanguage(lang string) error {
	_, err := a.store.Update(func(c *config.Config) error {
		c.Language = lang
		return nil
	})
	return err
}

// SaveDefaults 는 현재 config 를 기록한다(예: 첫 실행 온보딩 완료).
func (a *App) SaveDefaults() error {
	_, err := a.store.Update(func(*config.Config) error { return nil })
	return err
}

// SuggestSocks 는 다른 업스트림이 쓰지 않는 127.0.0.1:1080 이후의 첫 주소를 고른다.
func SuggestSocks(cfg config.Config, except string) string {
	used := map[string]bool{}
	for _, u := range cfg.Upstreams {
		if u.Name != except {
			used[u.Socks] = true
		}
	}
	for port := 1080; port < 1180; port++ {
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		if !used[addr] {
			return addr
		}
	}
	return config.DefaultSocks
}

var nameJunk = regexp.MustCompile(`[^a-z0-9_-]+`)

// SuggestName 은 ssh 호스트(별칭이나 도메인)에서 겹치지 않는 업스트림 이름을 만든다.
func SuggestName(cfg config.Config, host string) string {
	base := strings.ToLower(strings.TrimSpace(host))
	if i := strings.LastIndexByte(base, '@'); i >= 0 {
		base = base[i+1:]
	}
	if net.ParseIP(base) == nil {
		if i := strings.IndexByte(base, '.'); i > 0 {
			base = base[:i]
		}
	}
	base = strings.Trim(nameJunk.ReplaceAllString(base, "-"), "-_")
	if len(base) > 20 {
		base = base[:20]
	}
	if base == "" || base == router.ViaDirect || router.ValidateUpstreamName(base) != nil {
		base = "upstream"
	}
	name := base
	for i := 2; ; i++ {
		if _, taken := cfg.Upstream(name); !taken {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}
