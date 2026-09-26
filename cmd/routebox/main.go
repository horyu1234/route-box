// Command routebox 는 선택한 도메인만 SSH SOCKS5 터널로 보내고 나머지는
// 다이렉트로 보내는 HTTP CONNECT 프록시 기반의 선택적 도메인 프록시 라우터다.
package main

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"
)

// version 은 make build 가 ldflags 로 git describe 값을 넣는다. 비어 있으면
// ("dev") go install 이 바이너리에 기록한 모듈 버전(예: v0.1.1)을 쓴다.
var version = "dev"

func init() {
	bi, ok := debug.ReadBuildInfo()
	version = resolveVersion(version, bi, ok)
}

func resolveVersion(ldflags string, bi *debug.BuildInfo, ok bool) string {
	if ldflags != "dev" || !ok || bi == nil {
		return ldflags
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return ldflags
}

// errSilent 는 추가 출력 없이 0이 아닌 코드로 종료함을 나타낸다.
var errSilent = errors.New("")

func main() {
	if err := newRootCmd().Execute(); err != nil {
		if !errors.Is(err, errSilent) {
			fmt.Fprintln(os.Stderr, "routebox:", err)
		}
		os.Exit(1)
	}
}
