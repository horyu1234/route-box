// Package core 는 config, routing, proxy, 업스트림별 ssh supervisor 를 하나의
// App 으로 엮는다. TUI, CLI, 제어 소켓 모두 App 의 메서드로 RouteBox 를
// 움직이므로 모든 진입점이 동일한 동작을 공유한다.
package core

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/horyu1234/route-box/internal/config"
	"github.com/horyu1234/route-box/internal/events"
	"github.com/horyu1234/route-box/internal/logbuf"
	"github.com/horyu1234/route-box/internal/proxy"
	"github.com/horyu1234/route-box/internal/router"
	"github.com/horyu1234/route-box/internal/socks"
	"github.com/horyu1234/route-box/internal/ssh"
	"github.com/horyu1234/route-box/internal/stats"
)

var (
	ErrRouteExists     = errors.New("route already exists")
	ErrRouteNotFound   = errors.New("route not found")
	ErrNoUpstream      = errors.New("no upstream configured")
	ErrNotRunning      = errors.New("RouteBox is not running")
	ErrUpstreamInUse   = errors.New("upstream is still used by routes")
	ErrUpstreamMissing = errors.New("upstream not found")
	ErrNotManaged      = errors.New("upstream is not a managed SSH tunnel")
	ErrNoPendingKey    = errors.New("no fetched host key to trust; fetch it again")
)

const connLogSize = 500

// MigratedNotice 는 예전 단일 업스트림 설정을 옮겼을 때 한 번 알리는 문구다.
const MigratedNotice = `Converted the single-upstream config to an upstream named "default"; the file is updated on the next change.`

type Options struct {
	ConfigPath string
	Config     config.Config
	// FirstRun 은 아직 config 파일이 없었을 때 true 다.
	FirstRun bool
	// ListenOverride 와 SocksOverride 는 CLI 플래그에서 오며 이번 실행에만
	// 적용되고 config 파일에는 기록되지 않는다. SocksOverride 는 첫(기본)
	// 업스트림에만 적용된다.
	ListenOverride string
	SocksOverride  string

	Proxy        proxy.Options
	SSHTiming    ssh.Timing
	SSHBin       string
	Direct       *net.Dialer
	HealthPeriod time.Duration
}

// Health 는 가장 최근 SOCKS reachability probe 의 결과다.
type Health struct {
	Checked   time.Time `json:"checked,omitzero"`
	Reachable bool      `json:"reachable"`
	Err       string    `json:"error,omitempty"`
}

type upstreamRT struct {
	mgr    *ssh.Manager
	cancel context.CancelFunc
}

type App struct {
	opts      Options
	store     *config.Store
	router    *router.Router
	transport *proxy.Transport
	stats     *stats.Stats
	bus       *events.Bus
	conns     *logbuf.Ring[events.ConnectionEvent]
	healthNow chan struct{}
	upWG      sync.WaitGroup

	mu            sync.Mutex
	socksOverride string
	// overrideName 은 --socks 가 적용되는 업스트림이다. 시작 시점의 첫 업스트림에
	// 고정해서, 그 업스트림이 지워져도 다른 업스트림으로 옮겨 가지 않게 한다.
	overrideName string
	runCtx       context.Context
	ups          map[string]*upstreamRT
	health       map[string]Health
	// pendingKeys 는 사용자가 확인 중인 host key 다. 신뢰는 여기 있는 키와
	// 지문이 같을 때만 한다: 소켓 너머로 known_hosts 줄을 받지 않는다.
	pendingKeys map[string]ssh.HostKey
	started     time.Time
	proxyUp     bool
	proxyErr    error
	listenAddr  string
}

func New(opts Options) *App {
	if opts.Proxy == (proxy.Options{}) {
		opts.Proxy = proxy.DefaultOptions()
	}
	if opts.SSHTiming == (ssh.Timing{}) {
		opts.SSHTiming = ssh.DefaultTiming()
	}
	if opts.HealthPeriod == 0 {
		opts.HealthPeriod = 5 * time.Second
	}
	cfg := opts.Config.Clone()
	a := &App{
		opts:          opts,
		store:         config.NewStore(opts.ConfigPath, cfg),
		router:        router.New(cfg.Routes),
		stats:         &stats.Stats{},
		bus:           events.NewBus(),
		conns:         logbuf.New[events.ConnectionEvent](connLogSize),
		healthNow:     make(chan struct{}, 1),
		socksOverride: opts.SocksOverride,
		overrideName:  cfg.DefaultUpstream(),
		ups:           map[string]*upstreamRT{},
		health:        map[string]Health{},
		pendingKeys:   map[string]ssh.HostKey{},
	}
	a.transport = proxy.NewTransport(opts.Direct, nil)
	a.syncUpstreams(cfg)
	return a
}

// effectiveUpstreams 는 --socks override 를 첫 업스트림에 적용한 목록이다.
func (a *App) effectiveUpstreams(cfg config.Config) []config.Upstream {
	ups := append([]config.Upstream(nil), cfg.Upstreams...)
	a.mu.Lock()
	override, name := a.socksOverride, a.overrideName
	a.mu.Unlock()
	if override == "" {
		return ups
	}
	for i := range ups {
		if ups[i].Name == name {
			ups[i].Socks = override
		}
	}
	return ups
}

// ListenAddr 는 실제로 적용되는 프록시 listen 주소다.
func (a *App) ListenAddr() string {
	if a.opts.ListenOverride != "" {
		return a.opts.ListenOverride
	}
	return a.store.Get().Listen
}

func (a *App) ConfigPath() string    { return a.store.Path() }
func (a *App) FirstRun() bool        { return a.opts.FirstRun }
func (a *App) Config() config.Config { return a.store.Get() }

// Warnings 는 사용자가 알아야 할 설정상의 위험을 나열한다.
func (a *App) Warnings() []string {
	var w []string
	if !config.IsLoopback(a.ListenAddr()) {
		w = append(w, "RouteBox is listening on a non-loopback address. This may expose an open proxy to your network.")
	}
	return w
}

func (a *App) Subscribe(buffer int) (<-chan events.Event, func()) {
	return a.bus.Subscribe(buffer)
}

func (a *App) publish(e events.Event) {
	if ce, ok := e.(events.ConnectionEvent); ok {
		if ce.State == events.ConnOpen || !a.conns.Update(
			func(x events.ConnectionEvent) bool { return x.ID == ce.ID },
			func(x *events.ConnectionEvent) { *x = ce },
		) {
			a.conns.Push(ce)
		}
	}
	a.bus.Publish(e)
}

// Run 은 ctx 가 취소될 때까지 프록시를 서비스하고 업스트림을 감독한다.
// 프록시 포트를 바인딩하지 못하면 바로 실패한다. 반환 시점에는 모든 ssh
// 자식이 회수돼 있다.
func (a *App) Run(ctx context.Context) error {
	defer a.bus.Close()
	addr := a.ListenAddr()
	ln, err := proxy.Listen(addr)
	if err != nil {
		a.setProxy(false, err, "")
		a.publish(events.ProxyStopped{Time: time.Now(), Err: err})
		return err
	}
	a.mu.Lock()
	a.started = time.Now()
	a.runCtx = ctx
	a.mu.Unlock()
	a.setProxy(true, nil, ln.Addr().String())
	a.publish(events.ProxyStarted{Time: time.Now(), Addr: ln.Addr().String()})
	for _, w := range a.Warnings() {
		a.publish(events.Notice{Time: time.Now(), Message: w, Warning: true})
	}
	if a.opts.Config.Migrated {
		a.publish(events.Notice{Time: time.Now(), Message: MigratedNotice})
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.healthLoop(ctx)
	}()
	a.syncUpstreams(a.store.Get())

	srv := proxy.NewServer(a.router, a.transport, a.stats, a.publish, a.opts.Proxy)
	serveErr := srv.Serve(ctx, ln)
	wg.Wait()
	a.upWG.Wait()
	a.mu.Lock()
	a.runCtx = nil
	a.ups = map[string]*upstreamRT{}
	a.mu.Unlock()
	a.setProxy(false, serveErr, "")
	a.publish(events.ProxyStopped{Time: time.Now(), Err: serveErr})
	return serveErr
}

func (a *App) setProxy(up bool, err error, addr string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.proxyUp, a.proxyErr = up, err
	if addr != "" {
		a.listenAddr = addr
	}
}

func (a *App) running() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.proxyUp
}

func (a *App) sshConfig(u config.Upstream) ssh.Config {
	return ssh.Config{
		Enabled: u.Mode == config.SSHManaged && u.Host != "",
		Bin:     a.opts.SSHBin,
		Spec: ssh.Spec{
			Host:         u.Host,
			User:         u.User,
			Port:         u.Port,
			IdentityFile: u.IdentityFile,
			SocksAddr:    u.Socks,
		},
		Reconnect: u.Reconnect,
	}
}

// syncUpstreams 는 transport 의 SOCKS 표를 갱신하고, 실행 중이면 업스트림마다
// ssh 관리자를 맞춘다(새 업스트림은 시작, 사라진 업스트림은 정지).
func (a *App) syncUpstreams(cfg config.Config) {
	ups := a.effectiveUpstreams(cfg)
	addrs := make(map[string]string, len(ups))
	for _, u := range ups {
		addrs[u.Name] = u.Socks
	}
	a.transport.SetUpstreams(addrs)

	type job struct {
		mgr *ssh.Manager
		cfg ssh.Config
	}
	var jobs []job
	a.mu.Lock()
	if a.runCtx == nil || a.runCtx.Err() != nil {
		a.mu.Unlock()
		return
	}
	for _, u := range ups {
		rt, ok := a.ups[u.Name]
		if !ok {
			rt = a.startUpstreamLocked(u.Name)
		}
		jobs = append(jobs, job{rt.mgr, a.sshConfig(u)})
	}
	for name, rt := range a.ups {
		if _, ok := addrs[name]; !ok {
			rt.cancel()
			delete(a.ups, name)
			delete(a.health, name)
		}
	}
	a.mu.Unlock()
	// Manager 의 명령 채널은 Run 루프가 읽기 시작한 뒤에만 비워지므로 락 밖에서 보낸다.
	for _, j := range jobs {
		j.mgr.Configure(j.cfg)
	}
	a.checkHealthSoon()
}

func (a *App) startUpstreamLocked(name string) *upstreamRT {
	ctx, cancel := context.WithCancel(a.runCtx)
	mgr := ssh.NewManager(func(st ssh.Status) {
		a.publish(events.SSHStateChanged{Time: time.Now(), Upstream: name, Status: st})
		if st.State == ssh.StateConnected {
			a.checkHealthSoon()
		}
	}, a.opts.SSHTiming)
	rt := &upstreamRT{mgr: mgr, cancel: cancel}
	a.ups[name] = rt
	a.upWG.Add(1)
	go func() {
		defer a.upWG.Done()
		mgr.Run(ctx)
	}()
	return rt
}

func (a *App) checkHealthSoon() {
	select {
	case a.healthNow <- struct{}{}:
	default:
	}
}

func (a *App) healthLoop(ctx context.Context) {
	t := time.NewTicker(a.opts.HealthPeriod)
	defer t.Stop()
	for {
		a.probeAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-a.healthNow:
		}
	}
}

func (a *App) probeAll(ctx context.Context) {
	ups := a.effectiveUpstreams(a.store.Get())
	var wg sync.WaitGroup
	for _, u := range ups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err := socks.Probe(pctx, u.Socks)
			cancel()
			if ctx.Err() != nil {
				return
			}
			h := Health{Checked: time.Now(), Reachable: err == nil}
			if err != nil {
				h.Err = err.Error()
			}
			a.setHealth(u.Name, h)
		}()
	}
	wg.Wait()
}

func (a *App) setHealth(name string, h Health) {
	a.mu.Lock()
	if _, ok := a.ups[name]; !ok && a.runCtx != nil {
		a.mu.Unlock()
		return
	}
	prev, had := a.health[name]
	a.health[name] = h
	a.mu.Unlock()
	if !had || prev.Reachable != h.Reachable || prev.Err != h.Err {
		var err error
		if h.Err != "" {
			err = errors.New(h.Err)
		}
		a.publish(events.UpstreamHealth{Time: h.Checked, Upstream: name, Reachable: h.Reachable, Err: err})
	}
}

// RestartUpstream 은 managed 업스트림의 ssh 를 재시작하고 external 은 다시
// 확인한다. name 이 비어 있으면 전부 대상이다.
func (a *App) RestartUpstream(name string) error {
	cfg := a.store.Get()
	if !cfg.UpstreamConfigured() {
		return ErrNoUpstream
	}
	if name != "" {
		if _, ok := cfg.Upstream(name); !ok {
			return errorf(ErrUpstreamMissing, name)
		}
	}
	a.mu.Lock()
	if !a.proxyUp {
		a.mu.Unlock()
		return ErrNotRunning
	}
	var mgrs []*ssh.Manager
	for _, u := range cfg.Upstreams {
		if (name == "" || u.Name == name) && u.Mode == config.SSHManaged {
			if rt, ok := a.ups[u.Name]; ok {
				mgrs = append(mgrs, rt.mgr)
			}
		}
	}
	a.mu.Unlock()
	for _, m := range mgrs {
		m.Restart()
	}
	a.checkHealthSoon()
	return nil
}

// SSHLog 는 업스트림의 최근 ssh stderr 줄을 돌려준다.
func (a *App) SSHLog(name string) []string {
	a.mu.Lock()
	rt, ok := a.ups[name]
	a.mu.Unlock()
	if !ok {
		return nil
	}
	return rt.mgr.Stderr()
}

func (a *App) RecentConnections() []events.ConnectionEvent { return a.conns.Snapshot() }

// ScanHostKey 는 managed 업스트림 서버의 host key 를 받아 와 사용자가 확인하도록
// 돌려준다. 이 프로세스(서비스로 돌 때는 데몬)의 ssh 환경에서 실행된다.
func (a *App) ScanHostKey(ctx context.Context, name string) (ssh.HostKey, error) {
	u, ok := a.effectiveUpstream(name)
	if !ok {
		return ssh.HostKey{}, errorf(ErrUpstreamMissing, name)
	}
	if u.Mode != config.SSHManaged || u.Host == "" {
		return ssh.HostKey{}, errorf(ErrNotManaged, name)
	}
	k, err := ssh.ScanHostKey(ctx, a.opts.SSHBin, a.sshConfig(u).Spec)
	if err != nil {
		return ssh.HostKey{}, err
	}
	a.mu.Lock()
	a.pendingKeys[name] = k
	a.mu.Unlock()
	return k, nil
}

// TrustHostKey 는 방금 ScanHostKey 로 보여 준 키(fingerprint 로 확인)를
// known_hosts 에 저장하고, 실행 중이면 그 업스트림을 다시 연결한다.
func (a *App) TrustHostKey(name, fingerprint string) error {
	a.mu.Lock()
	k, ok := a.pendingKeys[name]
	if ok && k.Fingerprint() == fingerprint {
		delete(a.pendingKeys, name)
	}
	a.mu.Unlock()
	if !ok || k.Fingerprint() != fingerprint {
		return ErrNoPendingKey
	}
	if err := ssh.TrustHostKey(k); err != nil {
		return err
	}
	if a.running() {
		return a.RestartUpstream(name)
	}
	return nil
}

func (a *App) effectiveUpstream(name string) (config.Upstream, bool) {
	for _, u := range a.effectiveUpstreams(a.store.Get()) {
		if u.Name == name {
			return u, true
		}
	}
	return config.Upstream{}, false
}
