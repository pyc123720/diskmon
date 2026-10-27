package main

import (
	"fmt"
	"time"
)

// ---------- 定时懒人清理 ----------

func autoLazyEnabled(cfg Config) bool {
	return cfg.LazyAuto == "daily" || cfg.LazyAuto == "weekly"
}

func sameLocalDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// 是否到了该跑的时间（纯逻辑，便于测试）
func lazyAutoDue(cfg Config, st *Store, now time.Time) bool {
	if !autoLazyEnabled(cfg) {
		return false
	}
	if now.Hour() != cfg.LazyAutoHour {
		return false
	}
	if cfg.LazyAuto == "weekly" && now.Weekday() != time.Sunday {
		return false
	}
	for _, a := range st.Alerts() {
		if a.Kind == "lazy-auto" && sameLocalDay(time.Unix(a.TS, 0), now) {
			return false // 今天已经跑过
		}
	}
	return true
}

func lazyAutoLoop(cfg Config, st *Store) {
	time.Sleep(2 * time.Minute) // 启动后先让扫描/告警稳定
	tick := time.NewTicker(10 * time.Minute)
	defer tick.Stop()
	for {
		c := getConfig()
		now := time.Now()
		if lazyAutoDue(c, st, now) {
			q := openQuarantine(c)
			if startLazy(c, st, q) {
				label := "每天"
				if c.LazyAuto == "weekly" {
					label = "每周日"
				}
				st.AddAlert("lazy-auto", fmt.Sprintf("自动清理已启动（%s %02d:00，保守方案）", label, c.LazyAutoHour))
				logf("lazy-auto: 已启动（%s %02d:00）", label, c.LazyAutoHour)
			}
		}
		<-tick.C
	}
}

// 给 UI 用的一句话
func lazyScheduleText(cfg Config) string {
	if !autoLazyEnabled(cfg) {
		return "未开启自动清理"
	}
	when := fmt.Sprintf("每天 %02d:00", cfg.LazyAutoHour)
	if cfg.LazyAuto == "weekly" {
		when = fmt.Sprintf("每周日 %02d:00", cfg.LazyAutoHour)
	}
	return "自动清理：" + when + "（保守方案，可撤销）"
}
