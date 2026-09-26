package router

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	ErrUnknownMode         = errors.New("unknown route mode")
	ErrInvalidUpstreamName = errors.New("invalid upstream name")
)

// WildcardPrefix 로 시작하는 route("*.example.com")는 서브도메인에만 매칭되고
// example.com 자체에는 매칭되지 않는다.
const WildcardPrefix = "*."

// ViaDirect 는 "업스트림 없이 직접 연결"을 뜻하는 예약어라 업스트림 이름으로 쓸 수 없다.
const ViaDirect = "direct"

// Mode 는 host 가 route 에 매칭된 연결에 어떤 일이 일어나는지를 나타낸다.
type Mode string

const (
	ModeProxy  Mode = "proxy"
	ModeDirect Mode = "direct"
)

func ParseMode(s string) (Mode, error) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case "", ModeProxy:
		return ModeProxy, nil
	case ModeDirect:
		return ModeDirect, nil
	default:
		return "", fmt.Errorf("%w %q (want proxy or direct)", ErrUnknownMode, s)
	}
}

// Label 은 로그와 TUI 에 쓰이는 대문자 태그다.
func (m Mode) Label() string {
	if m == ModeProxy {
		return "PROXY"
	}
	return "DIRECT"
}

// Route 는 Domain 과 그 모든 서브도메인에 매칭된다. Domain 이 "*." 로
// 시작하면 서브도메인에만 매칭된다. IP 리터럴이면 그 정확한 주소에만 매칭된다. proxy route 는 Upstream 이름의 SOCKS 로 나간다.
type Route struct {
	Domain   string `json:"domain"`
	Mode     Mode   `json:"mode"`
	Upstream string `json:"upstream,omitempty"`
}

// Via 는 route 의 목적지를 한 단어로 돌려준다: 업스트림 이름, "direct",
// 또는 아직 업스트림이 없을 때의 "".
func (r Route) Via() string {
	if r.Mode == ModeDirect {
		return ViaDirect
	}
	return r.Upstream
}

var upstreamNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,23}$`)

// ValidateUpstreamName 은 route 의 via 값과 충돌하지 않는 업스트림 이름인지 검사한다.
func ValidateUpstreamName(name string) error {
	if name == ViaDirect {
		return fmt.Errorf("%w: %q is reserved", ErrInvalidUpstreamName, name)
	}
	if !upstreamNameRe.MatchString(name) {
		return fmt.Errorf("%w: %q (use 1-24 lowercase letters, digits, '-' or '_')", ErrInvalidUpstreamName, name)
	}
	return nil
}

// ParseVia 는 "direct" 또는 업스트림 이름을 mode 와 upstream 으로 푼다.
// 빈 문자열은 "기본 업스트림"이다.
func ParseVia(via string) (Mode, string, error) {
	v := strings.ToLower(strings.TrimSpace(via))
	switch v {
	case "":
		return ModeProxy, "", nil
	case ViaDirect:
		return ModeDirect, "", nil
	}
	if err := ValidateUpstreamName(v); err != nil {
		return "", "", err
	}
	return ModeProxy, v, nil
}

// NewRoute 는 사용자가 입력한 domain 과 via 를 정규화한다.
func NewRoute(input, via string) (Route, error) {
	d, err := NormalizeRouteInput(input)
	if err != nil {
		return Route{}, err
	}
	m, up, err := ParseVia(via)
	if err != nil {
		return Route{}, err
	}
	return Route{Domain: d, Mode: m, Upstream: up}, nil
}
