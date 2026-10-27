package main

import (
	"os"
	"testing"
	"time"
)

func TestLazyAutoDue(t *testing.T) {
	base, err := os.MkdirTemp(".", "lazyauto-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	st := openStore(Config{DataDir: base})

	// 未开启
	cfg := Config{LazyAuto: "off", LazyAutoHour: 3}
	if lazyAutoDue(cfg, st, time.Date(2026, 10, 27, 3, 5, 0, 0, time.Local)) {
		t.Fatal("关闭时不应触发")
	}

	// 每天 03:00
	cfg.LazyAuto = "daily"
	if !lazyAutoDue(cfg, st, time.Date(2026, 10, 27, 3, 5, 0, 0, time.Local)) {
		t.Fatal("03:05 应触发")
	}
	if lazyAutoDue(cfg, st, time.Date(2026, 10, 27, 4, 5, 0, 0, time.Local)) {
		t.Fatal("04:05 不应触发")
	}

	// 今天已跑过
	st.AddAlert("lazy-auto", "自动清理已启动")
	if lazyAutoDue(cfg, st, time.Date(2026, 10, 27, 3, 30, 0, 0, time.Local)) {
		t.Fatal("同一天不应重复触发")
	}
	// 第二天可以
	if !lazyAutoDue(cfg, st, time.Date(2026, 10, 28, 3, 5, 0, 0, time.Local)) {
		t.Fatal("第二天应触发")
	}

	// weekly 只在周日
	cfg.LazyAuto = "weekly"
	sunday := time.Date(2026, 11, 1, 3, 5, 0, 0, time.Local) // 2026-11-01 是周日
	if sunday.Weekday() != time.Sunday {
		t.Fatalf("测试前提错误：%v 不是周日", sunday.Weekday())
	}
	if !lazyAutoDue(cfg, st, sunday) {
		t.Fatal("周日应触发 weekly")
	}
	if lazyAutoDue(cfg, st, time.Date(2026, 10, 28, 3, 5, 0, 0, time.Local)) {
		t.Fatal("周三不应触发 weekly")
	}
}
