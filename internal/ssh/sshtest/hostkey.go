// Package sshtest 는 host key 확인 흐름을 테스트하는 가짜 ssh 를 만든다.
// 실제 ~/.ssh 와 네트워크는 절대 건드리지 않는다.
package sshtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Key 는 가짜 서버가 내미는 ed25519 공개키(base64)다.
const Key = "AAAAC3NzaC1lZDI1NTE5AAAAIJPEIy6l5ef8sy4MqSKmrsAOkFq7+VWd5Faai+O6Ak5u"

// Fingerprint 는 Key 의 SHA256 지문이다(ssh-keygen -lf 로 확인한 값).
const Fingerprint = "ssh-ed25519 SHA256:pdIhR7UYsGcGTnqQPXULw9bGXPBUCiHJHl8rCHaS6QM"

// Mode 는 가짜 서버의 상태다.
type Mode string

const (
	Unknown     Mode = "unknown"     // known_hosts 에 없음
	Changed     Mode = "changed"     // 저장된 키와 다름
	Known       Mode = "known"       // 이미 신뢰됨
	Unreachable Mode = "unreachable" // 연결 불가
)

// Fake 는 가짜 ssh 스크립트다. KnownHosts 는 ssh -G 가 보고하는 (임시) 파일이고,
// ArgsLog 에는 호출마다 인자가 한 줄씩 남는다.
type Fake struct {
	Bin        string
	KnownHosts string
	ArgsLog    string
}

func New(t *testing.T, mode Mode) Fake {
	t.Helper()
	dir := t.TempDir()
	f := Fake{
		Bin:        filepath.Join(dir, "ssh"),
		KnownHosts: filepath.Join(dir, "home", ".ssh", "known_hosts"),
		ArgsLog:    filepath.Join(dir, "args.log"),
	}
	script := strings.NewReplacer("MODE", string(mode), "KNOWN", f.KnownHosts, "ARGSLOG", f.ArgsLog, "KEY", Key).Replace(`#!/bin/sh
echo "$*" >> 'ARGSLOG'
if [ "$1" = "-G" ]; then
	echo "hostname fake.example.net"
	echo "userknownhostsfile KNOWN KNOWN2"
	exit 0
fi
check=; scan=; kh=
for a in "$@"; do
	case "$a" in
	StrictHostKeyChecking=yes) check=1 ;;
	StrictHostKeyChecking=accept-new) scan=1 ;;
	UserKnownHostsFile=*) kh="${a#UserKnownHostsFile=}" ;;
	esac
done
if [ "MODE" = unreachable ]; then
	echo "ssh: connect to host fake.example.net port 22: Connection refused" >&2
	exit 255
fi
if [ -n "$check" ]; then
	case MODE in
	unknown)
		echo "No ED25519 host key is known for fake.example.net and you have requested strict checking." >&2
		echo "Host key verification failed." >&2 ;;
	changed)
		echo "@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @" >&2
		echo "Host key verification failed." >&2 ;;
	known)
		echo "me@fake.example.net: Permission denied (publickey)." >&2 ;;
	esac
	exit 255
fi
if [ -n "$scan" ]; then
	echo "Warning: Permanently added 'fake.example.net' (ED25519) to the list of known hosts." >&2
	echo "fake.example.net ssh-ed25519 KEY" >> "$kh"
	echo "me@fake.example.net: Permission denied (publickey)." >&2
	exit 255
fi
exit 1
`)
	if err := os.WriteFile(f.Bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}
