package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 直接测试隔离区底层机制（绕过分类守卫，守卫另有测试）
func TestQuarantineCycle(t *testing.T) {
	base, err := os.MkdirTemp(".", "qdtest-src")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	src := filepath.Join(base, "victim")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a.bin", "b.bin", filepath.Join("sub", "c.bin")} {
		if err := os.WriteFile(filepath.Join(src, n), make([]byte, 1024*1024), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	data, err := os.MkdirTemp(".", "qdtest-data")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(data)
	q := &Quarantine{root: filepath.Join(data, "quarantine"), index: filepath.Join(data, "quarantine", "index.jsonl")}
	if err := os.MkdirAll(q.root, 0o755); err != nil {
		t.Fatal(err)
	}

	// 1) 隔离
	e, err := q.apply(Config{Whitelist: nil}, src, "test")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("源目录应已移动，但仍然存在")
	}
	if e.Files != 3 || e.Bytes != 3*1024*1024 {
		t.Fatalf("统计不对：files=%d bytes=%d", e.Files, e.Bytes)
	}
	if _, err := os.Stat(e.Stored); err != nil {
		t.Fatalf("隔离文件不存在：%v", err)
	}

	// 2) 还原
	if _, err := q.restore(e.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, n := range []string{"a.bin", "b.bin", filepath.Join("sub", "c.bin")} {
		if _, err := os.Stat(filepath.Join(src, n)); err != nil {
			t.Fatalf("还原后缺少 %s: %v", n, err)
		}
	}

	// 3) 再隔离一次并清理超期项
	e2, err := q.apply(Config{Whitelist: nil}, src, "test2")
	if err != nil {
		t.Fatalf("apply2: %v", err)
	}
	n, bytes := q.purge(0)
	if n != 1 || bytes == 0 {
		t.Fatalf("清理数量不对：n=%d bytes=%d", n, bytes)
	}
	if _, err := os.Stat(e2.Stored); !os.IsNotExist(err) {
		t.Fatalf("清理后隔离副本应已删除")
	}
}

func TestGuardRefuses(t *testing.T) {
	cfg := Config{Whitelist: []string{`\documents\`}}
	cases := []string{`C:\Windows\System32`, `C:\Users\x\Documents\secret.txt`, `C:\`}
	for _, c := range cases {
		if _, _, refuse := guardPath(cfg, c); refuse == "" {
			t.Fatalf("%s 应该被拒绝", c)
		}
	}
	if _, tier, refuse := guardPath(Config{}, `C:\Users\x\AppData\Local\Temp\some-cache`); refuse != "" || tier != "clean" {
		t.Fatalf("temp 应允许：tier=%s refuse=%s", tier, refuse)
	}
}
