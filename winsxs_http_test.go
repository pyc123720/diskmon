package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 这一条是**真的发一次 HTTP 请求**：起 httptest 服务器（用的就是生产那个 newHTTPHandler），
// 客户端走 net/http 打过去。
//
// 为什么非要多这一层：这个 mux 是单个 catch-all，注册了分支不等于能被访问到
// （今晚已经在虚构的 /apps 上栽过一次）。只有真请求一次、真看 200 和正文，
// 才算证明「这个路由存在且内容对」。
func TestWinsxsRealHTTPRequest(t *testing.T) {
	componentStoreReset()
	old := componentStoreRun
	componentStoreRun = func() ComponentStore {
		return ComponentStore{Src: "dism", Err: "需要管理员权限：DISM 的组件库分析必须以提升的权限运行（预计 1–2 分钟）"}
	}
	defer func() { componentStoreRun = old; componentStoreReset() }()

	cfg := Config{DataDir: t.TempDir(), Port: 0}
	st := openStore(cfg)
	srv := httptest.NewServer(newHTTPHandler(cfg, st))
	defer srv.Close()

	// 1) GET 页面：必须 200，且成本与权限提示都在正文里。
	resp, err := http.Get(srv.URL + "/winsxs")
	if err != nil {
		t.Fatalf("GET /winsxs 失败：%v", err)
	}
	body := readAll(t, resp)
	t.Logf("GET %s/winsxs → %d %s，正文 %d 字节", srv.URL, resp.StatusCode, resp.Header.Get("Content-Type"), len(body))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /winsxs 期望 200，实际 %d", resp.StatusCode)
	}
	for _, want := range []string{"约 1–2 分钟", "需要管理员", `action="/winsxs/run"`, "还没跑过"} {
		if !strings.Contains(body, want) {
			t.Fatalf("页面缺少 %q：\n%s", want, body)
		}
	}

	// 2) GET /winsxs/run：链接/刷新都不能触发，必须 405。
	resp2, err := http.Get(srv.URL + "/winsxs/run")
	if err != nil {
		t.Fatalf("GET /winsxs/run 失败：%v", err)
	}
	b2 := readAll(t, resp2)
	t.Logf("GET %s/winsxs/run → %d", srv.URL, resp2.StatusCode)
	if resp2.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /winsxs/run 期望 405，实际 %d", resp2.StatusCode)
	}
	if !strings.Contains(b2, "必须用页面上的按钮") {
		t.Fatalf("405 页面没说清为什么：%s", b2)
	}

	// 3) POST 按钮：303 回页面，后台跑完，页面上出现「没有可信结果 + 原因」。
	//    这里特意关掉自动跟随：要看的就是 303 本身，而不是跟过去之后的 200。
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp3, err := noFollow.Post(srv.URL+"/winsxs/run", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST /winsxs/run 失败：%v", err)
	}
	readAll(t, resp3)
	t.Logf("POST %s/winsxs/run → %d（Location=%s）", srv.URL, resp3.StatusCode, resp3.Header.Get("Location"))
	if resp3.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST 期望 303，实际 %d", resp3.StatusCode)
	}
	_ = io.Discard
	waitComponentStoreDone(t)

	resp4, err := http.Get(srv.URL + "/winsxs")
	if err != nil {
		t.Fatalf("跑完再 GET 失败：%v", err)
	}
	b4 := readAll(t, resp4)
	if !strings.Contains(b4, "没拿到可信结果") || !strings.Contains(b4, "需要管理员权限") {
		t.Fatalf("失败状态必须把原因摊在页面上：\n%s", b4)
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读正文失败：%v", err)
	}
	return string(b)
}
