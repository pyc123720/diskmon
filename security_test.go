package main

import (
	"strings"
	"testing"
)

func TestSafeBack(t *testing.T) {
	cases := map[string]string{
		"/settings":        "/settings",
		"/detail?x=1":      "/detail?x=1",
		"//evil.example":   "/",
		"///evil.example":  "/",
		`/a\b`:             "/",
		"http://evil.com":  "/",
		"javascript:alert": "/",
		"":                 "/",
	}
	for in, want := range cases {
		if got := safeBack(in); got != want {
			t.Fatalf("safeBack(%q)=%q，期望 %q", in, got, want)
		}
	}
}

func TestCSVCell(t *testing.T) {
	if csvCell("normal") != "normal" {
		t.Fatal("普通内容不应被改写")
	}
	for _, s := range []string{"=1+1", "+cmd", "-2", "@SUM(A1)", "\tx"} {
		if !strings.HasPrefix(csvCell(s), "'") {
			t.Fatalf("危险前缀 %q 应被加上单引号", s)
		}
	}
}
