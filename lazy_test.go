package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	// 计划应该在开始时实测（每个目录真实 1 MB），而不是沿用扫描里虚报的 200 MB
	if s.Planned > 50<<20 {
		t.Fatalf("计划应是实测值，实际 %s", human(s.Planned))
	}
	if s.Freed == 0 || s.Freed > s.Planned+1<<20 {
		t.Fatalf("实际释放 %s 应接近实测计划 %s", human(s.Freed), human(s.Planned))
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

func TestLazyPauseResumeStop(t *testing.T) {
	lazyMu.Lock()
	lazyCur = lazyRun{Running: true}
	lazyMu.Unlock()
	defer func() {
		lazyMu.Lock()
		lazyCur = lazyRun{}
		lazyMu.Unlock()
	}()

	if !lazyPause() {
		t.Fatal("运行中应能暂停")
	}
	released := make(chan bool, 1)
	go func() { released <- lazyGate() }()
	select {
	case <-released:
		t.Fatal("暂停时闸门不应放行")
	case <-time.After(100 * time.Millisecond):
	}

	if !lazyResume() {
		t.Fatal("暂停中应能继续")
	}
	select {
	case ok := <-released:
		if !ok {
			t.Fatal("继续后闸门应放行")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("继续后闸门没有放行")
	}

	if !lazyStop() {
		t.Fatal("运行中应能停止")
	}
	if lazyGate() {
		t.Fatal("停止后闸门应返回 false")
	}
	if lazyPause() {
		t.Fatal("停止后不应还能暂停")
	}
}

func TestMoveFilesIndividuallyStop(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	dst := filepath.Join(base, "dst")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1024)
	for i := 0; i < 50; i++ {
		if err := os.WriteFile(filepath.Join(src, fmt.Sprintf("f%02d.bin", i)), buf, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	allowed := 0
	gate := func() bool { allowed++; return allowed <= 12 }
	moved, files, _, err := moveFilesIndividually(src, dst, nil, gate)
	if !errors.Is(err, errStopped) {
		t.Fatalf("应在中途停止，实际 err=%v", err)
	}
	if files == 0 || files >= 50 {
		t.Fatalf("应移动一部分文件，实际 %d 个（%d 字节）", files, moved)
	}
	left, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) == 0 || len(left) >= 50 {
		t.Fatalf("源目录应剩下未移动的文件，实际剩 %d 个", len(left))
	}
}

func TestLazyDeltaText(t *testing.T) {
	more := lazyDeltaText(lazyRun{Planned: 100 << 20, Freed: 130 << 20})
	if !strings.Contains(more, "多") {
		t.Fatalf("实际多于计划时应解释原因，得到：%s", more)
	}
	less := lazyDeltaText(lazyRun{Planned: 100 << 20, Freed: 80 << 20, SkippedFiles: 3})
	if !strings.Contains(less, "少") || !strings.Contains(less, "3") {
		t.Fatalf("实际少于计划时应说明跳过数，得到：%s", less)
	}
	stopped := lazyDeltaText(lazyRun{Planned: 100 << 20, Freed: 40 << 20, Stopped: true, Remaining: 2})
	if !strings.Contains(stopped, "手动停止") || strings.Contains(stopped, "被程序占用") {
		t.Fatalf("手动停止不应归咎于文件占用，得到：%s", stopped)
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
	moved, n, skipped, err := moveFilesIndividually(src, dst, func(m uint64, f, s int, cur string) { lastFiles = f }, nil)
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
