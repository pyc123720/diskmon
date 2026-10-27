package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLazyRunAndUndo(t *testing.T) {
	base, err := os.MkdirTemp(".", "lazy-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)

	mk := func(name string) string {
		d := filepath.Join(base, name)
		_ = os.MkdirAll(d, 0o755)
		_ = os.WriteFile(filepath.Join(d, "a.bin"), make([]byte, 1<<20), 0o644)
		return d
	}
	d1, d2 := mk("cache1"), mk("cache2")

	st := &Store{}
	st.st.Scan = &Scan{Deep: []DirEntry{
		{Path: d1, Bytes: 200 << 20, Cat: "temp"},
		{Path: d2, Bytes: 200 << 20, Cat: "temp"},
	}}
	cfg := Config{DataDir: base, QuarantineDays: 7, QuarantineMaxGB: 20}
	q := openQuarantine(cfg)

	if !startLazy(cfg, st, q) {
		t.Fatal("懒人清理应能启动")
	}
	// 等它跑完
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !lazyState().Running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	s := lazyState()
	if s.Running {
		t.Fatal("超时仍在运行")
	}
	if len(s.IDs) != 2 {
		t.Fatalf("应隔离 2 个目录，实际 %d（失败 %d）", len(s.IDs), s.Failed)
	}
	if _, err := os.Stat(d1); err == nil {
		t.Fatal("目录应已被移走")
	}

	n, freed := lazyUndo(cfg, q, st)
	if n != 2 || freed == 0 {
		t.Fatalf("撤销应还原 2 个目录，实际 %d / %d 字节", n, freed)
	}
	if _, err := os.Stat(d1); err != nil {
		t.Fatal("撤销后目录应已还原")
	}

	// 已无待撤销项时再撤销不应报错
	if n2, _ := lazyUndo(cfg, q, st); n2 != 0 {
		t.Fatalf("重复撤销应为 0，实际 %d", n2)
	}
}
