package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// walk-count 只接受盘符。传路径进去必须被拒绝，
// 而不是拼出一个不存在的根、报 0、再声称「没有漏」。
func TestWalkCountLetter(t *testing.T) {
	ok := map[string]string{
		"C":   "C",
		"c":   "C",
		"C:":  "C",
		`C:\`: "C",
		"d:":  "D",
	}
	for in, want := range ok {
		got, good := walkCountLetter(in)
		if !good || got != want {
			t.Fatalf("walkCountLetter(%q)=(%q,%v)，期望 (%q,true)", in, got, good, want)
		}
	}

	// 这些都不是盘符：路径、多字符、空、符号、盘符+子路径
	bad := []string{
		``,
		`C:\Users\me\dir`,
		`C:\Users`,
		`Users`,
		`CD`,
		`1`,
		`*`,
		`C:\Users\me\dir\`,
	}
	for _, in := range bad {
		if got, good := walkCountLetter(in); good {
			t.Fatalf("walkCountLetter(%q) 接受成了 %q，期望拒绝", in, got)
		}
	}
}

// /detail/<未知分区> 必须回和其它 404 一样的完整页面。
// 以前它只回一行裸文本（45 字节），浏览器里就是一行没有样式的字。
func TestDetailUnknownSectionIsFullPage(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), Port: 0}
	st := openStore(cfg)
	srv := httptest.NewServer(newHTTPHandler(cfg, st))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/detail/no-such-section")
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 404 {
		t.Fatalf("状态码=%d，期望 404", resp.StatusCode)
	}
	text := string(body)
	if !strings.Contains(text, "<html") {
		t.Fatalf("404 正文不是完整页面（没看到 <html>），实际 %d 字节：%.80q", len(body), text)
	}
	if !strings.Contains(text, "返回全部数据") {
		t.Fatalf("404 正文里没有返回入口，实际 %d 字节", len(body))
	}
	if len(body) < 2000 {
		t.Fatalf("404 正文只有 %d 字节，看起来还是裸文本而不是页面", len(body))
	}

	// 未知分区要说出是哪个分区，否则用户不知道该删什么
	if !strings.Contains(text, "no-such-section") {
		t.Fatalf("404 正文没提具体分区名")
	}
}
