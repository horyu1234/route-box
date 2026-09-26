package router

import "strings"

// Matcher 는 route 목록으로 만든 불변 lookup 테이블이다.
// 가장 구체적인(가장 긴) 매칭 route 가 우선하므로, "intranet.example.com" 의
// "direct" route 가 "example.com" 의 "proxy" route 안에서 예외를 만들 수 있다.
type Matcher struct {
	domains map[string]Route
	ips     map[string]Route
}

func NewMatcher(routes []Route) *Matcher {
	m := &Matcher{domains: make(map[string]Route), ips: make(map[string]Route)}
	for _, r := range routes {
		h, err := ParseHost(r.Domain)
		if err != nil {
			continue
		}
		r.Domain = h.Name
		if h.Kind == KindDomain {
			m.domains[h.Name] = r
		} else {
			m.ips[h.Name] = r
		}
	}
	return m
}

// Match 는 h 를 커버하는 route 가 있으면 그것을 보고한다.
func (m *Matcher) Match(h Host) (Route, bool) {
	if h.Kind != KindDomain {
		r, ok := m.ips[h.Name]
		return r, ok
	}
	name := h.Name
	for {
		if r, ok := m.domains[name]; ok {
			return r, true
		}
		i := strings.IndexByte(name, '.')
		if i < 0 {
			return Route{}, false
		}
		name = name[i+1:]
	}
}
