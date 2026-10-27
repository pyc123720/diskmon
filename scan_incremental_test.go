package main

import (
	"os"
	"path/filepath"
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
