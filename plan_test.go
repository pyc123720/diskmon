package main

import (
	"os"
	"path/filepath"
	"testing"
)

func mkScan(dirs ...DirEntry) *Scan { return &Scan{Deep: dirs} }

func TestBuildPlanTiers(t *testing.T) {
	root := t.TempDir()
	mk := func(parts ...string) string {
		p := root
		for _, s := range parts {
			p = filepath.Join(p, s)
		}
		_ = os.MkdirAll(p, 0o755)
		return p
	}
	st := &Store{}
	st.st.Scan = mkScan(
		DirEntry{Path: mk("Temp", "a"), Bytes: 500 << 20, Cat: "temp"},                    // clean
		DirEntry{Path: mk("Users", "x", "AppData", "Local", "Chrome", "Cache"), Bytes: 400 << 20, Cat: "browser"}, // confirm
		DirEntry{Path: mk("Users", "x", "AppData", "Roaming", "Tencent"), Bytes: 900 << 20, Cat: "comms_media"}, // confirm
		DirEntry{Path: mk("Windows", "WinSxS"), Bytes: 900 << 20, Cat: "system_winsxs"},   // adjust：永不允许
		DirEntry{Path: mk("Small", "tmp"), Bytes: 10 << 20, Cat: "temp"},                  // 太小
	)
	cfg := Config{Whitelist: nil, QuarantineDays: 7, QuarantineMaxGB: 20}

	con := buildPlan(cfg, st, "conservative")
	if len(con.Dirs) != 1 || con.Dirs[0].Cat != "temp" {
		t.Fatalf("保守方案只应包含 clean/temp，实际 %d 个", len(con.Dirs))
	}

	std := buildPlan(cfg, st, "standard")
	for _, d := range std.Dirs {
		if d.Cat == "comms_media" {
			t.Fatalf("标准方案不应包含聊天媒体")
		}
		if d.Cat == "system_winsxs" {
			t.Fatalf("任何方案都不应包含系统可调整项")
		}
	}
	if len(std.Dirs) != 2 {
		t.Fatalf("标准方案应包含 temp+browser，实际 %d 个", len(std.Dirs))
	}

	agg := buildPlan(cfg, st, "aggressive")
	foundComms := false
	for _, d := range agg.Dirs {
		if d.Cat == "comms_media" {
			foundComms = true
		}
		if d.Cat == "system_winsxs" {
			t.Fatalf("激进方案也不应包含系统可调整项")
		}
	}
	if !foundComms {
		t.Fatalf("激进方案应包含聊天媒体")
	}
}

func TestPlanWhitelist(t *testing.T) {
	st := &Store{}
	st.st.Scan = mkScan(
		DirEntry{Path: `C:\Users\x\Documents\cache`, Bytes: 500 << 20, Cat: "temp"},
	)
	cfg := Config{Whitelist: []string{`\documents\`}, QuarantineDays: 7, QuarantineMaxGB: 20}
	p := buildPlan(cfg, st, "conservative")
	if len(p.Dirs) != 0 {
		t.Fatalf("白名单目录不应进入方案，实际 %d", len(p.Dirs))
	}
}
