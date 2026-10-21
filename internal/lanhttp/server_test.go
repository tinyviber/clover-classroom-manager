package lanhttp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tinyviber/clover-classroom-manager/internal/labsvc"
	"github.com/tinyviber/clover-classroom-manager/internal/lan"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	static := fstest.MapFS{
		"index.html":     {Data: []byte("<html><head></head><body>ui</body></html>")},
		"assets/app.js":  {Data: []byte("console.log(1)")},
		"assets/app.css": {Data: []byte("body{}")},
	}
	s, err := New(static, t.TempDir(), "http://127.0.0.1:47825")
	if err != nil {
		t.Fatal(err)
	}
	id, err := lan.GenIdentity()
	if err != nil {
		t.Fatal(err)
	}
	s.Bind(labsvc.New(lan.NewServer(id, 0), s, t.TempDir()))
	return s
}

// 非回环请求拿不到注入 token 的页面——token 就是管理凭据。
func TestStaticTokenNotLeakedToLAN(t *testing.T) {
	s := testServer(t)

	req := httptest.NewRequest("GET", "/", nil) // RemoteAddr 默认 192.0.2.1（非回环）
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("LAN 无凭据访问页面应 401, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), s.Token()) {
		t.Fatal("未认证响应里泄露了 token")
	}

	// SPA 回退路径同样收紧
	req = httptest.NewRequest("GET", "/admin/devices", nil)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("SPA 回退路径应 401, got %d", rec.Code)
	}
}

// 本机回环浏览器照常注入；LAN 客户端带有效 ?k 也放行。
func TestStaticInjectionLoopbackAndToken(t *testing.T) {
	s := testServer(t)

	req := httptest.NewRequest("GET", "http://127.0.0.1:47825/", nil)
	req.RemoteAddr = "127.0.0.1:53210"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), s.Token()) {
		t.Fatalf("回环页面应注入 token: %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "http://x.local/?k="+s.Token(), nil) // LAN + 凭据
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), s.Token()) {
		t.Fatalf("带凭据的 LAN 页面应注入 token: %d", rec.Code)
	}

	// 非 html 静态资源不含机密，不带凭据也可访问（页面加载需要）
	req = httptest.NewRequest("GET", "/assets/app.js", nil)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("静态资源应可访问: %d", rec.Code)
	}
}

// DNS rebinding：攻击者域名解析到 127.0.0.1 时，浏览器会以回环地址
// 连上本机服务但 Host 仍是攻击者域名——绝不能把 token 注进它的页面。
func TestStaticHostRebindingRejected(t *testing.T) {
	s := testServer(t)

	for _, host := range []string{"evil.example", "127.0.0.1.evil.com", "localhost.evil.com", "example.com"} {
		req := httptest.NewRequest("GET", "http://"+host+"/", nil)
		req.RemoteAddr = "127.0.0.1:53210" // 连接来自回环，Host 却是外人
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("Host=%s 回环连接应 401: %d", host, rec.Code)
		}
		if strings.Contains(rec.Body.String(), s.Token()) {
			t.Fatalf("Host=%s 的响应泄露了 token", host)
		}
	}
	// 但合法本机 Host 照常注入
	for _, host := range []string{"127.0.0.1", "127.0.0.1:47825", "localhost", "localhost:47825", "[::1]:47825"} {
		req := httptest.NewRequest("GET", "http://"+host+"/", nil)
		req.RemoteAddr = "[::1]:9999"
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), s.Token()) {
			t.Fatalf("Host=%s 本机页面应注入 token: %d", host, rec.Code)
		}
	}
}

// API 一律要 token；denylist 方法即使带 token 也调不到。
func TestCallAuthAndDenylist(t *testing.T) {
	s := testServer(t)

	call := func(body, token, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "http://127.0.0.1:47825/api/call", strings.NewReader(body))
		if token != "" {
			req.Header.Set("X-Lab-Token", token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}

	if rec := call(`{"m":"Lab.Devices","a":[]}`, "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("无 token 应 401: %d", rec.Code)
	}
	if rec := call(`{"m":"Lab.ExportClientPackageTo","a":["/tmp/x","1.2.3.4"]}`, s.Token(), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("denylist 方法应 404: %d", rec.Code)
	}
	// 跨站 Origin 拒绝（带 token 也一样）
	if rec := call(`{"m":"Lab.Devices","a":[]}`, s.Token(), "http://evil.example"); rec.Code != http.StatusForbidden {
		t.Fatalf("跨站 Origin 应 403: %d", rec.Code)
	}
	if rec := call(`{"m":"Lab.Devices","a":[]}`, s.Token(), "http://127.0.0.1:47825"); rec.Code != http.StatusOK {
		t.Fatalf("同源调用应 200: %d", rec.Code)
	}
}

// Go 方法返回的非 nil error 要变成 HTTP 错误，而不是被序列化成 {} 吞掉。
func TestCallErrorPropagation(t *testing.T) {
	s := testServer(t)
	s.methods["Lab.Boom"] = reflect.ValueOf(func() error { return errors.New("boom") })
	s.methods["Lab.Pair"] = reflect.ValueOf(func() (string, error) { return "ok-val", nil })

	post := func(m string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/call", strings.NewReader(`{"m":"`+m+`","a":[]}`))
		req.Header.Set("X-Lab-Token", s.Token())
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}

	if rec := post("Lab.Boom"); rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("error 应变 500 且带消息: %d %q", rec.Code, rec.Body.String())
	}
	if rec := post("Lab.Pair"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok-val") {
		t.Fatalf("多返回值应取首个非 error: %d %q", rec.Code, rec.Body.String())
	}
}
