package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuarantineCap(t *testing.T) {
	base, err := os.MkdirTemp(".", "cap-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)

	qroot := filepath.Join(base, "quarantine")
	if err := os.MkdirAll(qroot, 0o755); err != nil {
		t.Fatal(err)
	}
	q := &Quarantine{root: qroot, index: filepath.Join(qroot, "index.jsonl")}

	// 伪造一条 1.5 GB 的隔离记录，把上限（1 GB）占满
	b, _ := json.Marshal(QEntry{ID: "fake", Original: `C:\x`, Stored: filepath.Join(qroot, "fake"), Bytes: 1536 << 20})
	if err := os.WriteFile(q.index, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(base, "victim")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "a.bin"), make([]byte, 4096), 0o644)

	cfg := Config{QuarantineMaxGB: 1}
	_, err = q.apply(cfg, src, "test")
	if err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("超过上限时应拒绝，实际：%v", err)
	}
	if _, e := os.Stat(src); e != nil {
		t.Fatalf("被拒绝后源目录应保持原样")
	}
}
