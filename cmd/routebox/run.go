package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/horyu1234/route-box/internal/config"
	"github.com/horyu1234/route-box/internal/control"
	"github.com/horyu1234/route-box/internal/core"
	"github.com/horyu1234/route-box/internal/events"
	"github.com/horyu1234/route-box/internal/logfile"
	"github.com/horyu1234/route-box/internal/router"
	"github.com/horyu1234/route-box/internal/ssh"
	"github.com/horyu1234/route-box/internal/tui"
)

type runFlags struct {
	noTUI   bool
	listen  string
	socks   string
	presets []string
	lang    string
	logFile string
}

type loaded struct {
	path     string
	cfg      config.Config
	firstRun bool
	err      error // 파일은 존재하지만 사용할 수 없을 때 설정된다
}

func loadConfig(path string) (loaded, error) {
	if path == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return loaded{}, err
		}
		path = p
	}
	cfg, err := config.Load(path)
	switch {
	case err == nil:
		return loaded{path: path, cfg: cfg}, nil
	case errors.Is(err, fs.ErrNotExist):
		return loaded{path: path, cfg: cfg, firstRun: true}, nil
	default:
		return loaded{path: path, cfg: config.Default(), err: err}, nil
	}
}

func validateOverrides(f runFlags) error {
	if f.listen != "" {
		if err := config.ValidateAddr(f.listen); err != nil {
			return fmt.Errorf("--listen: %w", err)
		}
	}
	if f.socks != "" {
		if err := config.ValidateAddr(f.socks); err != nil {
			return fmt.Errorf("--socks: %w", err)
		}
	}
	return nil
}

// runMain 은 TUI 와 함께, 또는 --no-tui 로 headless 상태로 RouteBox 를 시작한다.
func runMain(configPath string, f runFlags) error {
	if err := validateOverrides(f); err != nil {
		return err
	}
	ld, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	sockPath := control.SocketPath(ld.path)
	// 이미 (보통 백그라운드 서비스로) 돌고 있으면 관리 패널로만 붙는다.
	if !f.noTUI {
		c := control.NewClient(sockPath)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, err := c.Status(ctx)
		cancel()
		if err == nil {
			return runAttached(c, f)
		}
	}
	if ld.err != nil && (f.noTUI || len(f.presets) > 0) {
		return fmt.Errorf("%w\n(the file was not modified; fix or remove it and try again)", ld.err)
	}

	ln, err := control.Listen(sockPath)
	if err != nil {
		if errors.Is(err, control.ErrAlreadyRunning) {
			return fmt.Errorf("%w; use `routebox status` or stop it first", control.ErrAlreadyRunning)
		}
		return err
	}

	app := core.New(core.Options{
		ConfigPath:     ld.path,
		Config:         ld.cfg,
		FirstRun:       ld.firstRun,
		ListenOverride: f.listen,
		SocksOverride:  f.socks,
	})
	for _, p := range f.presets {
		added, err := app.AddPreset(p, "")
		if err != nil {
			_ = ln.Close()
			return err
		}
		if f.noTUI {
			log.Printf("preset %s: added %d routes", p, len(added))
		}
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stopSignals()
	rt := newRuntime(app, ln)

	if f.noTUI {
		if f.logFile != "" {
			lf, err := logfile.Open(f.logFile, 0)
			if err != nil {
				_ = ln.Close()
				return err
			}
			defer lf.Close()
			log.SetOutput(lf)
			log.SetFlags(log.Ldate | log.Ltime)
		} else {
			log.SetFlags(log.Ltime)
		}
		return runHeadless(ctx, app, rt)
	}
	// TUI 가 뜨기 전에 먼저 서비스를 시작한다: 프록시가 터미널 준비를 기다려서는 안 된다.
	if ld.err == nil {
		rt.start()
	}

	tuiErr := tui.Run(ctx, tui.Options{
		App:      app,
		Start:    rt.start,
		Shutdown: rt.stop,
		LoadErr:  ld.err,
		Lang:     f.lang,
		Backup:   func() (string, error) { return config.Backup(ld.path) },
	})
	runErr := rt.stopAndWait()
	if tuiErr != nil {
		return tuiErr
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return runErr
	}
	return nil
}

var _ tui.Backend = (*control.Remote)(nil)

// runAttached 는 이미 실행 중인 인스턴스에 TUI 를 관리 패널로 붙인다. 패널을
// 닫아도 인스턴스는 계속 돈다.
func runAttached(c *control.Client, f runFlags) error {
	if f.listen != "" || f.socks != "" {
		return errors.New("--listen and --socks apply only when RouteBox starts, and it is already running (stop it first, e.g. `routebox service stop`)")
	}
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stopSignals()
	for _, p := range f.presets {
		if _, err := c.AddPreset(ctx, p, ""); err != nil {
			return err
		}
	}
	remote, err := control.NewRemote(ctx, c)
	if err != nil {
		return err
	}
	defer remote.Close()
	return tui.Run(ctx, tui.Options{App: remote, Attached: true, Lang: f.lang})
}

// runtime 은 실행 중인 인스턴스 뒤에서 도는 goroutine들을 소유한다: proxy/ssh
// core 와 제어 소켓. stopAndWait 은 ssh 자식 프로세스가 회수(reap)된 뒤에만 반환한다.
type runtime struct {
	app    *core.App
	ln     net.Listener
	ctx    context.Context
	cancel context.CancelFunc

	once    sync.Once
	wg      sync.WaitGroup
	mu      sync.Mutex
	runErr  error
	started bool
}

func newRuntime(app *core.App, ln net.Listener) *runtime {
	ctx, cancel := context.WithCancel(context.Background())
	return &runtime{app: app, ln: ln, ctx: ctx, cancel: cancel}
}

func (r *runtime) start() {
	r.once.Do(func() {
		r.mu.Lock()
		r.started = true
		r.mu.Unlock()
		r.wg.Add(2)
		go func() {
			defer r.wg.Done()
			err := r.app.Run(r.ctx)
			r.mu.Lock()
			r.runErr = err
			r.mu.Unlock()
			r.cancel()
		}()
		go func() {
			defer r.wg.Done()
			_ = control.Serve(r.ctx, r.ln, r.app)
		}()
	})
}

func (r *runtime) stop() { r.cancel() }

func (r *runtime) done() <-chan struct{} { return r.ctx.Done() }

func (r *runtime) stopAndWait() error {
	r.cancel()
	r.mu.Lock()
	started := r.started
	r.mu.Unlock()
	if !started {
		_ = r.ln.Close()
		return nil
	}
	finished := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		return errors.New("timed out waiting for shutdown")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runErr
}

func runHeadless(ctx context.Context, app *core.App, rt *runtime) error {
	sub, unsub := app.Subscribe(1024)
	defer unsub()
	cfg := app.Config()
	log.Printf("RouteBox %s — config %s", version, app.ConfigPath())
	for _, w := range app.Warnings() {
		log.Printf("WARNING: %s", w)
	}
	if !cfg.UpstreamConfigured() {
		log.Printf("no upstream configured: proxied routes will fail until one is added (routebox upstream add ...)")
	}
	rt.start()

	logEvents := make(chan struct{})
	go func() {
		defer close(logEvents)
		for e := range sub {
			if line := formatEvent(e); line != "" {
				log.Print(line)
			}
		}
	}()
	select {
	case <-ctx.Done():
		log.Printf("shutting down…")
	case <-rt.done():
	}
	err := rt.stopAndWait()
	unsub()
	<-logEvents
	return err
}

func formatEvent(e events.Event) string {
	switch e := e.(type) {
	case events.ProxyStarted:
		return "proxy listening on " + e.Addr
	case events.ProxyStopped:
		if e.Err != nil {
			return "proxy stopped: " + e.Err.Error()
		}
		return "proxy stopped"
	case events.ConnectionEvent:
		target := net.JoinHostPort(e.Host, e.Port)
		if e.Method == "HTTP" {
			target = "http://" + target
		}
		via := "DIRECT"
		if e.Route == router.ModeProxy {
			via = "→" + e.Upstream
		}
		switch {
		case e.State == events.ConnOpen, e.State == events.ConnClosed && e.Method == "HTTP" && e.Error == nil:
			return fmt.Sprintf("%-10s %s", via, target)
		case e.State == events.ConnFailed || e.Error != nil:
			return fmt.Sprintf("%-10s %s  FAILED: %v", via, target, e.Error)
		}
	case events.SSHStateChanged:
		st, name := e.Status, e.Upstream
		switch st.State {
		case ssh.StateConnected:
			return fmt.Sprintf("[%s] ssh %s connected (pid %d)", name, st.Host, st.PID)
		case ssh.StateStarting:
			return fmt.Sprintf("[%s] ssh %s starting (pid %d)", name, st.Host, st.PID)
		case ssh.StateReconnecting:
			if st.NextRetry.IsZero() {
				return fmt.Sprintf("[%s] ssh %s reconnecting (pid %d, attempt %d)", name, st.Host, st.PID, st.Attempt)
			}
			return fmt.Sprintf("[%s] ssh %s failed: %s — retrying at %s", name, st.Host, st.Err, st.NextRetry.Format("15:04:05"))
		case ssh.StateFailed:
			return fmt.Sprintf("[%s] ssh %s failed: %s", name, st.Host, st.Err)
		case ssh.StateStopped:
			return fmt.Sprintf("[%s] ssh stopped", name)
		}
	case events.UpstreamHealth:
		if e.Reachable {
			return fmt.Sprintf("[%s] SOCKS reachable", e.Upstream)
		}
		return fmt.Sprintf("[%s] SOCKS unreachable: %v", e.Upstream, e.Err)
	case events.RouteAdded:
		via := e.Route.Via()
		if via == "" {
			via = "first upstream"
		}
		return "route added: " + e.Route.Domain + " → " + via
	case events.RouteRemoved:
		return "route removed: " + e.Route.Domain
	case events.Notice:
		if e.Warning {
			return "WARNING: " + e.Message
		}
		return e.Message
	case events.ErrorEvent:
		return fmt.Sprintf("error (%s): %v", e.Source, e.Err)
	}
	return ""
}
