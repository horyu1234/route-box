package router

import (
	"sync"
	"sync/atomic"
)

// Decision 은 한 host 에 대한 라우팅 결과다.
type Decision struct {
	Mode     Mode
	Upstream string // proxy 일 때 나갈 업스트림 이름
	Matched  string // 매칭된 route 도메인, fallback 이 적용됐으면 ""
}

type table struct {
	matcher  *Matcher
	fallback Decision
}

// Router 는 살아 있는 라우팅 테이블을 들고 있다. 갱신은 불변 테이블을
// 통째로 교체하므로 조회는 막히지 않고 이전 표나 새 표 중 하나를 온전히 본다.
type Router struct {
	mu    sync.Mutex // 쓰기끼리만 직렬화한다
	table atomic.Pointer[table]
}

func New(routes []Route) *Router {
	r := &Router{}
	r.table.Store(&table{matcher: NewMatcher(routes), fallback: Decision{Mode: ModeDirect}})
	return r
}

func (r *Router) SetRoutes(routes []Route) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := *r.table.Load()
	t.matcher = NewMatcher(routes)
	r.table.Store(&t)
}

// SetFallback 은 매칭되는 route 가 없는 host 가 나갈 업스트림을 정한다.
// "" 이면 DIRECT 다.
func (r *Router) SetFallback(upstream string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := *r.table.Load()
	t.fallback = Decision{Mode: ModeDirect}
	if upstream != "" {
		t.fallback = Decision{Mode: ModeProxy, Upstream: upstream}
	}
	r.table.Store(&t)
}

// Decide 는 매칭되는 route 가 없는 host 를 fallback(기본은 DIRECT)으로 보낸다.
func (r *Router) Decide(h Host) Decision {
	t := r.table.Load()
	if rt, ok := t.matcher.Match(h); ok {
		return Decision{Mode: rt.Mode, Upstream: rt.Upstream, Matched: rt.Domain}
	}
	return t.fallback
}
