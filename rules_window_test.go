package main

import (
	"testing"
	"time"
)

func mkClock(hh, mm int) time.Time {
	return time.Date(2026, 10, 26, hh, mm, 0, 0, time.Local)
}

func TestInWindow(t *testing.T) {
	cases := []struct {
		now        time.Time
		start, end string
		want       bool
	}{
		{mkClock(3, 0), "02:00", "06:00", true},
		{mkClock(7, 0), "02:00", "06:00", false},
		{mkClock(23, 30), "22:00", "06:00", true},  // 跨零点
		{mkClock(5, 59), "22:00", "06:00", true},  // 跨零点
		{mkClock(6, 0), "22:00", "06:00", false},  // 右开区间
		{mkClock(12, 0), "", "", true},             // 空=全天
		{mkClock(12, 0), "bad", "06:00", true},     // 格式错误按全天处理
		{mkClock(12, 0), "12:00", "12:00", true},   // 起止相同=全天
	}
	for _, c := range cases {
		if got := inWindow(c.now, c.start, c.end); got != c.want {
			t.Fatalf("%s 在 [%s,%s) 期望 %v，实际 %v", c.now.Format("15:04"), c.start, c.end, c.want, got)
		}
	}
}
