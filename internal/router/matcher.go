package router

import "strings"

// Matcher 는 route 목록으로 만든 불변 lookup 테이블이다.
// 가장 구체적인(가장 긴) 매칭 route 가 우선하므로, "intranet.example.com" 의
// "direct" route 가 "example.com" 의 "proxy" route 안에서 예외를 만들 수 있다.
// 같은 도메인이면 서브도메인만 덮는 "*.example.com" 이 "example.com" 보다
// 구체적이다: 둘을 함께 두면 example.com 자체와 그 서브도메인을 나눠 보낼 수 있다.
type Matcher struct {
	domains map[string]Route // 도메인 자체와 서브도메인
	subs    map[string]Route // 서브도메인만("*." 와일드카드)
	ips     map[string]Route
}

func NewMatcher(routes []Route) *Matcher {
	m := &Matcher{domains: make(map[string]Route), subs: make(map[string]Route), ips: make(map[string]Route)}
	for _, r := range routes {
		name, wild := strings.CutPrefix(r.Domain, WildcardPrefix)
		h, err := ParseHost(name)
		if err != nil || (wild && h.Kind != KindDomain) {
			continue
		}
		switch {
		case wild:
			r.Domain = WildcardPrefix + h.Name
			m.subs[h.Name] = r
		case h.Kind == KindDomain:
			r.Domain = h.Name
			m.domains[h.Name] = r
		default:
			r.Domain = h.Name
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
	for self := true; ; self = false {
		// h 자신이 아니라 그 상위 도메인일 때만 와일드카드가 매칭된다.
		if r, ok := m.subs[name]; ok && !self {
			return r, true
		}
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
