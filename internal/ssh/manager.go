package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/horyu1234/route-box/internal/logbuf"
	"github.com/horyu1234/route-box/internal/socks"
)

// Config 는 원하는 managed-ssh 상태다. Enabled=false 면 어떤 child 든 멈춘다.
type Config struct {
	Enabled   bool
	Bin       string // ssh 바이너리; 비어 있으면 FindBinary 사용
	Spec      Spec
	Reconnect bool
}

// Timing 은 테스트를 위해 노출된다.
type Timing struct {
	ProbeInterval time.Duration
	ReadyTimeout  time.Duration
	BackoffMin    time.Duration
	BackoffMax    time.Duration
}

func DefaultTiming() Timing {
	return Timing{
		ProbeInterval: 250 * time.Millisecond,
		ReadyTimeout:  30 * time.Second,
		BackoffMin:    time.Second,
		BackoffMax:    30 * time.Second,
	}
}

// Manager 는 한 번에 최대 하나의 ssh child 만 소유한다. 모든 프로세스 상태는
// Run goroutine 안에서만 존재하며, 다른 메서드는 명령을 보내거나 snapshot 을
// 읽을 뿐이다.
type Manager struct {
	notify func(Status)
	timing Timing
	cmds   chan func(*loop)
	stderr *logbuf.Ring[string]

	mu     sync.Mutex
	status Status
}

func NewManager(notify func(Status), timing Timing) *Manager {
	if notify == nil {
		notify = func(Status) {}
	}
	return &Manager{
		notify: notify,
		timing: timing,
		cmds:   make(chan func(*loop), 8),
		stderr: logbuf.New[string](50),
		status: Status{State: StateDisabled, Since: time.Now()},
	}
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Stderr 는 가장 최근의 ssh stderr 라인들을 오래된 순서로 반환한다.
func (m *Manager) Stderr() []string { return m.stderr.Snapshot() }

// Configure 는 cfg 를 적용한다. config 가 바뀌면 child 를 재시작하고, 동일하면
// no-op 이다. Run 이 시작되기 전에 호출해도 안전하다.
func (m *Manager) Configure(cfg Config) {
	m.cmds <- func(l *loop) { l.configure(cfg) }
}

// Restart 는 현재 child 가 있으면 멈추고 새로 시작한다.
func (m *Manager) Restart() {
	m.cmds <- func(l *loop) { l.restart() }
}

type exitMsg struct {
	proc *process
}

type readyMsg struct {
	proc *process
	err  error
}

type loop struct {
	m       *Manager
	ctx     context.Context
	cfg     Config
	proc    *process
	stop    context.CancelFunc
	ready   bool
	attempt int
	retry   *time.Timer
	exits   chan exitMsg
	readies chan readyMsg
}

// Run 은 ctx 가 취소될 때까지 child 를 감독하고, 취소되면 종료시킨 뒤 그것이
// reap 될 때까지 기다렸다가 반환한다.
func (m *Manager) Run(ctx context.Context) {
	l := &loop{
		m:       m,
		ctx:     ctx,
		exits:   make(chan exitMsg, 1),
		readies: make(chan readyMsg, 1),
	}
	defer l.shutdown()
	for {
		var retryC <-chan time.Time
		if l.retry != nil {
			retryC = l.retry.C
		}
		select {
		case <-ctx.Done():
			return
		case fn := <-m.cmds:
			fn(l)
		case msg := <-l.exits:
			l.onExit(msg)
		case msg := <-l.readies:
			l.onReady(msg)
		case <-retryC:
			l.retry = nil
			l.start(StateReconnecting)
		}
	}
}

func (l *loop) set(st Status) {
	st.Since = time.Now()
	if st.Host == "" {
		st.Host = l.cfg.Spec.Host
	}
	l.m.mu.Lock()
	l.m.status = st
	l.m.mu.Unlock()
	l.m.notify(st)
}

func (l *loop) configure(cfg Config) {
	if cfg == l.cfg && (l.proc != nil || l.retry != nil) {
		return
	}
	l.cfg = cfg
	l.restart()
}

func (l *loop) restart() {
	l.kill()
	l.cancelRetry()
	l.attempt = 0
	if !l.cfg.Enabled {
		l.set(Status{State: StateDisabled})
		return
	}
	l.start(StateStarting)
}

func (l *loop) start(state State) {
	l.attempt++
	fail := func(err error) {
		l.m.stderr.Push(err.Error())
		l.failed(err)
	}
	if err := l.cfg.Spec.Validate(); err != nil {
		fail(err)
		return
	}
	bin := l.cfg.Bin
	if bin == "" {
		var err error
		if bin, err = FindBinary(); err != nil {
			fail(err)
			return
		}
	}
	if err := checkPortFree(l.ctx, l.cfg.Spec.SocksAddr); err != nil {
		fail(err)
		return
	}
	args, err := Args(l.cfg.Spec)
	if err != nil {
		fail(err)
		return
	}
	procCtx, stop := context.WithCancel(l.ctx)
	proc, err := startProcess(procCtx, bin, args, l.m.stderr.Push)
	if err != nil {
		stop()
		fail(err)
		return
	}
	l.proc, l.stop, l.ready = proc, stop, false
	l.set(Status{State: state, PID: proc.pid(), Attempt: l.attempt})

	go func() {
		<-proc.done
		select {
		case l.exits <- exitMsg{proc}:
		case <-l.ctx.Done():
		}
	}()
	go l.waitReady(procCtx, proc)
}

// waitReady 는 SOCKS 포트를 폴링한다. ssh 는 인증에 성공한 뒤에만 -D 를 여니,
// SOCKS greeting 이 성공하면 터널이 사용 가능하다는 뜻이다.
func (l *loop) waitReady(ctx context.Context, proc *process) {
	deadline := time.NewTimer(l.m.timing.ReadyTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(l.m.timing.ProbeInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-proc.done:
			return
		case <-deadline.C:
			l.sendReady(ctx, readyMsg{proc, fmt.Errorf("SOCKS port %s did not open within %v", l.cfg.Spec.SocksAddr, l.m.timing.ReadyTimeout)})
			return
		case <-tick.C:
			pctx, cancel := context.WithTimeout(ctx, time.Second)
			err := socks.Probe(pctx, l.cfg.Spec.SocksAddr)
			cancel()
			if err == nil {
				l.sendReady(ctx, readyMsg{proc: proc})
				return
			}
		}
	}
}

func (l *loop) sendReady(ctx context.Context, msg readyMsg) {
	select {
	case l.readies <- msg:
	case <-ctx.Done():
	case <-msg.proc.done:
	}
}

func (l *loop) onReady(msg readyMsg) {
	if msg.proc != l.proc {
		return
	}
	if msg.err != nil {
		l.m.stderr.Push(msg.err.Error())
		l.kill()
		l.failed(msg.err)
		return
	}
	l.ready = true
	l.attempt = 0
	l.set(Status{State: StateConnected, PID: msg.proc.pid()})
}

func (l *loop) onExit(msg exitMsg) {
	if msg.proc != l.proc {
		return
	}
	stop := l.stop
	l.proc, l.stop = nil, nil
	stop()
	l.failed(exitError(msg.proc.err, msg.proc.last()))
}

// failed 는 err 를 기록하고, 활성화돼 있으면 재연결을 예약한다.
func (l *loop) failed(err error) {
	if l.cfg.Reconnect && l.cfg.Enabled {
		delay := backoff(l.attempt, l.m.timing)
		l.cancelRetry()
		l.retry = time.NewTimer(delay)
		l.set(Status{State: StateReconnecting, Attempt: l.attempt, NextRetry: time.Now().Add(delay), Err: err.Error()})
		return
	}
	l.set(Status{State: StateFailed, Err: err.Error()})
}

func (l *loop) kill() {
	if l.proc == nil {
		return
	}
	proc, stop := l.proc, l.stop
	l.proc, l.stop = nil, nil
	stop()
	<-proc.done
}

func (l *loop) cancelRetry() {
	if l.retry != nil {
		l.retry.Stop()
		l.retry = nil
	}
}

func (l *loop) shutdown() {
	l.cancelRetry()
	wasActive := l.proc != nil
	l.kill()
	if wasActive || l.cfg.Enabled {
		l.set(Status{State: StateStopped})
	}
}

func backoff(attempt int, t Timing) time.Duration {
	d := t.BackoffMin
	for i := 1; i < attempt && d < t.BackoffMax; i++ {
		d *= 2
	}
	return min(d, t.BackoffMax)
}

// exitError 는 ssh 가 왜 종료됐는지 요약하는데, 그냥 exit status 보다는
// 마지막 stderr 라인(예: "Permission denied (publickey).")을 우선한다.
func exitError(waitErr error, last string) error {
	switch {
	case last != "" && waitErr != nil:
		return fmt.Errorf("%s (%v)", last, waitErr)
	case last != "":
		return errors.New(last)
	case waitErr != nil:
		return fmt.Errorf("ssh exited: %w", waitErr)
	default:
		return errors.New("ssh exited")
	}
}

// ErrSocksPortInUse 는 SOCKS 주소를 이미 다른 무언가가 listen 중이라는 뜻이다.
var ErrSocksPortInUse = errors.New("SOCKS port already in use")

func checkPortFree(ctx context.Context, addr string) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("%w: %s (another ssh -D? use External SOCKS mode or change the address)", ErrSocksPortInUse, addr)
		}
		return fmt.Errorf("cannot bind SOCKS address %s: %w", addr, err)
	}
	return ln.Close()
}
