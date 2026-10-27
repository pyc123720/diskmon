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

	now := time.Now()
	day := func(h, m int) time.Time { return time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, time.Local) }
	next := day(3, 5).AddDate(0, 0, 1)

	cfg := Config{LazyAuto: "off", LazyAutoHour: 3}
	if lazyAutoDue(cfg, st, day(3, 5)) {
		t.Fatal("关闭时不应触发")
	}
	cfg.LazyAuto = "daily"
	if !lazyAutoDue(cfg, st, day(3, 5)) {
		t.Fatal("03:05 应触发")
	}
	if lazyAutoDue(cfg, st, day(4, 5)) {
		t.Fatal("04:05 不应触发")
	}
	st.AddAlert("lazy-auto", "自动清理已启动")
	if lazyAutoDue(cfg, st, day(3, 30)) {
		t.Fatal("同一天不应重复触发")
	}
	if !lazyAutoDue(cfg, st, next) {
		t.Fatal("第二天应触发")
	}
	cfg.LazyAuto = "weekly"
	sun := now
	for sun.Weekday() != time.Sunday {
		sun = sun.AddDate(0, 0, 1)
	}
	if !lazyAutoDue(cfg, st, time.Date(sun.Year(), sun.Month(), sun.Day(), 3, 5, 0, 0, time.Local)) {
		t.Fatal("周日应触发 weekly")
	}
	if lazyAutoDue(cfg, st, day(3, 5)) {
		t.Fatal("非周日不应触发 weekly")
	}
}
