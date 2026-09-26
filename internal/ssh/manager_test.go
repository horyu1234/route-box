package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// 테스트 바이너리 자신이 가짜 ssh 역할도 겸한다: ROUTEBOX_FAKE_SSH 로 동작을 고른다.
func TestMain(m *testing.M) {
	if mode := os.Getenv("ROUTEBOX_FAKE_SSH"); mode != "" {
		os.Exit(fakeSSH(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeSSH(mode string, args []string) int {
	switch mode {
	case "authfail":
		fmt.Fprintln(os.Stderr, "user@host: Permission denied (publickey).")
		return 255
	case "ignoreterm":
		signal.Ignore(syscall.SIGTERM)
	}
	i := slices.Index(args, "-D")
	if i < 0 || i+1 >= len(args) {
		fmt.Fprintln(os.Stderr, "fake ssh: missing -D")
		return 2
	}
	ln, err := net.Listen("tcp", args[i+1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "bind [%s]: Address already in use\n", args[i+1])
		return 255
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 3)
				if _, err := io.ReadFull(c, buf); err == nil {
					_, _ = c.Write([]byte{5, 0})
				}
			}()
		}
	}()
	if mode == "dieafter" {
		time.Sleep(300 * time.Millisecond)
		fmt.Fprintln(os.Stderr, "Connection to host closed by remote host.")
		return 255
	}
	if mode == "ignoreterm" {
		select {}
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	<-sig
	return 0
}

type recorder struct {
	mu     sync.Mutex
	states []Status
}

func (r *recorder) notify(s Status) {
	r.mu.Lock()
	r.states = append(r.states, s)
	r.mu.Unlock()
}

func (r *recorder) waitFor(t *testing.T, fn func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		for _, s := range r.states {
			if fn(s) {
				r.mu.Unlock()
				return s
			}
		}
		r.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Fatalf("state not reached; history: %+v", r.states)
	return Status{}
}

func (r *recorder) reset() {
	r.mu.Lock()
	r.states = nil
	r.mu.Unlock()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func testTiming() Timing {
	return Timing{
		ProbeInterval: 20 * time.Millisecond,
		ReadyTimeout:  5 * time.Second,
		BackoffMin:    50 * time.Millisecond,
		BackoffMax:    200 * time.Millisecond,
	}
}

func startManager(t *testing.T, mode string, reconnect bool) (*Manager, *recorder, context.CancelFunc, chan struct{}, string) {
	t.Helper()
	t.Setenv("ROUTEBOX_FAKE_SSH", mode)
	rec := &recorder{}
	m := NewManager(rec.notify, testTiming())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()
	addr := freeAddr(t)
	m.Configure(Config{
		Enabled:   true,
		Bin:       os.Args[0],
		Spec:      Spec{Host: "proxy-seoul", SocksAddr: addr},
		Reconnect: reconnect,
	})
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return m, rec, cancel, done, addr
}

func assertReaped(t *testing.T, pid int) {
	t.Helper()
	// zombie 상태에서도 signal 0 은 여전히 받아들여진다; reap 된 pid 만 ESRCH 를 반환한다.
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("pid %d still exists (err=%v)", pid, err)
	}
}

func TestManagerConnectsAndStopsChildOnShutdown(t *testing.T) {
	m, rec, cancel, done, _ := startManager(t, "ok", true)
	st := rec.waitFor(t, func(s Status) bool { return s.State == StateConnected })
	if st.PID == 0 || st.Host != "proxy-seoul" {
		t.Fatalf("status = %+v", st)
	}
	if m.Status().State != StateConnected {
		t.Fatalf("Status() = %+v", m.Status())
	}
	cancel()
	<-done
	if m.Status().State != StateStopped {
		t.Fatalf("after shutdown: %+v", m.Status())
	}
	assertReaped(t, st.PID)
}

func TestManagerAuthFailureWithoutReconnect(t *testing.T) {
	m, rec, _, _, _ := startManager(t, "authfail", false)
	st := rec.waitFor(t, func(s Status) bool { return s.State == StateFailed })
	if !strings.Contains(st.Err, "Permission denied (publickey)") {
		t.Fatalf("err = %q", st.Err)
	}
	if !slices.ContainsFunc(m.Stderr(), func(l string) bool { return strings.Contains(l, "Permission denied") }) {
		t.Fatalf("stderr = %q", m.Stderr())
	}
}

func TestManagerReconnectsAfterChildDies(t *testing.T) {
	_, rec, _, _, _ := startManager(t, "dieafter", true)
	first := rec.waitFor(t, func(s Status) bool { return s.State == StateConnected })
	rec.waitFor(t, func(s Status) bool {
		return s.State == StateReconnecting && strings.Contains(s.Err, "closed by remote host")
	})
	second := rec.waitFor(t, func(s Status) bool { return s.State == StateConnected && s.PID != first.PID })
	if second.PID == first.PID {
		t.Fatal("expected a new child")
	}
	assertReaped(t, first.PID)
}

func TestManagerRestartReplacesChild(t *testing.T) {
	m, rec, _, _, _ := startManager(t, "ok", true)
	first := rec.waitFor(t, func(s Status) bool { return s.State == StateConnected })
	rec.reset()
	m.Restart()
	second := rec.waitFor(t, func(s Status) bool { return s.State == StateConnected })
	if second.PID == first.PID {
		t.Fatal("restart kept the same child")
	}
	assertReaped(t, first.PID)
}

func TestManagerDisableStopsChild(t *testing.T) {
	m, rec, _, _, addr := startManager(t, "ok", true)
	first := rec.waitFor(t, func(s Status) bool { return s.State == StateConnected })
	m.Configure(Config{Enabled: false, Spec: Spec{SocksAddr: addr}})
	rec.waitFor(t, func(s Status) bool { return s.State == StateDisabled })
	assertReaped(t, first.PID)
}

func TestManagerSocksPortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("ROUTEBOX_FAKE_SSH", "ok")
	rec := &recorder{}
	m := NewManager(rec.notify, testTiming())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	m.Configure(Config{Enabled: true, Bin: os.Args[0], Spec: Spec{Host: "h", SocksAddr: ln.Addr().String()}})
	st := rec.waitFor(t, func(s Status) bool { return s.State == StateFailed })
	if !strings.Contains(st.Err, "already in use") {
		t.Fatalf("err = %q", st.Err)
	}
}

func TestManagerEscalatesToKill(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the SIGKILL grace period")
	}
	_, rec, cancel, done, _ := startManager(t, "ignoreterm", true)
	st := rec.waitFor(t, func(s Status) bool { return s.State == StateConnected })
	start := time.Now()
	cancel()
	<-done
	if d := time.Since(start); d < killGrace-500*time.Millisecond {
		t.Fatalf("returned after %v, before the kill grace", d)
	}
	assertReaped(t, st.PID)
}

func TestArgs(t *testing.T) {
	home, _ := os.UserHomeDir()
	args, err := Args(Spec{Host: "proxy-seoul", User: "me", Port: 2222, IdentityFile: "~/.ssh/id_ed25519", SocksAddr: "127.0.0.1:1080"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-N -D 127.0.0.1:1080", "ExitOnForwardFailure=yes", "ServerAliveInterval=30",
		"ServerAliveCountMax=3", "BatchMode=yes", "-p 2222", "-l me",
		"-i " + home + "/.ssh/id_ed25519",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
	if !strings.HasSuffix(joined, "-- proxy-seoul") {
		t.Errorf("host must come last after --: %q", joined)
	}

	minimal, _ := Args(Spec{Host: "proxy-seoul", SocksAddr: "127.0.0.1:1080"})
	for _, flag := range []string{"-p", "-l", "-i"} {
		if slices.Contains(minimal, flag) {
			t.Errorf("unset option %s passed; it would override ~/.ssh/config", flag)
		}
	}
}

func TestSpecValidate(t *testing.T) {
	for _, s := range []Spec{
		{Host: "", SocksAddr: "a:1"},
		{Host: "-oProxyCommand=evil", SocksAddr: "a:1"},
		{Host: "a b", SocksAddr: "a:1"},
		{Host: "h", User: "-x", SocksAddr: "a:1"},
		{Host: "h", Port: 70000, SocksAddr: "a:1"},
		{Host: "h"},
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("%+v accepted", s)
		}
	}
}

func TestBackoff(t *testing.T) {
	tm := Timing{BackoffMin: time.Second, BackoffMax: 30 * time.Second}
	for attempt, want := range map[int]time.Duration{0: time.Second, 1: time.Second, 2: 2 * time.Second, 4: 8 * time.Second, 10: 30 * time.Second} {
		if got := backoff(attempt, tm); got != want {
			t.Errorf("backoff(%d) = %v, want %v", attempt, got, want)
		}
	}
}

// 로그인 거절은 다시 시도해도 풀리지 않고, 짧은 간격으로 반복하면 sshd 의
// PerSourcePenalties 같은 차단을 부른다. 그래서 첫 재시도부터 최대 간격을 쓴다.
func TestAuthFailureRetriesAtMaxBackoff(t *testing.T) {
	_, rec, _, _, _ := startManager(t, "authfail", true)
	st := rec.waitFor(t, func(s Status) bool { return s.State == StateReconnecting })
	if st.Attempt != 1 || st.NextRetry.Sub(st.Since) < testTiming().BackoffMax-10*time.Millisecond {
		t.Fatalf("first retry after an auth failure waits %v (attempt %d), want the max backoff %v",
			st.NextRetry.Sub(st.Since), st.Attempt, testTiming().BackoffMax)
	}
}

func TestTransientDisconnectRetriesQuickly(t *testing.T) {
	_, rec, _, _, _ := startManager(t, "dieafter", true)
	st := rec.waitFor(t, func(s Status) bool { return s.State == StateReconnecting })
	if wait := st.NextRetry.Sub(st.Since); wait > testTiming().BackoffMin+20*time.Millisecond {
		t.Fatalf("a dropped tunnel should come back quickly, waits %v", wait)
	}
}

func TestClassifyFailure(t *testing.T) {
	for msg, want := range map[string]Failure{
		"Host key verification failed. (exit status 255)":                                 FailHostKey,
		"horyu@example.com: Permission denied (publickey). (exit status 255)":             FailAuth,
		"Received disconnect from 192.0.2.1 port 22:2: Too many authentication failures":  FailAuth,
		"Connection closed by 192.0.2.1 port 40056 (exit status 255)":                     FailRefused,
		"kex_exchange_identification: Connection closed by remote host":                   FailRefused,
		"Connection reset by 192.0.2.1 port 22 (exit status 255)":                         FailRefused,
		"Connection to example.com closed by remote host. (exit status 255)":              FailOther,
		"ssh: connect to host example.com port 22: Connection refused (exit status 255)":  FailOther,
		"ssh: Could not resolve hostname example.invalid: nodename nor servname provided": FailOther,
	} {
		if got := ClassifyFailure(msg); got != want {
			t.Errorf("ClassifyFailure(%q) = %v, want %v", msg, got, want)
		}
	}
}
