// Package ssh 는 managed 모드에서 SOCKS5 upstream 을 제공하는 "ssh -N -D"
// child 프로세스를 감독한다.
package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Spec 은 ssh 커맨드라인을 만드는 재료다. 빈 필드와 Port 0 은 생략되어
// ~/.ssh/config 가 그 값을 결정하게 둔다.
type Spec struct {
	Host         string
	User         string
	Port         int
	IdentityFile string
	SocksAddr    string
}

func (s Spec) Validate() error {
	switch {
	case s.Host == "":
		return errors.New("ssh host is empty")
	case strings.HasPrefix(s.Host, "-") || strings.ContainsAny(s.Host, " \t\r\n"):
		return fmt.Errorf("invalid ssh host %q", s.Host)
	case strings.HasPrefix(s.User, "-") || strings.ContainsAny(s.User, " \t\r\n@"):
		return fmt.Errorf("invalid ssh user %q", s.User)
	case s.Port < 0 || s.Port > 65535:
		return fmt.Errorf("invalid ssh port %d", s.Port)
	case s.SocksAddr == "":
		return errors.New("local SOCKS address is empty")
	}
	return nil
}

// FindBinary 는 ssh 클라이언트를 찾는다.
func FindBinary() (string, error) {
	if p, err := exec.LookPath("ssh"); err == nil {
		return p, nil
	}
	for _, p := range []string{"/usr/bin/ssh", "/usr/local/bin/ssh", "/opt/homebrew/bin/ssh"} {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("ssh client not found in PATH")
}

// Args 는 ssh 인자 목록을 만든다. BatchMode 는 TUI 가 터미널을 차지하고 있는
// 동안 ssh 가 비밀번호나 host key 를 프롬프트하지 않게 막는다.
func Args(s Spec) ([]string, error) {
	args := []string{
		"-N",
		"-D", s.SocksAddr,
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
	}
	if s.Port > 0 {
		args = append(args, "-p", strconv.Itoa(s.Port))
	}
	if s.User != "" {
		args = append(args, "-l", s.User)
	}
	if s.IdentityFile != "" {
		p, err := ExpandHome(s.IdentityFile)
		if err != nil {
			return nil, err
		}
		args = append(args, "-i", p)
	}
	return append(args, "--", s.Host), nil
}

// ExpandHome 은 앞에 붙은 "~/" 를 사용자 홈 디렉터리 기준으로 풀어준다.
func ExpandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand %q: %w", p, err)
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}

type process struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error // done 이 닫힌 뒤에만 유효하다

	mu       sync.Mutex
	lastLine string
}

func (p *process) pid() int { return p.cmd.Process.Pid }

func (p *process) last() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastLine
}

// startProcess 는 ssh 를 실행한다. ctx 를 취소하면 프로세스 그룹에 SIGTERM 을
// 보내고 killGrace 이후 SIGKILL 로 확대한다. child 는 항상 reap 된다.
func startProcess(ctx context.Context, bin string, args []string, onLine func(string)) (*process, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	p := &process{cmd: cmd, done: make(chan struct{})}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = &lineWriter{fn: func(line string) {
		p.mu.Lock()
		p.lastLine = line
		p.mu.Unlock()
		onLine(line)
	}}
	cmd.SysProcAttr = sysProcAttr()
	cmd.Cancel = func() error { return terminate(cmd) }
	cmd.WaitDelay = killGrace
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ssh: %w", err)
	}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

const killGrace = 3 * time.Second

// lineWriter 는 ssh stderr 스트림을 trim 된 라인들로 바꾼다.
type lineWriter struct {
	mu  sync.Mutex
	buf []byte
	fn  func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexAny(w.buf, "\r\n")
		if i < 0 {
			break
		}
		if line := strings.TrimSpace(string(w.buf[:i])); line != "" {
			w.fn(line)
		}
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > 4096 {
		w.fn(strings.TrimSpace(string(w.buf)))
		w.buf = w.buf[:0]
	}
	return len(p), nil
}
