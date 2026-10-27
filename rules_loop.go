package main

import (
	"fmt"
	"time"
)

// inWindow 判断当前时间是否在 [start,end) 内，支持跨零点（如 22:00–06:00）
func inWindow(now time.Time, start, end string) bool {
	parse := func(s string) (int, bool) {
		var h, m int
		if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
			return 0, false
		}
		if h < 0 || h > 23 || m < 0 || m > 59 {
			return 0, false
		}
		return h*60 + m, true
	}
	s, ok1 := parse(start)
	e, ok2 := parse(end)
	if !ok1 || !ok2 || s == e {
		return true
	}
	cur := now.Hour()*60 + now.Minute()
	if s < e {
		return cur >= s && cur < e
	}
	return cur >= s || cur < e
}

// 每日自动扫描：启动 45 秒后检查一次，之后每小时检查，超过 auto_scan_hours 就静默扫一份
func autoScanLoop(cfg Config, st *Store) {
	time.Sleep(45 * time.Second)
	for {
		c := getConfig()
		if c.AutoScanHours > 0 {
			sc := st.GetScan()
			due := sc == nil || time.Since(time.Unix(sc.TS, 0)) > time.Duration(c.AutoScanHours)*time.Hour
			if due {
				logf("auto-scan: 距上次扫描超过 %d 小时，开始静默扫描", c.AutoScanHours)
				startScanAsync(c, st)
			}
		}
		time.Sleep(time.Hour)
	}
}
