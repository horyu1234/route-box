package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"

	"github.com/horyu1234/route-box/internal/router"
)

const (
	DefaultListen = "127.0.0.1:8080"
	DefaultSocks  = "127.0.0.1:1080"
)

type SSHMode string

const (
	SSHManaged  SSHMode = "managed"
	SSHExternal SSHMode = "external"
)

// Upstream 은 이름 붙은 SOCKS 출구 하나다. managed 면 RouteBox 가 ssh -D 를
// 띄우고, external 이면 이미 떠 있는 Socks 주소만 쓴다. Port 0 과 빈 필드는
// ~/.ssh/config 에 맡긴다. identity file 은 경로만 저장한다.
type Upstream struct {
	Name         string  `json:"name"`
	Mode         SSHMode `json:"mode"`
	Host         string  `json:"host,omitempty"`
	User         string  `json:"user,omitempty"`
	Port         int     `json:"port,omitempty"`
	IdentityFile string  `json:"identity_file,omitempty"`
	Socks        string  `json:"socks"`
	Reconnect    bool    `json:"reconnect"`
}

// Configured 는 이 업스트림으로 SOCKS 에 닿을 수 있을 만큼 정보가 있는지 보고한다.
func (u Upstream) Configured() bool {
	if u.Mode == SSHExternal {
		return u.Socks != ""
	}
	return u.Host != ""
}

// legacySSH 는 업스트림이 하나뿐이던 예전 설정 형식이다. 읽을 때만 쓴다.
type legacySSH struct {
	Mode         SSHMode `json:"mode"`
	Host         string  `json:"host"`
	User         string  `json:"user"`
	Port         int     `json:"port"`
	IdentityFile string  `json:"identity_file"`
	Reconnect    bool    `json:"reconnect"`
}

type Config struct {
	Listen    string         `json:"listen"`
	Language  string         `json:"language,omitempty"`
	Upstreams []Upstream     `json:"upstreams"`
	Routes    []router.Route `json:"routes"`
	// Fallback 은 어떤 route 에도 매칭되지 않은 연결이 나갈 업스트림 이름이다.
	// 비어 있으면 DIRECT 다.
	Fallback string `json:"fallback,omitempty"`

	LegacySocks string     `json:"socks,omitempty"`
	LegacySSH   *legacySSH `json:"ssh,omitempty"`
	// Migrated 는 이번 로드에서 예전 단일 업스트림 형식을 옮겼음을 알린다.
	Migrated bool `json:"-"`
}

func Default() Config {
	return Config{
		Listen:    DefaultListen,
		Upstreams: []Upstream{},
		Routes:    []router.Route{},
	}
}

func (c Config) Clone() Config {
	c.Routes = slices.Clone(c.Routes)
	if c.Routes == nil {
		c.Routes = []router.Route{}
	}
	c.Upstreams = slices.Clone(c.Upstreams)
	if c.Upstreams == nil {
		c.Upstreams = []Upstream{}
	}
	if c.LegacySSH != nil {
		l := *c.LegacySSH
		c.LegacySSH = &l
	}
	return c
}

// Upstream 은 이름으로 업스트림을 찾는다.
func (c Config) Upstream(name string) (Upstream, bool) {
	for _, u := range c.Upstreams {
		if u.Name == name {
			return u, true
		}
	}
	return Upstream{}, false
}

// DefaultUpstream 은 via 를 지정하지 않은 proxy route 가 쓰는 첫 업스트림 이름이다.
func (c Config) DefaultUpstream() string {
	if len(c.Upstreams) == 0 {
		return ""
	}
	return c.Upstreams[0].Name
}

// UpstreamConfigured 는 쓸 수 있는 업스트림이 하나라도 있는지 보고한다.
func (c Config) UpstreamConfigured() bool {
	for _, u := range c.Upstreams {
		if u.Configured() {
			return true
		}
	}
	return false
}

// RoutesVia 는 해당 업스트림으로 나가는 route 수를 센다.
func (c Config) RoutesVia(name string) int {
	n := 0
	for _, r := range c.Routes {
		if r.Mode == router.ModeProxy && r.Upstream == name {
			n++
		}
	}
	return n
}

func (c *Config) migrateLegacy() {
	if len(c.Upstreams) == 0 && c.LegacySSH != nil {
		l := c.LegacySSH
		if l.Mode == "" {
			l.Mode = SSHManaged
		}
		u := Upstream{Name: "default", Mode: l.Mode, Host: l.Host, User: l.User, Port: l.Port,
			IdentityFile: l.IdentityFile, Socks: c.LegacySocks, Reconnect: l.Reconnect}
		if u.Socks == "" {
			u.Socks = DefaultSocks
		}
		if u.Configured() {
			c.Upstreams = []Upstream{u}
			c.Migrated = true
		}
	}
	c.LegacySSH, c.LegacySocks = nil, ""
}

// Normalize 는 예전 형식을 옮기고 기본값을 채운 뒤 routes 를 정규화하고 검증한다.
// via 가 비어 있는 proxy route 는 업스트림이 생기는 순간 첫 업스트림 이름으로
// 고정된다. 나중에 업스트림 순서가 바뀌어도 조용히 다른 출구로 가지 않게 하기 위해서다.
func (c *Config) Normalize() error {
	c.migrateLegacy()
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if c.Upstreams == nil {
		c.Upstreams = []Upstream{}
	}
	for i := range c.Upstreams {
		u := &c.Upstreams[i]
		u.Name = strings.ToLower(strings.TrimSpace(u.Name))
		u.Host = strings.TrimSpace(u.Host)
		u.User = strings.TrimSpace(u.User)
		if u.Mode == "" {
			u.Mode = SSHManaged
		}
		if u.Socks == "" {
			u.Socks = DefaultSocks
		}
	}
	c.Fallback = NormalizeFallback(c.Fallback)
	def := c.DefaultUpstream()
	seen := make(map[string]bool, len(c.Routes))
	routes := make([]router.Route, 0, len(c.Routes))
	for _, r := range c.Routes {
		mode, err := router.ParseMode(string(r.Mode))
		if err != nil {
			return fmt.Errorf("route %q: %w", r.Domain, err)
		}
		via := r.Upstream
		if mode == router.ModeDirect {
			via = router.ViaDirect
		} else if via == "" {
			via = def
		}
		nr, err := router.NewRoute(r.Domain, via)
		if err != nil {
			return fmt.Errorf("route %q: %w", r.Domain, err)
		}
		if seen[nr.Domain] {
			continue
		}
		seen[nr.Domain] = true
		routes = append(routes, nr)
	}
	c.Routes = routes
	return c.Validate()
}

func (c Config) Validate() error {
	var errs []error
	if err := ValidateAddr(c.Listen); err != nil {
		errs = append(errs, fmt.Errorf("listen: %w", err))
	}
	switch c.Language {
	case "", "en", "ko":
	default:
		errs = append(errs, fmt.Errorf("language: unsupported %q (en or ko)", c.Language))
	}
	names := map[string]bool{}
	socks := map[string]string{}
	for _, u := range c.Upstreams {
		if err := ValidateUpstream(u); err != nil {
			errs = append(errs, fmt.Errorf("upstream %q: %w", u.Name, err))
			continue
		}
		if names[u.Name] {
			errs = append(errs, fmt.Errorf("upstream %q: %w", u.Name, ErrDuplicateUpstream))
		}
		names[u.Name] = true
		if other, dup := socks[u.Socks]; dup {
			errs = append(errs, fmt.Errorf("upstream %q: %w: %s is also used by %q", u.Name, ErrSocksInUse, u.Socks, other))
		}
		socks[u.Socks] = u.Name
	}
	for _, r := range c.Routes {
		if r.Mode == router.ModeProxy && r.Upstream != "" && !names[r.Upstream] {
			errs = append(errs, fmt.Errorf("route %q: %w %q", r.Domain, ErrUnknownUpstream, r.Upstream))
		}
	}
	if c.Fallback != "" && !names[c.Fallback] {
		errs = append(errs, fmt.Errorf("fallback: %w %q", ErrUnknownUpstream, c.Fallback))
	}
	return errors.Join(errs...)
}

// NormalizeFallback 은 fallback 입력을 정규화한다. "direct" 와 "" 는 모두 DIRECT 인 "" 가 된다.
// route 의 via 와 달리 "" 가 첫 업스트림을 뜻하지 않는다.
func NormalizeFallback(via string) string {
	v := strings.ToLower(strings.TrimSpace(via))
	if v == router.ViaDirect {
		return ""
	}
	return v
}

// FallbackVia 는 fallback 을 route 의 via 처럼 한 단어로 돌려준다: 업스트림 이름 또는 "direct".
func (c Config) FallbackVia() string {
	if c.Fallback == "" {
		return router.ViaDirect
	}
	return c.Fallback
}

var (
	ErrDuplicateUpstream = errors.New("duplicate upstream name")
	ErrUnknownUpstream   = errors.New("unknown upstream")
	ErrSocksInUse        = errors.New("SOCKS address already used by another upstream")
)

// ValidateUpstream 은 업스트림 하나를 다른 업스트림과 무관하게 검사한다.
func ValidateUpstream(u Upstream) error {
	if err := router.ValidateUpstreamName(u.Name); err != nil {
		return err
	}
	switch u.Mode {
	case SSHManaged:
		if u.Host == "" {
			return errors.New("ssh host is required")
		}
		if strings.HasPrefix(u.Host, "-") || strings.ContainsAny(u.Host, " \t\r\n") {
			return fmt.Errorf("invalid ssh host %q", u.Host)
		}
		if strings.HasPrefix(u.User, "-") || strings.ContainsAny(u.User, " \t\r\n@") {
			return fmt.Errorf("invalid ssh user %q", u.User)
		}
	case SSHExternal:
	default:
		return fmt.Errorf("unknown mode %q (managed or external)", u.Mode)
	}
	if u.Port < 0 || u.Port > 65535 {
		return fmt.Errorf("ssh port %d out of range", u.Port)
	}
	if err := ValidateAddr(u.Socks); err != nil {
		return fmt.Errorf("socks: %w", err)
	}
	return nil
}

// ValidateAddr 는 "host:port" 형태의 listen/dial 주소를 검사한다.
func ValidateAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid address %q: %w", addr, err)
	}
	if host == "" {
		return fmt.Errorf("invalid address %q: host is required (use 127.0.0.1)", addr)
	}
	if _, _, err := router.SplitHostPort(net.JoinHostPort(host, port)); err != nil {
		return fmt.Errorf("invalid address %q: %w", addr, err)
	}
	return nil
}

// IsLoopback 은 listen 주소가 로컬 연결만 받는지 보고한다.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
