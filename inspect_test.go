package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRehearseQuick(t *testing.T) {
	base, err := os.MkdirTemp(".", "inspect-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)

	d := filepath.Join(base, "cache")
	_ = os.MkdirAll(d, 0o755)
	for i := 0; i < 5; i++ {
		_ = os.WriteFile(filepath.Join(d, fmt.Sprintf("f%d.bin", i)), make([]byte, 1024), 0o644)
	}

	cfg := Config{Whitelist: []string{`\.ssh`}}
	r := rehearseQuick(cfg, d, 3*time.Second)
	if r.Refuse != "" {
		t.Fatalf("不应拒绝：%s", r.Refuse)
	}
	if r.Files != 5 || r.Bytes != 5*1024 {
		t.Fatalf("应统计 5 个文件 / 5120 字节，实际 %d / %d", r.Files, r.Bytes)
	}
	if !r.Truncated {
		// 时间没超预算时 Truncated 应为 false
		t.Log("not truncated (ok)")
	}

	// 白名单路径应被拒绝
	wal := filepath.Join(base, ".ssh")
	_ = os.MkdirAll(wal, 0o755)
	r2 := rehearseQuick(cfg, wal, time.Second)
	if r2.Refuse == "" {
		t.Fatal("白名单目录应被拒绝")
	}
}
