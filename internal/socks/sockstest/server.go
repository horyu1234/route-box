// Package sockstest 는 테스트용 in-process SOCKS5 서버를 제공한다. 클라이언트가
// 보낸 주소를 아무것도 resolve 하지 않고 그대로 기록한다.
package sockstest

import (
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
)

// Request 는 wire 상에서 받은 CONNECT 요청 하나다.
type Request struct {
	Atyp byte
	Host string
	Port uint16
}

type Server struct {
	ln net.Listener
	// Dial 은 요청에 대한 upstream 을 연다. 에러를 반환하면 서버가 Reply
	// 값으로(Reply 가 0이면 0x05 로) 응답한다.
	Dial  func(r Request) (net.Conn, error)
	Reply byte

	mu       sync.Mutex
	requests []Request
	wg       sync.WaitGroup
}

// Start 는 127.0.0.1 에서 listen 하며 Close 될 때까지 서비스한다.
func Start(dial func(Request) (net.Conn, error)) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln, Dial: dial}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

func (s *Server) Addr() string { return s.ln.Addr().String() }

func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func (s *Server) Close() error {
	err := s.ln.Close()
	s.wg.Wait()
	return err
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(c)
		}()
	}
}

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	var hdr [2]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil || hdr[0] != 5 {
		return
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}
	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil {
		return
	}
	r := Request{Atyp: req[3]}
	switch req[3] {
	case 1, 4:
		n := 4
		if req[3] == 4 {
			n = 16
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		addr, _ := netip.AddrFromSlice(b)
		r.Host = addr.String()
	case 3:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return
		}
		b := make([]byte, l[0])
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		r.Host = string(b)
	default:
		return
	}
	var p [2]byte
	if _, err := io.ReadFull(c, p[:]); err != nil {
		return
	}
	r.Port = binary.BigEndian.Uint16(p[:])
	s.mu.Lock()
	s.requests = append(s.requests, r)
	s.mu.Unlock()

	up, err := s.Dial(r)
	if err != nil {
		code := s.Reply
		if code == 0 {
			code = 5
		}
		_, _ = c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	if _, err := c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if tc, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}
	go pipe(up, c)
	go pipe(c, up)
	<-done
	<-done
}

// DialTo 는 모든 요청을 addr 로 연결하는 Dial 함수를 반환한다.
func DialTo(addr string) func(Request) (net.Conn, error) {
	return func(Request) (net.Conn, error) { return net.Dial("tcp", addr) }
}

// HostPort 는 assertion 을 위해 요청 target 을 포맷한다.
func (r Request) HostPort() string {
	return net.JoinHostPort(r.Host, strconv.Itoa(int(r.Port)))
}
