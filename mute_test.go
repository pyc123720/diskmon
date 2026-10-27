package main

import "testing"

func TestSystemNoise(t *testing.T) {
	if !isSystemNoiseDirs(map[string]uint64{`C:\Windows\System32\sru`: 1}) {
		t.Fatal("sru 应算系统噪声")
	}
	if !isSystemNoiseDirs(map[string]uint64{`C:\Windows\System32\config`: 1, `C:\Windows\System32\winevt\Logs`: 2}) {
		t.Fatal("config+winevt 应算系统噪声")
	}
	if isSystemNoiseDirs(map[string]uint64{`C:\Windows\System32\sru`: 1, `C:\Users\x\AppData\Local\Temp`: 2}) {
		t.Fatal("混有用户目录时不应算噪声")
	}
	if isSystemNoiseDirs(nil) {
		t.Fatal("空集合不是噪声")
	}
}

func TestShouldNotifyGrowth(t *testing.T) {
	cfg := Config{NotifyMinMB: 500}
	small := map[string]uint64{`C:\Users\x\AppData\Local\Temp`: 100 << 20}
	if shouldNotifyGrowth(cfg, 100<<20, small) {
		t.Fatal("低于阈值不应弹窗")
	}
	big := map[string]uint64{`C:\Users\x\AppData\Local\Temp`: 600 << 20}
	if !shouldNotifyGrowth(cfg, 600<<20, big) {
		t.Fatal("超过阈值且非噪声应弹窗")
	}
	noise := map[string]uint64{`C:\Windows\System32\winevt\Logs`: 600 << 20}
	if shouldNotifyGrowth(cfg, 600<<20, noise) {
		t.Fatal("系统噪声不应弹窗")
	}
}

func TestMuteState(t *testing.T) {
	clearMute()
	if muted() || muteStatusText() != "" {
		t.Fatal("初始应为未静音")
	}
	setMute(3600_000_000_000) // 1 小时
	if !muted() || muteStatusText() == "" {
		t.Fatal("设置后应为静音")
	}
	clearMute()
	if muted() {
		t.Fatal("取消后应恢复")
	}
	muteForever()
	if !muted() {
		t.Fatal("forever 应静音")
	}
	clearMute()
}
