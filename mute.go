package main

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// ---------- 静音与噪声过滤 ----------

var muteUntil int64 // unix 秒；0=不静音；-1=直到重启

func muted() bool {
	v := atomic.LoadInt64(&muteUntil)
	if v == 0 {
		return false
	}
	if v < 0 {
		return true
	}
	return time.Now().Unix() < v
}

func setMute(d time.Duration) { atomic.StoreInt64(&muteUntil, time.Now().Add(d).Unix()) }
func muteForever()            { atomic.StoreInt64(&muteUntil, -1) }
func clearMute()              { atomic.StoreInt64(&muteUntil, 0) }

func muteStatusText() string {
	v := atomic.LoadInt64(&muteUntil)
	if v == 0 {
		return ""
	}
	if v < 0 {
		return "通知已静音（直到重启）"
	}
	return "通知已静音至 " + time.Unix(v, 0).Format("15:04")
}

// Windows 自身写入的目录：对用户没有意义，不弹窗，只记录
var systemNoisePatterns = []string{
	`\windows\system32\sru`,
	`\windows\system32\winevt`,
	`\windows\system32\config`,
	`\windows\system32\logfiles`,
	`\windows\security\logs`,
	`\windows\system32\wbem\repository`,
	`\system volume information`,
	`\registry`,
}

func isSystemNoisePath(p string) bool {
	low := strings.ToLower(p)
	for _, pat := range systemNoisePatterns {
		if strings.Contains(low, pat) {
			return true
		}
	}
	return false
}

// 如果一个窗口里的增长全部来自系统目录，就当作噪声
func isSystemNoiseDirs(dirs map[string]uint64) bool {
	if len(dirs) == 0 {
		return false
	}
	for d := range dirs {
		if !isSystemNoisePath(d) {
			return false
		}
	}
	return true
}

// 是否值得弹窗：低于通知阈值 或 全是系统噪声 → 不弹（仍会记录到告警）
func shouldNotifyGrowth(cfg Config, total uint64, dirs map[string]uint64) bool {
	if total < uint64(cfg.NotifyMinMB)<<20 {
		return false
	}
	if isSystemNoiseDirs(dirs) {
		return false
	}
	return true
}

func muteBanner() string {
	t := muteStatusText()
	if t == "" {
		return ""
	}
	return `<div class="warn" style="margin-bottom:14px">🔕 ` + htmlEscape(t) + `：告警仍会记录在看板，只是不弹通知。<a href="/mute?mode=off">取消静音</a> · <a href="/mute?mode=1h">再静音 1 小时</a></div>`
}

var _ = fmt.Sprintf
