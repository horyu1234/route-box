package socks

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/horyu1234/route-box/internal/socks/sockstest"
)

func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func TestDialSendsHostnameUnresolved(t *testing.T) {
	srv, err := sockstest.Start(sockstest.DialTo(echoServer(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	d := &Dialer{ProxyAddr: srv.Addr()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := d.DialContext(ctx, "www.example.com", "443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, ok := conn.(interface{ CloseWrite() error }); !ok {
		t.Errorf("conn %T does not support CloseWrite", conn)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo = %q, %v", buf, err)
	}
	reqs := srv.Requests()
	if len(reqs) != 1 || reqs[0].Atyp != 0x03 || reqs[0].Host != "www.example.com" || reqs[0].Port != 443 {
		t.Fatalf("requests = %+v", reqs)
	}
}

func TestDialIPLiterals(t *testing.T) {
	srv, err := sockstest.Start(sockstest.DialTo(echoServer(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	d := &Dialer{ProxyAddr: srv.Addr()}
	for _, tt := range []struct {
		host string
		atyp byte
		want string
	}{
		{"10.1.2.3", 0x01, "10.1.2.3"},
		{"2001:db8::1", 0x04, "2001:db8::1"},
	} {
		conn, err := d.DialContext(context.Background(), tt.host, "80")
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
		reqs := srv.Requests()
		last := reqs[len(reqs)-1]
		if last.Atyp != tt.atyp || last.Host != tt.want {
			t.Errorf("%s: got %+v", tt.host, last)
		}
	}
}

func TestDialReplyError(t *testing.T) {
	srv, err := sockstest.Start(func(sockstest.Request) (net.Conn, error) { return nil, errors.New("no") })
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	srv.Reply = 0x04
	_, err = (&Dialer{ProxyAddr: srv.Addr()}).DialContext(context.Background(), "example.com", "443")
	var re *ReplyError
	if !errors.As(err, &re) || re.Code != 0x04 {
		t.Fatalf("err = %v, want ReplyError 0x04", err)
	}
}

func TestDialServerDown(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	if _, err := (&Dialer{ProxyAddr: addr}).DialContext(context.Background(), "example.com", "443"); err == nil {
		t.Fatal("expected error")
	}
	if err := Probe(context.Background(), addr); err == nil {
		t.Fatal("probe of closed port should fail")
	}
}

func TestHandshakeHonoursContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			time.Sleep(2 * time.Second)
			_ = c.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := Probe(ctx, ln.Addr().String()); err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("probe ignored context: took %v", time.Since(start))
	}
}

func TestProbeOK(t *testing.T) {
	srv, err := sockstest.Start(sockstest.DialTo(echoServer(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if err := Probe(context.Background(), srv.Addr()); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidInput(t *testing.T) {
	d := &Dialer{ProxyAddr: "127.0.0.1:1"}
	for _, port := range []string{"0", "x", "70000"} {
		if _, err := d.DialContext(context.Background(), "a.com", port); err == nil {
			t.Errorf("port %q accepted", port)
		}
	}
}
