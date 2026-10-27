package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQuarantineRebuild(t *testing.T) {
	base, err := os.MkdirTemp(".", "rebuild-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)

	cfg := Config{DataDir: base, QuarantineDays: 7, QuarantineMaxGB: 20}
	q := openQuarantine(cfg)
	src := filepath.Join(base, "victim")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "a.bin"), make([]byte, 2048), 0o644)

	e, err := q.apply(cfg, src, "test")
	if err != nil {
		t.Fatal(err)
	}
	// 模拟索引损坏：清空 index.jsonl
	_ = os.WriteFile(q.index, []byte("{\"id\":\"broken\""), 0o644)

	rec, _, _ := q.rebuild()
	if rec < 1 {
		t.Fatalf("应至少恢复 1 条，实际 %d", rec)
	}
	found := false
	for _, x := range q.entries() {
		if x.ID == e.ID && x.Original == src {
			found = true
		}
	}
	if !found {
		t.Fatal("重建后应能按原路径找到条目")
	}
	if _, err := q.restore(e.ID); err != nil {
		t.Fatalf("重建后应可还原：%v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("还原后源目录应恢复")
	}
}

func TestAlertThrottle(t *testing.T) {
	base, err := os.MkdirTemp(".", "alert-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)

	old := alertThrottleWindow
	alertThrottleWindow = 5 * time.Minute
	defer func() { alertThrottleWindow = old }()

	st := openStore(Config{DataDir: base})

	if !st.AddAlert("growth", "增长 100 MB，来源 C:\\a") {
		t.Fatal("第一条应被记录")
	}
	// 数字不同但形态一致 → 应被节流
	if st.AddAlert("growth", "增长 234 MB，来源 C:\\a") {
		t.Fatal("同形态的重复告警应被节流")
	}
	// 不同目录 → 应放行
	if !st.AddAlert("growth", "增长 100 MB，来源 C:\\b") {
		t.Fatal("不同目标的告警应放行")
	}
	// 被节流后应有合并摘要
	joined := ""
	for _, a := range st.Alerts() {
		joined += a.Kind + ":" + a.Text + "\n"
	}
	if !strings.Contains(joined, "digest") {
		t.Fatalf("应写入合并摘要，实际：%s", joined)
	}
}
