package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// winapp2TestCleanDir 造一个可以参与测试的目录。
//
// 三道闸里，「越界」这一道用接缝放行：本机上根本不存在「写得了 + 不算越界」的位置
// （Users\Documents 被规则正确拦掉，AppData\Local 与各盘根目录又都建不了目录）。
// 放行的只有这一个判断，规则本身由 TestWinapp2UnsafePathCoversUserData 直接钉住。
// 「档位」这一道仍走真实的 guardPath：node_modules 会被判 dev_cache / clean。
func winapp2TestCleanDir(t *testing.T) string {
	t.Helper()
	old := winapp2PathAllowed
	winapp2PathAllowed = func(string) bool { return true }
	t.Cleanup(func() { winapp2PathAllowed = old })

	root := filepath.Join(t.TempDir(), "node_modules")
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatalf("建测试目录失败：%v", err)
	}
	return root
}

// fileExists 只用来判断「这个卷在不在」。
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// 越界判断是纯函数，单独钉一遍：它是「规则说了不算」的第一道闸。
func TestWinapp2UnsafePathCoversUserData(t *testing.T) {
	cases := map[string]bool{
		`C:\Windows\System32`:                      true,
		`C:\Program Files\App`:                     true,
		`C:\Users\someone\Documents`:               true,
		`C:\Users\someone\Desktop`:                 true,
		`C:\Users\someone\AppData\Roaming\App`:     true,
		`C:\`:                                      true,
		`relative\path`:                            true,
		`C:\Users\someone\AppData\Local\App\Cache`: false,
		`C:\Users\someone\AppData\LocalLow\App`:    false,
		`C:\Windows\Temp`:                          false,
		`C:\Windows\SoftwareDistribution\Download`: false,
		`C:\ProgramData\Microsoft\Windows\WER`:     false,
	}
	for p, want := range cases {
		if got := winapp2UnsafePath(p); got != want {
			t.Errorf("winapp2UnsafePath(%q) = %v，期望 %v", p, got, want)
		}
	}
}

// 规则里的路径要过两道：越界检查、以及 guardPath 的档位检查。
// 这条测试钉住的是「规则怎么写都不算数，得清理器自己点头」。
func TestWinapp2ScanOnlyAcceptsCleanAndSafePaths(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), QuarantineDays: 7, QuarantineMaxGB: 20}
	cleanRoot := winapp2TestCleanDir(t)
	// 非递归规则只扫一层，所以顶层要有一个真的 .tmp——否则命中 0 个文件，
	// 分组就正确地不出现（这是我第一版数据的错，不是代码的错）。
	if err := os.WriteFile(filepath.Join(cleanRoot, "a.tmp"), []byte("1234"), 0o644); err != nil {
		t.Fatalf("写测试文件失败：%v", err)
	}

	ini := "[社区缓存]\n" +
		"FileKey1=" + cleanRoot + "|*.tmp|\n" +
		"[用户文档]\nFileKey1=%USERPROFILE%\\Documents|\n" +
		"[系统目录]\nFileKey1=C:\\Windows\\System32|\n"
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "winapp2.ini"), []byte(ini), 0o644); err != nil {
		t.Fatalf("写规则文件失败：%v", err)
	}

	groups, _ := winapp2Scan(cfg)
	byName := map[string]Winapp2Group{}
	for _, g := range groups {
		byName[g.Name] = g
	}

	// 1. clean 档且不越界 → 进候选
	cache, ok := byName["社区缓存"]
	if !ok {
		t.Fatalf("「社区缓存」应当有候选项，实际分组：%v", byName)
	}
	if len(cache.Paths) != 1 || !strings.EqualFold(cache.Paths[0], cleanRoot) {
		t.Fatalf("「社区缓存」的候选路径不对：%v", cache.Paths)
	}

	// 2. 文档目录 → 不算候选，但原因要留下
	docs, ok := byName["用户文档"]
	if !ok {
		t.Fatalf("「用户文档」应当被列出来（带拒绝原因），而不是消失")
	}
	if len(docs.Paths) != 0 {
		t.Fatalf("用户文档不该进入可处理候选：%v", docs.Paths)
	}
	if len(docs.Skipped) == 0 {
		t.Fatalf("被拒绝的条目必须留下原因，否则用户以为规则没生效")
	}

	// 3. System32 → 越界，不采纳
	sys, ok := byName["系统目录"]
	if !ok {
		t.Fatalf("「系统目录」应当被列出来并说明原因")
	}
	if len(sys.Paths) != 0 {
		t.Fatalf("System32 绝不能进入候选：%v", sys.Paths)
	}
}

// 点下去之后走的是隔离区，不是删除：文件得能还原。
func TestWinapp2ApplyGoesThroughQuarantine(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), QuarantineDays: 7, QuarantineMaxGB: 20}
	cleanRoot := winapp2TestCleanDir(t)
	payload := filepath.Join(cleanRoot, "pkg", "dead.tmp")
	if err := os.WriteFile(payload, []byte("hello"), 0o644); err != nil {
		t.Fatalf("写测试文件失败：%v", err)
	}
	ini := "[社区缓存]\nFileKey1=" + cleanRoot + "|*.tmp|RECURSE\n"
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "winapp2.ini"), []byte(ini), 0o644); err != nil {
		t.Fatalf("写规则文件失败：%v", err)
	}

	res, err := winapp2Apply(cfg, "社区缓存")
	if err != nil {
		t.Fatalf("隔离失败：%v", err)
	}
	if res.Moved == 0 {
		t.Fatalf("应当有路径被移进隔离区，拒绝原因：%v", res.Refused)
	}
	if _, statErr := os.Stat(cleanRoot); !os.IsNotExist(statErr) {
		t.Fatalf("原路径应当已经不在了，Stat 结果：%v", statErr)
	}

	// 隔离区里要有记录，而且能还原——这正是它跟「删了就没了」的区别
	q := openQuarantine(cfg)
	var id string
	for _, e := range q.entries() {
		if !e.Restored && strings.EqualFold(e.Original, cleanRoot) {
			id = e.ID
		}
	}
	if id == "" {
		t.Fatalf("隔离区里找不到这条记录，条目数：%d", len(q.entries()))
	}
	if _, rerr := q.restore(id); rerr != nil {
		t.Fatalf("还原失败：%v", rerr)
	}
	if _, statErr := os.Stat(filepath.Join(cleanRoot, "pkg", "dead.tmp")); statErr != nil {
		t.Fatalf("还原之后文件应当回来了：%v", statErr)
	}
}

// 没有规则文件时不能报错、不能凭空造出卡片内容。
func TestWinapp2ScanWithoutFileIsQuiet(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	groups, warnings := winapp2Scan(cfg)
	if len(groups) != 0 || len(warnings) != 0 {
		t.Fatalf("没有规则文件时应当什么都不产出，得到 %d 组 / %d 警告", len(groups), len(warnings))
	}
	if html := winapp2CardHTML(cfg); !strings.Contains(html, "还没有读到规则文件") {
		t.Fatalf("卡片应当提示规则文件缺失，实际：%s", html)
	}
}

// ExcludeKey 是「这些别动」。不支持排除就不能照 FileKey 动手，
// 否则规则特意留下的文件会被一起挪走——整段不采纳，并说明原因。
func TestWinapp2SkipsEntriesWithExcludes(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), QuarantineDays: 7, QuarantineMaxGB: 20}
	cleanRoot := winapp2TestCleanDir(t)
	if err := os.WriteFile(filepath.Join(cleanRoot, "keep-me.tmp"), []byte("1234"), 0o644); err != nil {
		t.Fatalf("写测试文件失败：%v", err)
	}

	ini := "[带排除的缓存]\n" +
		"FileKey1=" + cleanRoot + "|*.tmp|\n" +
		"ExcludeKey1=FILE|" + cleanRoot + "|keep-me.tmp|\n"
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "winapp2.ini"), []byte(ini), 0o644); err != nil {
		t.Fatalf("写规则文件失败：%v", err)
	}

	groups, _ := winapp2Scan(cfg)
	var g Winapp2Group
	found := false
	for _, cand := range groups {
		if cand.Name == "带排除的缓存" {
			g, found = cand, true
		}
	}
	if !found {
		t.Fatalf("这一段应当被列出来说明原因，而不是消失")
	}
	if len(g.Paths) != 0 {
		t.Fatalf("声明了 ExcludeKey 的段落不能进候选，实际：%v", g.Paths)
	}
	if len(g.Skipped) == 0 || !strings.Contains(g.Skipped[0], "ExcludeKey") {
		t.Fatalf("拒绝原因里要写明 ExcludeKey，实际：%v", g.Skipped)
	}

	// 整段不采纳，所以那个本该被排除的文件必须还在原地
	if _, err := os.Stat(filepath.Join(cleanRoot, "keep-me.tmp")); err != nil {
		t.Fatalf("规则想留下的文件被动了：%v", err)
	}

	// 点不下去：即使直接调 apply 也要失败，不能留下绕过路径
	if _, err := winapp2Apply(cfg, "带排除的缓存"); err == nil {
		t.Fatalf("没有可处理路径的规则不该能执行")
	}
}
