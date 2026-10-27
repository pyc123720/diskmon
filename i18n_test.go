package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLangFromRequest(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if got := langFromRequest(r); got != "en" {
		t.Fatalf("Accept-Language 应识别为 en，实际 %s", got)
	}
	r2 := httptest.NewRequest("GET", "/?lang=en", nil)
	if got := langFromRequest(r2); got != "en" {
		t.Fatalf("?lang=en 应生效，实际 %s", got)
	}
	r3 := httptest.NewRequest("GET", "/", nil)
	r3.Header.Set("Accept-Language", "zh-CN")
	if got := langFromRequest(r3); got != "zh" {
		t.Fatalf("中文环境应为 zh，实际 %s", got)
	}
	r4 := httptest.NewRequest("GET", "/", nil)
	r4.AddCookie(&http.Cookie{Name: langCookie, Value: "zh"})
	r4.Header.Set("Accept-Language", "en")
	if got := langFromRequest(r4); got != "zh" {
		t.Fatalf("cookie 优先于浏览器语言，实际 %s", got)
	}
	r5 := httptest.NewRequest("GET", "/?lang=en", nil)
	r5.AddCookie(&http.Cookie{Name: langCookie, Value: "zh"})
	if got := langFromRequest(r5); got != "en" {
		t.Fatalf("显式 ?lang= 应覆盖 cookie，实际 %s", got)
	}
}

func TestTranslate(t *testing.T) {
	old := currentLang()
	curLang.Store("zh")
	if translateHTML("总览") != "总览" {
		t.Fatal("中文模式不应翻译")
	}
	curLang.Store("en")
	if got := translateHTML("<a>总览</a>"); got != "<a>Overview</a>" {
		t.Fatalf("英文应翻译，实际 %q", got)
	}
	if got := translateHTML("清理中心"); got != "Cleanup center" {
		t.Fatalf("长词优先替换，实际 %q", got)
	}
	curLang.Store(old)
}
