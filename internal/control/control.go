// Package control 은 unix 소켓을 통해 실행 중인 RouteBox 를 CLI 에 노출한다.
// 이 소켓은 single-instance lock 역할도 겸한다.
package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/horyu1234/route-box/internal/config"
	"github.com/horyu1234/route-box/internal/core"
	"github.com/horyu1234/route-box/internal/events"
	"github.com/horyu1234/route-box/internal/router"
	"github.com/horyu1234/route-box/internal/ssh"
)

var (
	ErrAlreadyRunning = errors.New("another RouteBox instance is already running")
	ErrNotRunning     = errors.New("RouteBox is not running")
)

// maxSocketPath 는 macOS 의 104바이트 sun_path 제한 아래로 여유를 둔다.
const maxSocketPath = 100

// SocketPath 는 설정 디렉터리 안의 제어 소켓 위치를 반환하고, 그 경로가
// unix 소켓으로 쓰기에 너무 길면 temp 디렉터리로 폴백한다.
func SocketPath(configPath string) string {
	p := filepath.Join(filepath.Dir(configPath), "routebox.sock")
	if len(p) <= maxSocketPath {
		return p
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("routebox-%d.sock", os.Getuid()))
}

// Listen 은 소켓을 점유한다. 살아 있는 소켓은 다른 인스턴스가 소유 중이라는
// 뜻이고, 죽은 소켓은 크래시로 남은 것이라 교체한다.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		_ = c.Close()
		return nil, fmt.Errorf("%w (control socket %s)", ErrAlreadyRunning, path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on control socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod control socket: %w", err)
	}
	return ln, nil
}

type errorBody struct {
	Error string `json:"error"`
	Kind  string `json:"kind,omitempty"`
}

// sentinels 는 소켓 너머로 errors.Is 가 그대로 동작하도록 오류 종류를 이름으로 옮긴다.
var sentinels = []struct {
	kind string
	err  error
	code int
}{
	{"route_exists", core.ErrRouteExists, http.StatusConflict},
	{"route_not_found", core.ErrRouteNotFound, http.StatusNotFound},
	{"upstream_in_use", core.ErrUpstreamInUse, http.StatusConflict},
	{"upstream_missing", core.ErrUpstreamMissing, http.StatusNotFound},
	{"duplicate_upstream", config.ErrDuplicateUpstream, http.StatusConflict},
	{"socks_in_use", config.ErrSocksInUse, http.StatusConflict},
	{"unknown_upstream", config.ErrUnknownUpstream, http.StatusBadRequest},
	{"invalid_upstream_name", router.ErrInvalidUpstreamName, http.StatusBadRequest},
	{"invalid_host", router.ErrInvalidHost, http.StatusBadRequest},
	{"unknown_preset", router.ErrUnknownPreset, http.StatusBadRequest},
	{"unknown_mode", router.ErrUnknownMode, http.StatusBadRequest},
	{"no_upstream", core.ErrNoUpstream, http.StatusBadRequest},
	{"not_running", core.ErrNotRunning, http.StatusServiceUnavailable},
	{"not_managed", core.ErrNotManaged, http.StatusBadRequest},
	{"no_pending_key", core.ErrNoPendingKey, http.StatusConflict},
	{"host_key_changed", ssh.ErrHostKeyChanged, http.StatusConflict},
	{"host_key_known", ssh.ErrHostKeyKnown, http.StatusConflict},
}

type remoteError struct {
	msg      string
	sentinel error
}

func (e *remoteError) Error() string { return e.msg }
func (e *remoteError) Unwrap() error { return e.sentinel }

type updateRouteRequest struct {
	Old    string `json:"old"`
	Domain string `json:"domain"`
	Via    string `json:"via"`
}

type trustRequest struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
}

type languageRequest struct {
	Language string `json:"language"`
}

type connectionLogRequest struct {
	On bool `json:"on"`
}

type fallbackRequest struct {
	Via string `json:"via"`
}

type routeRequest struct {
	Domain string `json:"domain"`
	Via    string `json:"via"`
}

// SSHInfo 는 /v1/ssh 응답의 업스트림 하나다.
type SSHInfo struct {
	Upstream string     `json:"upstream"`
	Status   ssh.Status `json:"status"`
	Log      []string   `json:"log"`
}

// Connection 은 연결 로그 항목의 JSON 형태다: host 와 port 만 담는다.
type Connection struct {
	Time     time.Time   `json:"time"`
	Method   string      `json:"method"`
	Host     string      `json:"host"`
	Port     string      `json:"port"`
	Route    router.Mode `json:"route"`
	Upstream string      `json:"upstream,omitempty"`
	State    string      `json:"state"`
	BytesIn  int64       `json:"bytes_in"`
	BytesOut int64       `json:"bytes_out"`
	Duration string      `json:"duration"`
	Error    string      `json:"error,omitempty"`
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(v); err != nil {
		return fmt.Errorf("bad request body: %w", err)
	}
	return nil
}

// Serve 는 ctx 가 취소될 때까지 제어 요청에 응답한 뒤 소켓을 제거한다.
func Serve(ctx context.Context, ln net.Listener, app *core.App) error {
	mux := http.NewServeMux()
	handle := func(pattern string, fn func(r *http.Request) (int, any, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			code, v, err := fn(r)
			if err != nil {
				writeError(w, err)
				return
			}
			if v == nil {
				w.WriteHeader(code)
				return
			}
			writeJSON(w, code, v)
		})
	}
	handle("GET /v1/status", func(*http.Request) (int, any, error) {
		return http.StatusOK, app.Status(), nil
	})
	handle("GET /v1/routes", func(*http.Request) (int, any, error) {
		return http.StatusOK, app.Routes(), nil
	})
	handle("POST /v1/routes", func(r *http.Request) (int, any, error) {
		var req routeRequest
		if err := decode(r, &req); err != nil {
			return 0, nil, err
		}
		rt, err := app.AddRoute(req.Domain, req.Via)
		return http.StatusCreated, rt, err
	})
	handle("POST /v1/routes/via", func(r *http.Request) (int, any, error) {
		var req routeRequest
		if err := decode(r, &req); err != nil {
			return 0, nil, err
		}
		domain, err := router.NormalizeRouteInput(req.Domain)
		if err != nil {
			return 0, nil, err
		}
		rt, err := app.SetRouteVia(domain, req.Via)
		return http.StatusOK, rt, err
	})
	handle("DELETE /v1/routes", func(r *http.Request) (int, any, error) {
		rt, err := app.RemoveRoute(r.URL.Query().Get("domain"))
		return http.StatusOK, rt, err
	})
	handle("POST /v1/presets/{name}", func(r *http.Request) (int, any, error) {
		added, err := app.AddPreset(r.PathValue("name"), r.URL.Query().Get("via"))
		if added == nil {
			added = []router.Route{}
		}
		return http.StatusOK, added, err
	})
	handle("GET /v1/upstreams", func(*http.Request) (int, any, error) {
		return http.StatusOK, app.Upstreams(), nil
	})
	handle("POST /v1/upstreams", func(r *http.Request) (int, any, error) {
		var u config.Upstream
		if err := decode(r, &u); err != nil {
			return 0, nil, err
		}
		added, err := app.AddUpstream(u)
		return http.StatusCreated, added, err
	})
	handle("DELETE /v1/upstreams", func(r *http.Request) (int, any, error) {
		return http.StatusNoContent, nil, app.RemoveUpstream(r.URL.Query().Get("name"))
	})
	handle("GET /v1/ssh", func(r *http.Request) (int, any, error) {
		name := r.URL.Query().Get("name")
		out := []SSHInfo{}
		for _, u := range app.Status().Upstreams {
			if name == "" || u.Name == name {
				out = append(out, SSHInfo{Upstream: u.Name, Status: u.SSH, Log: app.SSHLog(u.Name)})
			}
		}
		if name != "" && len(out) == 0 {
			return 0, nil, fmt.Errorf("%w: %s", core.ErrUpstreamMissing, name)
		}
		return http.StatusOK, out, nil
	})
	handle("POST /v1/ssh/restart", func(r *http.Request) (int, any, error) {
		return http.StatusNoContent, nil, app.RestartUpstream(r.URL.Query().Get("name"))
	})
	handle("GET /v1/connections", func(*http.Request) (int, any, error) {
		return http.StatusOK, connections(app), nil
	})
	handle("DELETE /v1/connections", func(*http.Request) (int, any, error) {
		return http.StatusNoContent, nil, app.ClearConnections()
	})
	handle("PUT /v1/connections/log", func(r *http.Request) (int, any, error) {
		var req connectionLogRequest
		if err := decode(r, &req); err != nil {
			return 0, nil, err
		}
		return http.StatusNoContent, nil, app.SetConnectionLog(req.On)
	})
	handle("GET /v1/config", func(*http.Request) (int, any, error) {
		return http.StatusOK, app.Config(), nil
	})
	handle("GET /v1/snapshot", func(*http.Request) (int, any, error) {
		return http.StatusOK, Snapshot{Config: app.Config(), Status: app.Status(), Connections: connections(app)}, nil
	})
	handle("PUT /v1/routes", func(r *http.Request) (int, any, error) {
		var req updateRouteRequest
		if err := decode(r, &req); err != nil {
			return 0, nil, err
		}
		rt, err := app.UpdateRoute(req.Old, req.Domain, req.Via)
		return http.StatusOK, rt, err
	})
	handle("PUT /v1/upstreams", func(r *http.Request) (int, any, error) {
		var u config.Upstream
		if err := decode(r, &u); err != nil {
			return 0, nil, err
		}
		updated, err := app.UpdateUpstream(r.URL.Query().Get("name"), u)
		return http.StatusOK, updated, err
	})
	handle("PUT /v1/fallback", func(r *http.Request) (int, any, error) {
		var req fallbackRequest
		if err := decode(r, &req); err != nil {
			return 0, nil, err
		}
		via, err := app.SetFallback(req.Via)
		return http.StatusOK, fallbackRequest{Via: via}, err
	})
	handle("PUT /v1/language", func(r *http.Request) (int, any, error) {
		var req languageRequest
		if err := decode(r, &req); err != nil {
			return 0, nil, err
		}
		return http.StatusNoContent, nil, app.SetLanguage(req.Language)
	})
	handle("POST /v1/ssh/hostkey", func(r *http.Request) (int, any, error) {
		k, err := app.ScanHostKey(r.Context(), r.URL.Query().Get("name"))
		return http.StatusOK, k, err
	})
	handle("POST /v1/ssh/trust", func(r *http.Request) (int, any, error) {
		var req trustRequest
		if err := decode(r, &req); err != nil {
			return 0, nil, err
		}
		return http.StatusNoContent, nil, app.TrustHostKey(req.Name, req.Fingerprint)
	})
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		streamEvents(w, r, app)
	})

	// BaseContext 를 ctx 로 두어, 종료할 때 이벤트 스트림이 Shutdown 을 붙잡지 않게 한다.
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	stop := context.AfterFunc(ctx, func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	})
	defer stop()
	err := srv.Serve(ln)
	_ = os.Remove(ln.Addr().String())
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func connections(app *core.App) []Connection {
	recent := app.RecentConnections()
	out := make([]Connection, len(recent))
	for i, e := range recent {
		out[i] = toConnection(e)
	}
	return out
}

// streamEvents 는 구독을 NDJSON 으로 흘려보낸다. 인스턴스가 멈춰 bus 가 닫히면
// 스트림도 끝나고, 클라이언트는 그것으로 연결이 끊겼음을 안다.
func streamEvents(w http.ResponseWriter, r *http.Request, app *core.App) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, errors.New("streaming unsupported"))
		return
	}
	sub, unsub := app.Subscribe(1024)
	defer unsub()
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	enc := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-sub:
			if !ok {
				return
			}
			we, send, err := encodeEvent(e)
			if err != nil || !send {
				continue
			}
			if enc.Encode(we) != nil {
				return
			}
			fl.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	body := errorBody{Error: err.Error()}
	code := http.StatusBadRequest
	for _, s := range sentinels {
		if errors.Is(err, s.err) {
			body.Kind, code = s.kind, s.code
			break
		}
	}
	writeJSON(w, code, body)
}

// Client 는 실행 중인 인스턴스와 통신한다.
type Client struct {
	hc *http.Client
	// stream 은 타임아웃이 없는 client 다: 이벤트 스트림은 attach 가 끝날 때까지 열려 있다.
	stream *http.Client
}

func NewClient(path string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}
	return &Client{
		hc:     &http.Client{Timeout: 10 * time.Second, Transport: tr},
		stream: &http.Client{Transport: tr},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://routebox"+path, rd)
	if err != nil {
		return err
	}
	resp, err := send(c.hc, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// send 는 요청을 보내고, 소켓이 없으면 ErrNotRunning 을, 오류 응답이면
// sentinel 을 되살린 오류를 돌려준다. 성공하면 body 는 호출자가 닫는다.
func send(hc *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := hc.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, ErrNotRunning
		}
		return nil, fmt.Errorf("control request: %w", err)
	}
	if resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	var eb errorBody
	_ = json.NewDecoder(resp.Body).Decode(&eb)
	for _, s := range sentinels {
		if s.kind == eb.Kind {
			return nil, &remoteError{msg: eb.Error, sentinel: s.err}
		}
	}
	return nil, errors.New(eb.Error)
}

func (c *Client) Status(ctx context.Context) (core.Status, error) {
	var s core.Status
	return s, c.do(ctx, http.MethodGet, "/v1/status", nil, &s)
}

func (c *Client) Routes(ctx context.Context) ([]router.Route, error) {
	var r []router.Route
	return r, c.do(ctx, http.MethodGet, "/v1/routes", nil, &r)
}

func (c *Client) AddRoute(ctx context.Context, domain, via string) (router.Route, error) {
	var r router.Route
	return r, c.do(ctx, http.MethodPost, "/v1/routes", routeRequest{Domain: domain, Via: via}, &r)
}

func (c *Client) SetRouteVia(ctx context.Context, domain, via string) (router.Route, error) {
	var r router.Route
	return r, c.do(ctx, http.MethodPost, "/v1/routes/via", routeRequest{Domain: domain, Via: via}, &r)
}

func (c *Client) RemoveRoute(ctx context.Context, domain string) (router.Route, error) {
	var r router.Route
	return r, c.do(ctx, http.MethodDelete, "/v1/routes?domain="+url.QueryEscape(domain), nil, &r)
}

func (c *Client) AddPreset(ctx context.Context, name, via string) ([]router.Route, error) {
	var r []router.Route
	return r, c.do(ctx, http.MethodPost, "/v1/presets/"+url.PathEscape(name)+"?via="+url.QueryEscape(via), nil, &r)
}

func (c *Client) Upstreams(ctx context.Context) ([]config.Upstream, error) {
	var u []config.Upstream
	return u, c.do(ctx, http.MethodGet, "/v1/upstreams", nil, &u)
}

func (c *Client) AddUpstream(ctx context.Context, u config.Upstream) (config.Upstream, error) {
	var out config.Upstream
	return out, c.do(ctx, http.MethodPost, "/v1/upstreams", u, &out)
}

func (c *Client) RemoveUpstream(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/upstreams?name="+url.QueryEscape(name), nil, nil)
}

func (c *Client) SSH(ctx context.Context, name string) ([]SSHInfo, error) {
	var s []SSHInfo
	return s, c.do(ctx, http.MethodGet, "/v1/ssh?name="+url.QueryEscape(name), nil, &s)
}

func (c *Client) RestartSSH(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodPost, "/v1/ssh/restart?name="+url.QueryEscape(name), nil, nil)
}

func (c *Client) Connections(ctx context.Context) ([]Connection, error) {
	var out []Connection
	return out, c.do(ctx, http.MethodGet, "/v1/connections", nil, &out)
}

func (c *Client) ClearConnections(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, "/v1/connections", nil, nil)
}

func (c *Client) SetConnectionLog(ctx context.Context, on bool) error {
	return c.do(ctx, http.MethodPut, "/v1/connections/log", connectionLogRequest{On: on}, nil)
}

func (c *Client) Config(ctx context.Context) (config.Config, error) {
	var cfg config.Config
	return cfg, c.do(ctx, http.MethodGet, "/v1/config", nil, &cfg)
}

func (c *Client) Snapshot(ctx context.Context) (Snapshot, error) {
	var s Snapshot
	return s, c.do(ctx, http.MethodGet, "/v1/snapshot", nil, &s)
}

func (c *Client) UpdateRoute(ctx context.Context, oldDomain, domain, via string) (router.Route, error) {
	var r router.Route
	return r, c.do(ctx, http.MethodPut, "/v1/routes", updateRouteRequest{Old: oldDomain, Domain: domain, Via: via}, &r)
}

func (c *Client) UpdateUpstream(ctx context.Context, oldName string, u config.Upstream) (config.Upstream, error) {
	var out config.Upstream
	return out, c.do(ctx, http.MethodPut, "/v1/upstreams?name="+url.QueryEscape(oldName), u, &out)
}

// SetFallback 은 매칭되지 않은 연결이 나갈 곳을 바꾸고 정규화된 값을 돌려준다.
func (c *Client) SetFallback(ctx context.Context, via string) (string, error) {
	var out fallbackRequest
	return out.Via, c.do(ctx, http.MethodPut, "/v1/fallback", fallbackRequest{Via: via}, &out)
}

func (c *Client) SetLanguage(ctx context.Context, lang string) error {
	return c.do(ctx, http.MethodPut, "/v1/language", languageRequest{Language: lang}, nil)
}

// ScanHostKey 는 실행 중인 인스턴스가 업스트림의 host key 를 받아 오게 한다.
// ssh 연결을 두 번 하므로 일반 요청 타임아웃 대신 ctx 로 기다린다.
func (c *Client) ScanHostKey(ctx context.Context, name string) (ssh.HostKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://routebox/v1/ssh/hostkey?name="+url.QueryEscape(name), nil)
	if err != nil {
		return ssh.HostKey{}, err
	}
	resp, err := send(c.stream, req)
	if err != nil {
		return ssh.HostKey{}, err
	}
	defer resp.Body.Close()
	var k ssh.HostKey
	return k, json.NewDecoder(resp.Body).Decode(&k)
}

func (c *Client) TrustHostKey(ctx context.Context, name, fingerprint string) error {
	return c.do(ctx, http.MethodPost, "/v1/ssh/trust", trustRequest{Name: name, Fingerprint: fingerprint}, nil)
}

// Events 는 실행 중인 인스턴스의 이벤트 스트림을 연다. 채널은 ctx 가 취소되거나
// 인스턴스가 멈추거나 연결이 끊기면 닫힌다.
func (c *Client) Events(ctx context.Context) (<-chan events.Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://routebox/v1/events", nil)
	if err != nil {
		return nil, err
	}
	resp, err := send(c.stream, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan events.Event, 256)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		dec := json.NewDecoder(resp.Body)
		for {
			var w wireEvent
			if err := dec.Decode(&w); err != nil {
				return
			}
			e, err := decodeEvent(w)
			if err != nil {
				continue
			}
			select {
			case ch <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}
