package ssh

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	// ErrHostKeyChanged 는 저장된 host key 와 서버가 내민 키가 다르다는 뜻이다.
	// 중간자 공격일 수 있으므로 RouteBox 는 이 키를 신뢰하자고 제안하지 않는다.
	ErrHostKeyChanged = errors.New("the host key has CHANGED since it was saved")
	// ErrHostKeyKnown 은 host key 가 이미 신뢰돼 있어 확인할 것이 없다는 뜻이다.
	ErrHostKeyKnown = errors.New("the host key is already trusted")
)

// HostKey 는 서버가 한 번의 연결에서 내민 host key 다. Lines 는 ssh 가 직접
// known_hosts 형식으로 쓴 줄이라, 사용자가 확인한 키와 저장되는 키가 같다.
type HostKey struct {
	Host         string   `json:"host"`
	Fingerprints []string `json:"fingerprints"` // "ssh-ed25519 SHA256:…"
	KnownHosts   string   `json:"known_hosts"`  // 저장될 파일(ssh -G 의 첫 UserKnownHostsFile)
	Lines        []string `json:"-"`
}

// Fingerprint 는 비교용 대표 지문이다(첫 줄).
func (k HostKey) Fingerprint() string {
	if len(k.Fingerprints) == 0 {
		return ""
	}
	return k.Fingerprints[0]
}

// IsHostKeyFailure 는 ssh 오류가 host key 확인 실패인지 본다.
func IsHostKeyFailure(msg string) bool {
	return strings.Contains(msg, "Host key verification failed")
}

// connectOpts 는 host key 확인용 연결의 공통 인자다: 인증은 모두 끄므로
// 키 교환까지만 하고 곧바로 실패한다.
func connectOpts(s Spec) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "PubkeyAuthentication=no",
		"-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "GSSAPIAuthentication=no",
		"-o", "HostbasedAuthentication=no",
		"-o", "UpdateHostKeys=no",
	}
	return append(args, target(s)...)
}

func target(s Spec) []string {
	var args []string
	if s.Port > 0 {
		args = append(args, "-p", strconv.Itoa(s.Port))
	}
	if s.User != "" {
		args = append(args, "-l", s.User)
	}
	return args
}

// ScanHostKey 는 ~/.ssh/config 를 그대로 따르는 ssh 로 서버의 host key 를 받아
// 온다. ssh-keyscan 은 Port, HostKeyAlias, ProxyJump, HashKnownHosts 를 무시하므로
// 쓰지 않는다. 키가 바뀌었으면 ErrHostKeyChanged, 이미 신뢰돼 있으면
// ErrHostKeyKnown 을 돌려준다.
func ScanHostKey(ctx context.Context, bin string, s Spec) (HostKey, error) {
	if err := s.Validate(); err != nil {
		return HostKey{}, err
	}
	if bin == "" {
		var err error
		if bin, err = FindBinary(); err != nil {
			return HostKey{}, err
		}
	}
	known, err := knownHostsFile(ctx, bin, s)
	if err != nil {
		return HostKey{}, err
	}

	// 1) 실제 known_hosts 로 확인: 모르는 키인지, 바뀐 키인지 가린다.
	check := append(connectOpts(s), "-o", "StrictHostKeyChecking=yes", "--", s.Host, "true")
	out, _ := runSSH(ctx, bin, check)
	switch classify(out) {
	case keyChanged:
		return HostKey{}, ErrHostKeyChanged
	case keyKnown:
		return HostKey{}, ErrHostKeyKnown
	}

	// 2) 빈 임시 known_hosts 로 한 번 더 연결해 ssh 가 키를 거기에 쓰게 한다.
	dir, err := os.MkdirTemp("", "routebox-hostkey")
	if err != nil {
		return HostKey{}, err
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, "known_hosts")
	scan := append(connectOpts(s),
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile="+tmp,
		"-o", "GlobalKnownHostsFile=/dev/null",
		"--", s.Host, "true")
	out, _ = runSSH(ctx, bin, scan)
	lines, fps, err := readKnownHosts(tmp)
	if err != nil || len(lines) == 0 {
		msg := lastLine(out)
		if msg == "" {
			msg = "no host key received"
		}
		return HostKey{}, fmt.Errorf("could not fetch the host key of %s: %s", s.Host, msg)
	}
	return HostKey{Host: s.Host, Fingerprints: fps, KnownHosts: known, Lines: lines}, nil
}

// TrustHostKey 는 ScanHostKey 가 받은 줄을 그대로 known_hosts 에 덧붙인다.
func TrustHostKey(k HostKey) error {
	if k.KnownHosts == "" || len(k.Lines) == 0 {
		return errors.New("nothing to trust")
	}
	if err := os.MkdirAll(filepath.Dir(k.KnownHosts), 0o700); err != nil {
		return err
	}
	prefix := ""
	if b, err := os.ReadFile(k.KnownHosts); err == nil && len(b) > 0 && b[len(b)-1] != '\n' {
		prefix = "\n"
	}
	f, err := os.OpenFile(k.KnownHosts, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(prefix + strings.Join(k.Lines, "\n") + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func knownHostsFile(ctx context.Context, bin string, s Spec) (string, error) {
	out, err := runSSH(ctx, bin, append(append([]string{"-G"}, target(s)...), "--", s.Host))
	if err != nil {
		return "", fmt.Errorf("ssh -G %s: %w: %s", s.Host, err, lastLine(out))
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), " "); ok && strings.EqualFold(k, "userknownhostsfile") {
			if f := strings.Fields(v); len(f) > 0 && f[0] != "/dev/null" {
				return ExpandHome(f[0])
			}
		}
	}
	return "", errors.New("ssh -G did not report a UserKnownHostsFile")
}

func runSSH(ctx context.Context, bin string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = nil
	return cmd.CombinedOutput()
}

type keyState int

const (
	keyUnknown keyState = iota
	keyChanged
	keyKnown
)

func classify(out []byte) keyState {
	s := string(out)
	switch {
	case strings.Contains(s, "IDENTIFICATION HAS CHANGED"),
		strings.Contains(s, "has changed and you have requested strict checking"),
		strings.Contains(s, "POSSIBLE DNS SPOOFING"):
		return keyChanged
	case strings.Contains(s, "host key is known for"),
		strings.Contains(s, "Host key verification failed"):
		return keyUnknown
	case strings.Contains(s, "Permission denied"):
		return keyKnown
	}
	// 연결 자체가 안 된 경우 등: 스캔에서 원인을 보고하게 둔다.
	return keyUnknown
}

func readKnownHosts(path string) (lines, fingerprints []string, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		blob, err := base64.StdEncoding.DecodeString(f[2])
		if err != nil {
			continue
		}
		sum := sha256.Sum256(blob)
		lines = append(lines, l)
		fingerprints = append(fingerprints, f[1]+" SHA256:"+base64.RawStdEncoding.EncodeToString(sum[:]))
	}
	return lines, fingerprints, nil
}

func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
