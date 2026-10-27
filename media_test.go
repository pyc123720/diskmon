package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMediaBatchAndUndo(t *testing.T) {
	base, err := os.MkdirTemp(".", "media-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)

	dir := filepath.Join(base, "files")
	_ = os.MkdirAll(dir, 0o755)

	old := time.Now().AddDate(0, 0, -40)
	newer := time.Now().AddDate(0, 0, -2)
	mk := func(name string, when time.Time) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, make([]byte, 4096), 0o644)
		_ = os.Chtimes(p, when, when)
		return p
	}
	oldJpg := mk("old.jpg", old)
	newJpg := mk("new.jpg", newer)
	doc := mk("old.docx", old)

	cfg := Config{DataDir: base, QuarantineDays: 7, QuarantineMaxGB: 20}
	q := openQuarantine(cfg)
	st := &Store{}

	id := startMediaClean(cfg, st, q, dir, "image", 30)
	if id == "" {
		t.Fatal("应能启动媒体清理")
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !mediaBatchState(id).Running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	b := mediaBatchState(id)
	if b.Running {
		t.Fatal("超时仍在运行")
	}
	if b.Total != 1 {
		t.Fatalf("只应命中 1 个 30 天前的图片，实际 %d", b.Total)
	}
	if _, err := os.Stat(oldJpg); err == nil {
		t.Fatal("旧图片应已被隔离")
	}
	if _, err := os.Stat(newJpg); err != nil {
		t.Fatal("新图片不应被动")
	}
	if _, err := os.Stat(doc); err != nil {
		t.Fatal("文档不应被动")
	}

	n, _ := undoMediaBatch(q, st, id)
	if n != 1 {
		t.Fatalf("撤销应还原 1 个文件，实际 %d", n)
	}
	if _, err := os.Stat(oldJpg); err != nil {
		t.Fatal("撤销后旧图片应已还原")
	}
}
