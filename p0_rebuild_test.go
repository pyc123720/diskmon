package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRebuildKeepsLegacyEntry(t *testing.T) {
	base, err := os.MkdirTemp(".", "rebuild-legacy")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	cfg := Config{DataDir: base, QuarantineDays: 7, QuarantineMaxGB: 20}
	q := openQuarantine(cfg)
	src := filepath.Join(base, "victim2")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "b.bin"), make([]byte, 1024), 0o644)
	e, err := q.apply(cfg, src, "test")
	if err != nil {
		t.Fatal(err)
	}
	// 模拟旧版本条目：删掉侧车元数据，只留索引
	_ = os.Remove(filepath.Join(q.root, e.ID, metaFileName))

	rec, _, _ := q.rebuild()
	if rec != 0 {
		t.Fatalf("没有侧车时不应恢复任何条目，实际 %d", rec)
	}
	found := false
	for _, x := range q.entries() {
		if x.ID == e.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("rebuild 不能丢掉索引里已有的条目")
	}
}
