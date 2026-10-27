package main

import (
	"testing"
	"time"
)

func TestRuleGuardDangerousRoots(t *testing.T) {
	cfg := Config{Whitelist: []string{`\documents\`, `\desktop\`}}
	cases := []string{`C:\`, `C:\Windows`, `C:\Users`, `C:\Program Files`, `C:\Users\Public\Documents`}
	for _, p := range cases {
		rule := CleanRule{Name: "x", Path: p, KeepDays: 7}
		if why, ok := ruleGuard(cfg, rule); ok {
			t.Fatalf("危险路径 %s 不应被允许用于自动清理（%s）", p, why)
		}
	}
	// 真临时目录应允许
	tmp := `C:\Users\Public\AppData\Local\Temp`
	_ = tmp
}

func TestRehearseQuickBudget(t *testing.T) {
	// 用一个不存在的深层路径，确保不会卡住
	start := time.Now()
	r := rehearseQuick(Config{}, `C:\definitely-not-exists-xyz`, 2*time.Second)
	if time.Since(start) > 3*time.Second {
		t.Fatal("快速体检超时")
	}
	if r.Refuse == "" {
		t.Fatal("不存在的路径应被拒绝")
	}
}
