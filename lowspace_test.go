package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// 测试用：清掉进程内缓存，模拟"刚启动"（真实重启由 lowspace.json 承担）。
func resetLowSpaceCacheForTest() {
	lowSpaceCache.mu.Lock()
	lowSpaceCache.valid = false
	lowSpaceCache.path = ""
	lowSpaceCache.mu.Unlock()
}

// 阈值档位用的测试配置：默认阈值，但数据目录落在临时目录里。
// Root 用 C:\ 是为了让状态里的 root 稳定；total 一律按 100 GB 算，
// 这样百分比档和 GB 档说的是一回事，断言不会因为两条判据打架而漂移。
func lowSpaceCfg(t *testing.T) Config {
	t.Helper()
	return Config{
		DataDir: t.TempDir(),
		Root:    `C:\`,
		Port:    8766,
		WarnGB:  20, DangerGB: 10, CriticalGB: 5,
		WarnPct: 15, DangerPct: 8, CriticalPct: 4,
	}
}

func gbBytes(f float64) uint64 { return uint64(f * float64(uint64(1)<<30)) }

func vol(freeGB float64) Volume {
	return Volume{Free: gbBytes(freeGB), Total: gbBytes(100)}
}

// 自动扫描桩：证明"至多一次"用。真实扫描在这里不能跑（会去遍历整盘）。
func stubLowSpaceScan(t *testing.T, started bool) *int32 {
	t.Helper()
	var n int32
	prev := lowSpaceScan
	lowSpaceScan = func(cfg Config, st *Store) bool {
		atomic.AddInt32(&n, 1)
		return started
	}
	t.Cleanup(func() { lowSpaceScan = prev })
	return &n
}

// 计数用 lowspace.json 里的 Notify，而不是告警流：
// AddAlert 自带同类节流（文案里数字会被抹成 #），拿它计数会把两次真实通知算成一次。
// 副作用是——这条断言测的正好是我们自己的防抖，不是告警节流。
func lowSpaceFires(cfg Config) int { return lowSpaceSnapshot(cfg).Notify }

func countLowSpaceAlerts(st *Store) int {
	n := 0
	for _, a := range st.Alerts() {
		if a.Kind == "lowspace" {
			n++
		}
	}
	return n
}

// 跌破一次只通知一次：同一档位持续低，不再刷屏。
func TestLowSpaceTripOncePerCrossing(t *testing.T) {
	resetLowSpaceCacheForTest()
	t.Cleanup(resetLowSpaceCacheForTest)

	cfg := lowSpaceCfg(t)
	scans := stubLowSpaceScan(t, true)
	st := openStore(cfg)

	// 先正常，再跌破警告线（20 GB）。
	observeSpace(cfg, st, vol(50))
	observeSpace(cfg, st, vol(19.9))
	if got := lowSpaceFires(cfg); got != 1 {
		t.Fatalf("跌破一次应只通知 1 次，实际 %d 次", got)
	}
	if got := atomic.LoadInt32(scans); got != 1 {
		t.Fatalf("跌破一次应只触发 1 次只读扫描，实际 %d", got)
	}

	// 继续在警告线下方抖动/停留：不再通知、不再扫描。
	for i := 0; i < 5; i++ {
		observeSpace(cfg, st, vol(19.5-float64(i)*0.5))
	}
	if got := lowSpaceFires(cfg); got != 1 {
		t.Fatalf("同一档位持续低不应再通知，实际 %d 次", got)
	}
	if got := atomic.LoadInt32(scans); got != 1 {
		t.Fatalf("同一档位不应重复扫描，实际 %d 次", got)
	}

	// 档位变差（跌破危险线）是一次新事件，应当通知。
	observeSpace(cfg, st, vol(9.9))
	if got := lowSpaceFires(cfg); got != 2 {
		t.Fatalf("从警告恶化到危险应再通知 1 次，实际 %d 次", got)
	}
	// 页面可见的留痕也要有：告警流里应当留下 lowspace 记录。
	if got := countLowSpaceAlerts(st); got < 1 {
		t.Fatalf("告警流里应有 lowspace 记录，实际 %d 条", got)
	}
}

// 阈值上下抖动不重复触发：刚过线回升一点、再跌破，不算"缓解过"。
func TestLowSpaceJitterDoesNotRepeat(t *testing.T) {
	resetLowSpaceCacheForTest()
	t.Cleanup(resetLowSpaceCacheForTest)

	cfg := lowSpaceCfg(t)
	scans := stubLowSpaceScan(t, true)
	st := openStore(cfg)

	observeSpace(cfg, st, vol(19.9))  // 跌破 → 通知
	observeSpace(cfg, st, vol(20.05)) // 只回升 0.15 GB：没过恢复带（20+2）
	observeSpace(cfg, st, vol(19.8))  // 又跌破
	observeSpace(cfg, st, vol(20.1))  // 再抖一次
	observeSpace(cfg, st, vol(19.95)) // 还跌破

	if got := lowSpaceFires(cfg); got != 1 {
		t.Fatalf("阈值附近抖动不应重复通知，实际 %d 次", got)
	}
	if got := atomic.LoadInt32(scans); got != 1 {
		t.Fatalf("抖动不应重复触发扫描，实际 %d 次", got)
	}
}

// 回升到恢复带以上（=真正的缓解）后再次跌破，要重新通知。
func TestLowSpaceRearmAfterRecovery(t *testing.T) {
	resetLowSpaceCacheForTest()
	t.Cleanup(resetLowSpaceCacheForTest)

	cfg := lowSpaceCfg(t)
	scans := stubLowSpaceScan(t, true)
	st := openStore(cfg)

	observeSpace(cfg, st, vol(19.9)) // 第一次跌破
	if got := lowSpaceFires(cfg); got != 1 {
		t.Fatalf("首次跌破应通知 1 次，实际 %d", got)
	}

	observeSpace(cfg, st, vol(25)) // 25 GB：越过 20+2 的恢复带
	if got := lowSpaceFires(cfg); got != 1 {
		t.Fatalf("回升不应产生新通知，实际 %d", got)
	}
	if lvl := lowSpaceSnapshot(cfg).Level; lvl != "ok" {
		t.Fatalf("越过恢复带后应复位成 ok，实际 %q", lvl)
	}

	observeSpace(cfg, st, vol(18)) // 再次跌破
	if got := lowSpaceFires(cfg); got != 2 {
		t.Fatalf("回升后再次跌破应重新通知，实际 %d 次", got)
	}
	if got := atomic.LoadInt32(scans); got != 2 {
		t.Fatalf("新的一次跌破应再扫描一次，实际 %d 次", got)
	}
}

// 防抖状态落盘：进程重启（只保留 lowspace.json）后，同一档位不再重复通知。
func TestLowSpacePersistAcrossRestart(t *testing.T) {
	resetLowSpaceCacheForTest()
	t.Cleanup(resetLowSpaceCacheForTest)

	cfg := lowSpaceCfg(t)
	stubLowSpaceScan(t, true)
	st := openStore(cfg)

	observeSpace(cfg, st, vol(19.9))
	path := lowSpaceStatePath(cfg)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("防抖状态应落盘到 %s：%v", path, err)
	}
	if !strings.Contains(string(b), `"level": "warn"`) {
		t.Fatalf("落盘内容里应记录已通知的档位：%s", string(b))
	}

	// 模拟重启：内存缓存清空，状态只从磁盘恢复。
	resetLowSpaceCacheForTest()
	st2 := openStore(cfg)
	observeSpace(cfg, st2, vol(19.7))
	if got := lowSpaceFires(cfg); got != 1 {
		t.Fatalf("重启后同一档位不应重复通知（Notify 应仍是 1），实际 %d", got)
	}

	// 重启后如果先回升过恢复带、再跌破，仍应通知（重启不该冻结住防抖）。
	resetLowSpaceCacheForTest()
	observeSpace(cfg, st2, vol(30))
	if got := lowSpaceFires(cfg); got != 1 {
		t.Fatalf("回升本身不该产生通知，实际 %d", got)
	}
	resetLowSpaceCacheForTest()
	observeSpace(cfg, st2, vol(19))
	if got := lowSpaceFires(cfg); got != 2 {
		t.Fatalf("回升后再跌破应通知（Notify 应变成 2），实际 %d", got)
	}
}

// 手动扫描正在跑时不该叠加 I/O：仍然通知、仍然留痕，但这次不扫描、也不重试。
func TestLowSpaceScanBusyStillNotifiesOnce(t *testing.T) {
	resetLowSpaceCacheForTest()
	t.Cleanup(resetLowSpaceCacheForTest)

	cfg := lowSpaceCfg(t)
	scans := stubLowSpaceScan(t, false) // 模拟 TryLock 失败
	st := openStore(cfg)

	observeSpace(cfg, st, vol(19.9))
	observeSpace(cfg, st, vol(19.6))
	observeSpace(cfg, st, vol(19.2))

	if got := lowSpaceFires(cfg); got != 1 {
		t.Fatalf("扫描被占用时也要通知（只一次），实际 %d 次", got)
	}
	if got := atomic.LoadInt32(scans); got != 1 {
		t.Fatalf("扫描尝试也只该一次（不每轮重试），实际 %d", got)
	}
}

// 通知内容要能立刻做决定：剩余、百分比、档位、阈值、入口。
func TestLowSpaceMessageActionable(t *testing.T) {
	cfg := lowSpaceCfg(t)
	msg := lowSpaceMessage(cfg, gbBytes(4.6), gbBytes(100))

	for _, want := range []string{`C:\`, "剩余", "4.6%", "紧急", "阈值 5 GB / 4%", "/clean", "8766"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("通知内容缺少 %q：%s", want, msg)
		}
	}
}

// 「自动行为只有通知 + 至多一次只读扫描」：这一层跑一遍，磁盘上的文件一个都不能少。
func TestLowSpaceObserveChangesNoFiles(t *testing.T) {
	resetLowSpaceCacheForTest()
	t.Cleanup(resetLowSpaceCacheForTest)

	dir := t.TempDir()
	files := []string{"a.bin", "b.bin", filepath.Join("sub", "c.bin")}
	for _, f := range files {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, 4096), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before, err := filepath.Glob(filepath.Join(dir, "**"))
	if err != nil {
		t.Fatal(err)
	}

	cfg := lowSpaceCfg(t)
	cfg.Root = dir
	cfg.WarnGB = 100000 // 让任何余量都算"跌破"，逼出通知与扫描分支
	stubLowSpaceScan(t, true)
	st := openStore(cfg)
	observeSpace(cfg, st, Volume{Free: 1 << 20, Total: 1 << 40})

	after, err := filepath.Glob(filepath.Join(dir, "**"))
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("observeSpace 不允许改动文件：之前 %d 项，之后 %d 项", len(before), len(after))
	}
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("文件被动了：%s（%v）", f, err)
		}
	}
}
