package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"time"
)

type closeWriter interface{ CloseWrite() error }

// relay 는 양방향으로 복사하며 양쪽이 끝나거나, 한쪽이 실패하거나, 터널이
// idle 이상 유휴 상태거나, ctx 가 취소될 때까지 계속한다. 한쪽이 EOF 에
// 도달하면 반대쪽의 write half 만 닫으므로 half-closed TCP 스트림도 계속
// 동작한다. relay 가 반환되기 전에 두 conn 모두 닫힌다.
func relay(ctx context.Context, client net.Conn, clientR io.Reader, upstream net.Conn, idle time.Duration) (up, down int64) {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())

	type result struct {
		n   int64
		err error
	}
	upc := make(chan result, 1)
	downc := make(chan result, 1)
	pipe := func(dst net.Conn, src io.Reader, out chan<- result) {
		n, err := io.Copy(dst, &activityReader{r: src, last: &last})
		if cw, ok := dst.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		out <- result{n, err}
	}
	go pipe(upstream, clientR, upc)
	go pipe(client, upstream, downc)

	var once atomic.Bool
	abort := func() {
		if once.CompareAndSwap(false, true) {
			_ = client.Close()
			_ = upstream.Close()
		}
	}
	defer abort()

	tick := idle / 4
	if tick < 50*time.Millisecond {
		tick = 50 * time.Millisecond
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	upDone, downDone := false, false
	for !upDone || !downDone {
		select {
		case r := <-upc:
			up, upDone = r.n, true
			if !cleanEOF(r.err) {
				abort()
			}
		case r := <-downc:
			down, downDone = r.n, true
			if !cleanEOF(r.err) {
				abort()
			}
		case <-ticker.C:
			if idle > 0 && time.Since(time.Unix(0, last.Load())) > idle {
				abort()
			}
		case <-ctx.Done():
			abort()
		}
	}
	return up, down
}

func cleanEOF(err error) bool {
	return err == nil || errors.Is(err, io.EOF)
}

type activityReader struct {
	r    io.Reader
	last *atomic.Int64
}

func (a *activityReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 {
		a.last.Store(time.Now().UnixNano())
	}
	return n, err
}
