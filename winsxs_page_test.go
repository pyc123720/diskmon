package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testWinsxsHandler 建一个只跑在内存里的 handler：DataDir 指向临时目录，
// 目的是「真的发一次 HTTP 请求」，而不是只调函数看字符串。
func testWinsxsHandler(t *testing.T) (http.Handler, func()) {
	t.Helper()
	componentStoreReset()
	cfg := Config{DataDir: t.TempDir(), Port: 0}
	st := openStore(cfg)
	return newHTTPHandler(cfg, st), componentStoreReset
}

// 最关键的一条：**打开页面不许跑 DISM**。
func TestWinsxsPageDoesNotRunDismOnLoad(t *testing.T) {
	h, done := testWinsxsHandler(t)
	defer done()

	calls := 0
	old := componentStoreRun
	componentStoreRun = func() ComponentStore { calls++; return ComponentStore{Src: "dism"} }
	defer func() { componentStoreRun = old }()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/winsxs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码应当是 200，实际 %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"约 1–2 分钟", "需要管理员", "还没跑过", `action="/winsxs/run"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("页面缺少 %q：\n%s", want, body)
		}
	}
	// 给它一点时间去「跑」——如果加载顺手起了 DISM，这里会被抓到。
	time.Sleep(50 * time.Millisecond)
	if calls != 0 {
		t.Fatalf("页面加载不该触发 DISM，实际调用了 %d 次", calls)
	}
}

// 点按钮：POST 起来 → 跑完 → 页面上看到失败原因原文（非管理员就是这条路）。
func TestWinsxsPostRunsAndShowsFailure(t *testing.T) {
	h, done := testWinsxsHandler(t)
	defer done()

	old := componentStoreRun
	componentStoreRun = func() ComponentStore {
		return ComponentStore{Src: "dism", Err: "需要管理员权限：DISM 的组件库分析必须以提升的权限运行（预计 1–2 分钟）"}
	}
	defer func() { componentStoreRun = old }()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/winsxs/run", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST 之后应当 303 回页面，实际 %d", rec.Code)
	}
	waitComponentStoreDone(t)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/winsxs", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "没拿到可信结果") || !strings.Contains(body, "需要管理员权限") {
		t.Fatalf("失败状态必须把原因摊开：\n%s", body)
	}
}

// 链接 / 刷新 / 外部请求都不能触发：必须 POST。
func TestWinsxsRunRejectsGet(t *testing.T) {
	h, done := testWinsxsHandler(t)
	defer done()

	calls := 0
	old := componentStoreRun
	componentStoreRun = func() ComponentStore { calls++; return ComponentStore{} }
	defer func() { componentStoreRun = old }()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/winsxs/run", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 触发应当 405，实际 %d", rec.Code)
	}
	if calls != 0 {
		t.Fatalf("GET 不该真的跑 DISM，实际 %d 次", calls)
	}
}

// 成功时把 DISM 的表格和「来源」字样显示出来。
func TestWinsxsPageShowsSuccess(t *testing.T) {
	_, done := testWinsxsHandler(t)
	defer done()

	cs, err := parseComponentStore(dismChineseShape)
	if err != nil {
		t.Fatal(err)
	}
	componentStoreMu.Lock()
	componentStoreCurrent = componentStoreState{Done: true, Result: cs}
	componentStoreMu.Unlock()

	body := winsxsPageHTML()
	if !strings.Contains(body, "来源：<b>DISM</b>") || !strings.Contains(body, "组件库实际大小") {
		t.Fatalf("成功状态没显示 DISM 结果：\n%s", body)
	}
}

// 跑着的时候要能看出「在跑、等了多久」，而不是一片空白。
func TestWinsxsPageShowsRunning(t *testing.T) {
	_, done := testWinsxsHandler(t)
	defer done()

	componentStoreMu.Lock()
	componentStoreCurrent = componentStoreState{Running: true, StartAt: time.Now().Unix()}
	componentStoreMu.Unlock()
	body := winsxsPageHTML()
	if !strings.Contains(body, "正在分析") || !strings.Contains(body, "已经等了") {
		t.Fatalf("跑着的时候页面说不清状态：\n%s", body)
	}
}

// WinSxS 那一类的页面上必须有入口，且写明成本与权限。
func TestWinSxsCategoryLinksToWinsxs(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	st := openStore(cfg)
	st.SetScan(&Scan{
		Root:        `C:\`,
		Total:       18 << 30,
		Files:       100,
		Cats:        map[string]uint64{"system_winsxs": 18 << 30, "temp": 1 << 30},
		CatsShared:  map[string]uint64{"system_winsxs": 2 << 30},
		HlProbeMB:   64,
		Shared:      2 << 30,
		Unique:      16 << 30,
		TS:          time.Now().Unix(),
	})
	body := cleanPage(cfg, st, "system_winsxs")
	for _, want := range []string{`href="/winsxs"`, "约 1–2 分钟", "需要管理员"} {
		if !strings.Contains(body, want) {
			t.Fatalf("WinSxS 分类页缺少 %q：\n%s", want, body)
		}
	}
}

func waitComponentStoreDone(t *testing.T) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if componentStoreSnapshot().Done {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("分析没有在预期时间内结束")
}
