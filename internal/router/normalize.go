package router

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// HostKind 는 DNS 이름과 IP 리터럴을 구분한다. IP 리터럴은 suffix matching 에
// 절대 참여하지 않는다.
type HostKind int

const (
	KindDomain HostKind = iota
	KindIPv4
	KindIPv6
)

func (k HostKind) String() string {
	switch k {
	case KindIPv4:
		return "ipv4"
	case KindIPv6:
		return "ipv6"
	default:
		return "domain"
	}
}

// Host 는 검증되고 정규화된 hostname 또는 IP 리터럴이다 (IPv6 는 대괄호 없이).
type Host struct {
	Name string
	Kind HostKind
}

var ErrInvalidHost = errors.New("invalid host")

const (
	maxHostLen  = 253
	maxLabelLen = 63
)

// ParseHost 는 포트 없는 host 를 정규화한다: 소문자로, 끝의 점 제거,
// IPv6 대괄호 제거, IP 리터럴은 canonical form 으로.
func ParseHost(raw string) (Host, error) {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		s = s[1 : len(s)-1]
	}
	if s == "" {
		return Host{}, fmt.Errorf("%w: empty", ErrInvalidHost)
	}
	if addr, err := netip.ParseAddr(s); err == nil {
		if addr.Is4() {
			return Host{Name: addr.String(), Kind: KindIPv4}, nil
		}
		if addr.Is4In6() && addr.Zone() == "" {
			return Host{Name: addr.Unmap().String(), Kind: KindIPv4}, nil
		}
		return Host{Name: addr.String(), Kind: KindIPv6}, nil
	}
	if strings.Contains(s, ":") {
		return Host{}, fmt.Errorf("%w: %q", ErrInvalidHost, raw)
	}
	name := strings.TrimSuffix(strings.ToLower(s), ".")
	if err := validateDomain(name); err != nil {
		return Host{}, fmt.Errorf("%w: %q: %v", ErrInvalidHost, raw, err)
	}
	return Host{Name: name, Kind: KindDomain}, nil
}

func validateDomain(name string) error {
	if name == "" {
		return errors.New("empty name")
	}
	if len(name) > maxHostLen {
		return errors.New("name too long")
	}
	for label := range strings.SplitSeq(name, ".") {
		if label == "" {
			return errors.New("empty label")
		}
		if len(label) > maxLabelLen {
			return errors.New("label too long")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("label starts or ends with '-'")
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			case c >= 0x80:
				return errors.New("non-ASCII name, use the punycode (xn--) form")
			default:
				return fmt.Errorf("invalid character %q", c)
			}
		}
	}
	return nil
}

// SplitHostPort 는 authority 를 host 와 port 로 나눈다. port 는 선택 사항이며
// 없으면 "" 이다. 대괄호 없는 IPv6 리터럴도 host 로 허용한다.
func SplitHostPort(authority string) (Host, string, error) {
	a := strings.TrimSpace(authority)
	if a == "" {
		return Host{}, "", fmt.Errorf("%w: empty", ErrInvalidHost)
	}
	var hostPart, port string
	switch {
	case strings.HasPrefix(a, "["):
		end := strings.IndexByte(a, ']')
		if end < 0 {
			return Host{}, "", fmt.Errorf("%w: unterminated '[' in %q", ErrInvalidHost, authority)
		}
		hostPart = a[1:end]
		rest := a[end+1:]
		if rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return Host{}, "", fmt.Errorf("%w: %q", ErrInvalidHost, authority)
			}
			port = rest[1:]
			if port == "" {
				return Host{}, "", fmt.Errorf("%w: empty port in %q", ErrInvalidHost, authority)
			}
		}
	case strings.Count(a, ":") > 1:
		hostPart = a
	default:
		h, p, err := net.SplitHostPort(a)
		if err != nil {
			hostPart = a
		} else {
			hostPart, port = h, p
			if port == "" {
				return Host{}, "", fmt.Errorf("%w: empty port in %q", ErrInvalidHost, authority)
			}
		}
	}
	if port != "" {
		if err := validatePort(port); err != nil {
			return Host{}, "", fmt.Errorf("%w: %q: %v", ErrInvalidHost, authority, err)
		}
	}
	h, err := ParseHost(hostPart)
	if err != nil {
		return Host{}, "", err
	}
	return h, port, nil
}

func validatePort(p string) error {
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
		return fmt.Errorf("invalid port %q", p)
	}
	return nil
}

// NormalizeRouteInput 은 "https://WWW.Example.com:443/watch?v=1" 같은 사용자
// 입력을 route key("www.example.com")로 바꾼다. 도메인 route 는 서브도메인을
// 포함하므로 앞의 "." 는 제거한다. 앞의 "*." 는 남겨 서브도메인만 매칭되는
// 와일드카드 route("*.example.com")가 된다.
func NormalizeRouteInput(input string) (string, error) {
	s := strings.TrimSpace(input)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndexByte(s, '@'); i >= 0 {
		s = s[i+1:]
	}
	wild := strings.HasPrefix(s, WildcardPrefix)
	if wild {
		s = s[len(WildcardPrefix):]
	} else {
		s = strings.TrimPrefix(s, ".")
	}
	if s == "" {
		return "", fmt.Errorf("%w: empty domain", ErrInvalidHost)
	}
	h, _, err := SplitHostPort(s)
	if err != nil {
		return "", err
	}
	if wild {
		if h.Kind != KindDomain {
			return "", fmt.Errorf("%w: %q: a wildcard needs a domain, not an IP address", ErrInvalidHost, input)
		}
		return WildcardPrefix + h.Name, nil
	}
	return h.Name, nil
}
