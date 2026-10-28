package main

import (
	"net/http"
	"net/http/httptest"

	"testing"
)

func guardHandler(t *testing.T) http.Handler {
	t.Helper()
	cfg := defaultConfig()
	cfg.DataDir = t.TempDir()
	return newHTTPHandler(cfg, &Store{})
}

func do(h http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r.Host = "127.0.0.1:8766" // 服务只监听回环地址，Host 永远是它
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// 任何网页都能向 127.0.0.1 发请求，跨站的清理请求必须被拒。
func TestCrossSiteRequestRejected(t *testing.T) {
	h := guardHandler(t)
	w := do(h, "GET", "/q/purge", map[string]string{"Sec-Fetch-Site": "cross-site"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("跨站请求应 403，实际 %d", w.Code)
	}
	w = do(h, "GET", "/", map[string]string{"Origin": "https://evil.example"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("跨源 Origin 应 403，实际 %d", w.Code)
	}
	// 同源 / 无头（curl、MCP）不受影响
	if w = do(h, "GET", "/", map[string]string{"Origin": "http://127.0.0.1:8766"}); w.Code != 200 {
		t.Fatalf("同源应放行，实际 %d", w.Code)
	}
	if w = do(h, "GET", "/", nil); w.Code != 200 {
		t.Fatalf("无 Origin 应放行，实际 %d", w.Code)
	}
}

// 永久删除不能靠链接 / 刷新触发。
func TestPurgeRequiresPost(t *testing.T) {
	h := guardHandler(t)
	if w := do(h, "GET", "/q/purge", nil); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /q/purge 应 405，实际 %d", w.Code)
	}
	if w := do(h, "POST", "/q/purge", nil); w.Code != http.StatusSeeOther {
		t.Fatalf("POST /q/purge 应重定向 303，实际 %d", w.Code)
	}
}

// 带副作用的老页面普遍用 GET 链接，尾斜杠也要能正确跳转。
func TestTrailingSlashRedirect(t *testing.T) {
	h := guardHandler(t)
	w := do(h, "GET", "/clean/?a=1", nil)
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("尾斜杠应 301，实际 %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/clean?a=1" {
		t.Fatalf("跳转目标不对：%q", loc)
	}
}

func TestSameOriginHelper(t *testing.T) {
	cases := []struct {
		origin, host string
		want         bool
	}{
		{"http://127.0.0.1:8766", "127.0.0.1:8766", true},
		{"http://localhost:8766", "127.0.0.1:8766", true},
		{"http://127.0.0.1:8799", "127.0.0.1:8766", true},
		{"https://evil.example", "127.0.0.1:8766", false},
		{"http://192.168.1.5:8766", "127.0.0.1:8766", false},
		{"", "127.0.0.1:8766", false},
	}
	for _, c := range cases {
		if got := sameOrigin(c.origin, c.host); got != c.want {
			t.Errorf("sameOrigin(%q,%q)=%v 期望 %v", c.origin, c.host, got, c.want)
		}
	}
	if !isLoopbackHost("127.0.0.1:8766") || !isLoopbackHost("localhost:8766") ||
		!isLoopbackHost("[::1]:8766") || isLoopbackHost("192.168.1.5:8766") {
		t.Fatal("isLoopbackHost 判定不对")
	}
}
