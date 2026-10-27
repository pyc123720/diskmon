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


func TestDedupeNested(t *testing.T) {
	dirs := []DirEntry{
		{Path: `C:\Users\x\AppData\Local\Temp`, Bytes: 100},
		{Path: `C:\Users\x\AppData\Local\Temp\nested`, Bytes: 50},
		{Path: `C:\Users\x\AppData\Local\Temp2`, Bytes: 30},
	}
	got := dedupeNested(dirs)
	if len(got) != 2 {
		t.Fatalf("应去掉被父目录覆盖的子目录，实际 %d 个：%+v", len(got), got)
	}
	for _, d := range got {
		if d.Path == `C:\Users\x\AppData\Local\Temp\nested` {
			t.Fatal("子目录不应保留")
		}
	}
}

func TestMoveFilesIndividually(t *testing.T) {
	base, err := os.MkdirTemp(".", "move-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	src := filepath.Join(base, "src")
	dst := filepath.Join(base, "dst")
	for _, d := range []string{"a", "b/c", "locked"} {
		if err := os.MkdirAll(filepath.Join(src, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]int{"a/1.bin": 100, "b/c/2.bin": 200, "top.bin": 50}
	for rel, size := range files {
		if err := os.WriteFile(filepath.Join(src, rel), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var lastFiles int
	moved, n, skipped, err := moveFilesIndividually(src, dst, func(m uint64, f, s int, cur string) { lastFiles = f })
	if err != nil {
		t.Fatalf("移动失败: %v", err)
	}
	if n != 3 || skipped != 0 || moved != 350 {
		t.Fatalf("移动统计不对: moved=%d files=%d skipped=%d", moved, n, skipped)
	}
	if lastFiles == 0 {
		t.Fatal("进度回调没有被调用")
	}
	if _, err := os.Stat(filepath.Join(dst, "b", "c", "2.bin")); err != nil {
		t.Fatal("子目录结构应保留")
	}

	// 合并还原：原目录已存在时应能放回去
	if err := os.MkdirAll(filepath.Join(src, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	rfiles, rbytes, rskip, rerr := restoreInto(dst, src)
	if rerr != nil || rfiles == 0 || rbytes == 0 {
		t.Fatalf("合并还原失败: files=%d bytes=%d skip=%d err=%v", rfiles, rbytes, rskip, rerr)
	}
	if _, err := os.Stat(filepath.Join(src, "top.bin")); err != nil {
		t.Fatal("文件应还原到原目录")
	}
}
