package router

import "sync/atomic"

// Decision 은 한 host 에 대한 라우팅 결과다.
type Decision struct {
	Mode     Mode
	Upstream string // proxy 일 때 나갈 업스트림 이름
	Matched  string // 매칭된 route 도메인, 기본값이 적용됐으면 ""
}

// Router 는 살아 있는 라우팅 테이블을 들고 있다. 갱신은 불변 Matcher 를
// 통째로 교체하므로 조회는 막히지 않고 이전 표나 새 표 중 하나를 온전히 본다.
type Router struct {
	matcher atomic.Pointer[Matcher]
}

func New(routes []Route) *Router {
	r := &Router{}
	r.SetRoutes(routes)
	return r
}

func (r *Router) SetRoutes(routes []Route) {
	r.matcher.Store(NewMatcher(routes))
}

// Decide 는 매칭되는 route 가 없는 host 를 DIRECT 로 보낸다.
func (r *Router) Decide(h Host) Decision {
	if rt, ok := r.matcher.Load().Match(h); ok {
		return Decision{Mode: rt.Mode, Upstream: rt.Upstream, Matched: rt.Domain}
	}
	return Decision{Mode: ModeDirect}
}
