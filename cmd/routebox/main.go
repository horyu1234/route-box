// Command routebox 는 선택한 도메인만 SSH SOCKS5 터널로 보내고 나머지는
// 다이렉트로 보내는 HTTP CONNECT 프록시 기반의 선택적 도메인 프록시 라우터다.
package main

import (
	"errors"
	"fmt"
	"os"
)

var version = "dev"

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
