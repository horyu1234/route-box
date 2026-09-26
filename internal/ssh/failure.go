package ssh

import (
	"regexp"
	"strings"
)

// Failure 는 ssh 가 실패한 이유를 재시도 전략과 안내 문구에 맞게 나눈 것이다.
type Failure int

const (
	FailOther Failure = iota
	// FailHostKey 는 host key 를 아직 신뢰하지 않았거나 바뀐 경우다.
	FailHostKey
	// FailAuth 는 서버가 로그인을 거절한 경우다. 사용자가 키나 agent 를 고치기
	// 전에는 다시 시도해도 같은 결과이고, 실패가 쌓이면 서버가 이 IP 를 막는다.
	FailAuth
	// FailRefused 는 서버가 인증 전에 연결을 끊은 경우다. 보통 sshd 의
	// PerSourcePenalties·MaxStartups 나 fail2ban 같은 차단이다.
	FailRefused
)

var closedByRemote = regexp.MustCompile(`Connection (closed|reset) by \S+ port \d+`)

// ClassifyFailure 는 ssh 오류 메시지(마지막 stderr 줄)를 분류한다.
func ClassifyFailure(msg string) Failure {
	switch {
	case IsHostKeyFailure(msg):
		return FailHostKey
	case strings.Contains(msg, "Permission denied"),
		strings.Contains(msg, "Too many authentication failures"):
		return FailAuth
	case strings.Contains(msg, "kex_exchange_identification"),
		closedByRemote.MatchString(msg):
		return FailRefused
	}
	return FailOther
}

// persistent 는 곧바로 다시 시도해도 풀리지 않는 실패다. 이런 실패는
// 처음부터 가장 긴 간격으로 재시도해 서버의 반복 실패 차단을 부르지 않는다.
func (f Failure) persistent() bool { return f != FailOther }
