package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTopChild(t *testing.T) {
	if got := topChild(`C:\`, `C:\Users\lenovo\AppData\x.txt`); got != `C:\Users\` {
		t.Fatalf("topChild=%q", got)
	}
	if got := topChild(`C:\`, `C:\Windows\System32`); got != `C:\Windows\` {
		t.Fatalf("topChild=%q", got)
	}
	if got := topChild(`C:\`, `D:\x`); got != "" {
		t.Fatalf("跨盘应为空，实际 %q", got)
	}
}

func TestCanReuseChild(t *testing.T) {
	now := time.Now()
	cache := &scanCache{
		Root: `C:\`, TS: now.Unix(), FullTS: now.Unix(), Session: procSession,
		Children: map[string]*Scan{`c:\windows\`: {Total: 1}, `c:\users\`: {Total: 2}},
	}
	if !canReuseChild(cache, `C:\Windows\`, map[string]bool{}, now) {
		t.Fatal("无脏标记且非空应复用")
	}
	if canReuseChild(cache, `C:\Windows\`, map[string]bool{`c:\windows\`: true}, now) {
		t.Fatal("脏子树不应复用")
	}
	if canReuseChild(cache, `C:\ProgramData\`, map[string]bool{}, now) {
		t.Fatal("缓存里没有的子树不应复用")
	}
	// 超过 24 小时不可复用
	old := *cache
	old.TS = now.Add(-25 * time.Hour).Unix()
	if canReuseChild(&old, `C:\Windows\`, map[string]bool{}, now) {
		t.Fatal("过期缓存不应复用")
	}
}

func TestDirtyAndSession(t *testing.T) {
	clearDirty()
	markDirty(`C:\`, `C:\Users\x\file.txt`, false)
	dirty, all := dirtySnapshot()
	if all || !dirty[`c:\users\`] {
		t.Fatalf("应标记 C:\\Users\\，实际 %v all=%v", dirty, all)
	}
	markDirty(`C:\`, `C:\Users\a`, true)
	if _, all2 := dirtySnapshot(); !all2 {
		t.Fatal("重命名应让全部失效")
	}
	clearDirty()

	// 缓存会话不匹配 → 不可复用
	now := time.Now()
	c := &scanCache{Root: `C:\`, TS: now.Unix(), FullTS: now.Unix(), Session: "other-session",
		Children: map[string]*Scan{`c:\windows\`: {}}}
	if c.Session == procSession {
		t.Fatal("测试前提：会话应不同")
	}
}

func TestCacheRoundtrip(t *testing.T) {
	dir, _ := os.MkdirTemp(".", "cache-test")
	defer os.RemoveAll(dir)
	cfg := Config{DataDir: dir}
	c := &scanCache{Root: `C:\`, TS: 1, FullTS: 2, Session: "s",
		Children: map[string]*Scan{`c:\windows\`: {Total: 42}}}
	saveScanCache(cfg, c)
	got := loadScanCache(cfg)
	if got == nil || got.Children[`c:\windows\`].Total != 42 {
		t.Fatalf("缓存读写失败: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "scan_cache.json")); err != nil {
		t.Fatal("缓存文件不存在")
	}
}

// setScanFlags 临时改全局开关，用例结束自动还原，避免污染同包其它用例。
func setScanFlags(t *testing.T, mft, usn bool) {
	t.Helper()
	oldMft, oldUsn := useMftEngine, usnActive.Load()
	useMftEngine = mft
	usnActive.Store(usn)
	t.Cleanup(func() {
		useMftEngine = oldMft
		usnActive.Store(oldUsn)
	})
}

// MFT 成功后必须留下缓存——这是「管理员第二次扫描不再重读整张 $MFT」的前提。
func TestMftScanWritesCache(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), Root: `C:\`}
	saveMftScanCache(cfg, &Scan{TS: time.Now().Unix(), Root: `C:\`, Files: 1234, Dirs: 56, Total: 789, Source: "mft"})

	got := loadScanCache(cfg)
	if got == nil || got.Mft == nil {
		t.Fatalf("MFT 成功后应写下缓存，实际 %+v", got)
	}
	if got.Session != procSession {
		t.Fatalf("缓存会话应为当前进程，实际 %q", got.Session)
	}
	if !strings.EqualFold(got.Root, cfg.Root) {
		t.Fatalf("缓存根应为 %q，实际 %q", cfg.Root, got.Root)
	}
	if got.Mft.Files != 1234 || got.Mft.Total != 789 || got.Mft.Dirs != 56 {
		t.Fatalf("缓存内容不对：%+v", got.Mft)
	}
	if len(got.Children) != 0 {
		t.Fatalf("MFT 快照不该带子树缓存，实际 %d 个", len(got.Children))
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "scan_cache.json")); err != nil {
		t.Fatal("缓存文件不存在")
	}

	// 失败/回落时不写半成品：没有结果就一个字都不落盘。
	failCfg := Config{DataDir: t.TempDir(), Root: `C:\`}
	saveMftScanCache(failCfg, nil)
	if _, err := os.Stat(filepath.Join(failCfg.DataDir, "scan_cache.json")); err == nil {
		t.Fatal("没有扫描结果时不该写缓存文件")
	}
}

// 只有 MFT 快照的缓存不能被当成"遍历缓存可复用"——否则会退化成逐子树全扫。
func TestWalkCacheUsableIgnoresMftOnlyCache(t *testing.T) {
	setScanFlags(t, true, true)
	now := time.Now()
	mftOnly := &scanCache{Root: `C:\`, TS: now.Unix(), FullTS: now.Unix(), Session: procSession,
		Mft: &Scan{Files: 10}}
	if walkCacheUsable(mftOnly, `C:\`, false, now) {
		t.Fatal("只有 MFT 快照的缓存不能算遍历缓存可用")
	}
	if c := cachedMftScan(mftOnly, `C:\`, false, nil, false, now); c == nil || c.Files != 10 {
		t.Fatal("MFT 快照本身应当可复用")
	}
	walk := &scanCache{Root: `C:\`, TS: now.Unix(), FullTS: now.Unix(), Session: procSession,
		Children: map[string]*Scan{`c:\windows\`: {}}}
	if !walkCacheUsable(walk, `C:\`, false, now) {
		t.Fatal("有子树的缓存应当可复用")
	}
	if c := cachedMftScan(walk, `C:\`, false, nil, false, now); c != nil {
		t.Fatal("没有 MFT 快照时不该返回结果")
	}
}

// 复用的前提必须和遍历缓存一样严：会话、根、时效、开关、脏集合，缺一不可。
func TestCachedMftScanInvalidation(t *testing.T) {
	setScanFlags(t, true, true)
	now := time.Now()
	base := func() *scanCache {
		return &scanCache{Root: `C:\`, TS: now.Unix(), FullTS: now.Unix(), Session: procSession,
			Mft: &Scan{Files: 10, Total: 100}}
	}
	if c := cachedMftScan(base(), `C:\`, false, map[string]bool{}, false, now); c == nil {
		t.Fatal("整卷无变更时应复用 MFT 快照")
	}
	if c := cachedMftScan(base(), `C:\`, false, map[string]bool{`c:\users\`: true}, false, now); c != nil {
		t.Fatal("有脏子树时不应复用（整卷快照拆不出子树）")
	}
	if c := cachedMftScan(base(), `C:\`, false, nil, true, now); c != nil {
		t.Fatal("重命名导致全部失效时不应复用")
	}
	if c := cachedMftScan(base(), `C:\`, true, nil, false, now); c != nil {
		t.Fatal("强制全扫时不应复用")
	}
	useMftEngine = false
	if c := cachedMftScan(base(), `C:\`, false, nil, false, now); c != nil {
		t.Fatal("--walk / --no-mft 时不应复用 MFT 快照")
	}
	useMftEngine = true
	other := base()
	other.Session = "other-session"
	if c := cachedMftScan(other, `C:\`, false, nil, false, now); c != nil {
		t.Fatal("跨进程缓存不应复用")
	}
	old := base()
	old.TS = now.Add(-7 * time.Hour).Unix()
	if c := cachedMftScan(old, `C:\`, false, nil, false, now); c != nil {
		t.Fatal("扫描时间超过 6 小时不应复用")
	}
	oldFull := base()
	oldFull.FullTS = now.Add(-25 * time.Hour).Unix()
	if c := cachedMftScan(oldFull, `C:\`, false, nil, false, now); c != nil {
		t.Fatal("对账基准超过 24 小时不应复用")
	}
	wrongRoot := base()
	if c := cachedMftScan(wrongRoot, `D:\`, false, nil, false, now); c != nil {
		t.Fatal("根不一致时不应复用")
	}
}
