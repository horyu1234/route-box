package ssh_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/horyu1234/route-box/internal/ssh"
	"github.com/horyu1234/route-box/internal/ssh/sshtest"
)

func TestScanShowsFingerprintAndTrustAppendsTheSameLine(t *testing.T) {
	fake := sshtest.New(t, sshtest.Unknown)
	spec := ssh.Spec{Host: "fake.example.net", User: "me", Port: 2222, SocksAddr: "127.0.0.1:1080"}
	k, err := ssh.ScanHostKey(context.Background(), fake.Bin, spec)
	if err != nil {
		t.Fatal(err)
	}
	if k.Fingerprint() != sshtest.Fingerprint || k.KnownHosts != fake.KnownHosts {
		t.Fatalf("scan = %+v", k)
	}
	if _, err := os.Stat(fake.KnownHosts); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("scanning must not write the real known_hosts")
	}
	log, _ := os.ReadFile(fake.ArgsLog)
	for _, l := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		if !strings.Contains(l, "-p 2222") || !strings.Contains(l, "-l me") || !strings.Contains(l, "-- fake.example.net") {
			t.Errorf("ssh call lost port/user/host separator: %s", l)
		}
		if !strings.HasPrefix(l, "-G") && (!strings.Contains(l, "PubkeyAuthentication=no") || !strings.Contains(l, "BatchMode=yes")) {
			t.Errorf("host key probe must not try to authenticate: %s", l)
		}
	}

	// 기존 파일이 개행 없이 끝나도 줄이 붙지 않는다.
	if err := os.MkdirAll(filepath.Dir(fake.KnownHosts), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fake.KnownHosts, []byte("other.example.org ssh-ed25519 AAAA"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ssh.TrustHostKey(k); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(fake.KnownHosts)
	want := "other.example.org ssh-ed25519 AAAA\nfake.example.net ssh-ed25519 " + sshtest.Key + "\n"
	if string(b) != want {
		t.Fatalf("known_hosts =\n%q\nwant\n%q", b, want)
	}
}

func TestScanRefusesChangedAndSkipsKnownKeys(t *testing.T) {
	spec := ssh.Spec{Host: "fake.example.net", SocksAddr: "127.0.0.1:1080"}
	for mode, want := range map[sshtest.Mode]error{sshtest.Changed: ssh.ErrHostKeyChanged, sshtest.Known: ssh.ErrHostKeyKnown} {
		fake := sshtest.New(t, mode)
		if _, err := ssh.ScanHostKey(context.Background(), fake.Bin, spec); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", mode, err, want)
		}
		if log, _ := os.ReadFile(fake.ArgsLog); strings.Contains(string(log), "accept-new") {
			t.Errorf("%s: must not fetch a key it will not offer to trust", mode)
		}
	}
	fake := sshtest.New(t, sshtest.Unreachable)
	if _, err := ssh.ScanHostKey(context.Background(), fake.Bin, spec); err == nil || !strings.Contains(err.Error(), "Connection refused") {
		t.Errorf("unreachable: %v", err)
	}
}

func TestIsHostKeyFailure(t *testing.T) {
	if !ssh.IsHostKeyFailure("Host key verification failed. (exit status 255)") || ssh.IsHostKeyFailure("Permission denied (publickey)") {
		t.Fatal("classification wrong")
	}
}
