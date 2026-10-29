package main

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// testCleanDir 造一个分类上属于「可清」的目录。
//
// 老写法自拼 %LOCALAPPDATA%\Temp：那条路径不保证可写（受限环境里直接
// 「Access is denied」），用例红得和被测的保存流程毫无关系。现在位置交给
// t.TempDir()，不再猜 %TEMP% 在哪。
//
// 但规则护栏只放行「可清」类目录，t.TempDir() 可能落在仓库内（→ 判「用户文档」）。
// 所以挂一个 node_modules 段：分类规则里它是 dev_cache（可清），且排在 \documents\
// 之前，位置便不再影响判定。
func testCleanDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "node_modules", "diskmon-rules")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建临时目录失败：%v", err)
	}
	if why, ok := ruleGuard(Config{}, CleanRule{Path: dir}); !ok {
		t.Fatalf("测试前提不成立：%q 不算「可清」类目录（%s）", dir, why)
	}
	return dir
}

// 用一份临时配置替换全局 liveConfig，结束自动还原，避免污染同包其它用例。
func withRules(t *testing.T, rules []CleanRule) Config {
	t.Helper()
	cfg := Config{DataDir: t.TempDir(), Rules: rules}
	old := getConfig()
	setLiveConfig(cfg)
	t.Cleanup(func() { setLiveConfig(old) })
	return cfg
}

// guardConfigWrite 兜住「测试把配置真的写到磁盘上」这件事。
//
// saveConfig 固定写 <exeDir>/config.json；跑测试时 exeDir 取决于测试二进制放在哪
// （go test 放临时目录、go test -c 放指定路径；某些情况下目录就是包目录）。
// 无论哪一种，都不该在仓库里留下 config.json，更不该盖掉开发者自己的配置。
//
// 做法是不改产品代码：动手前把两个候选位置的原状记下来，测试结束按原状恢复——
// 原来没有就删掉，原来有就写回原字节。
func guardConfigWrite(t *testing.T) {
	t.Helper()
	type snap struct {
		path   string
		data   []byte
		exists bool
	}
	cands := []string{
		filepath.Join(exeDir(), "config.json"),
		"config.json", // cwd 就是包目录时的那个位置
	}
	snaps := make([]snap, 0, len(cands))
	for _, p := range cands {
		b, err := os.ReadFile(p)
		snaps = append(snaps, snap{path: p, data: b, exists: err == nil})
	}
	t.Cleanup(func() {
		for _, s := range snaps {
			if s.exists {
				_ = os.WriteFile(s.path, s.data, 0o644)
				continue
			}
			_ = os.Remove(s.path)
		}
	})
}

// 编辑已有规则必须能存进去。
//
// 这条链上真的断过一环：编辑页表单里没有 editing 字段，后端读到的永远是空串，
// 于是每次保存都被当成「新建」，一头撞进同名判据，只报「已存在同名规则」——
// 规则一个字都改不了。所以这里分两段验：表单要发，后端要认。
func TestRuleEditFormCarriesEditing(t *testing.T) {
	guardConfigWrite(t) // saveRuleFromValues 会真的写配置文件，先兜住
	dir := testCleanDir(t)
	const oldName = "旧规则"
	cfg := withRules(t, []CleanRule{{Name: oldName, Path: dir, MaxSizeMB: 2048, KeepDays: 7, IntervalMin: 60}})
	st := openStore(cfg)

	page := rulesPage(getConfig(), st, oldName)
	m := regexp.MustCompile(`name="editing"\s+value="([^"]*)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal(`编辑页表单里没有 name="editing"：后端拿不到「正在改哪一条」，保存必然失败`)
	}
	if m[1] != oldName {
		t.Fatalf("editing 应当是 %q，实际 %q", oldName, m[1])
	}

	// 把表单值原样交给后端：必须走修改分支。
	vals := url.Values{
		"name":         {oldName},
		"path":         {dir},
		"max_size_mb":  {"1024"},
		"keep_days":    {"3"},
		"interval_min": {"30"},
		"enabled":      {"1"},
		"dry_run":      {"1"},
		"editing":      {m[1]},
	}
	if err := saveRuleFromValues(vals, vals.Get("editing")); err != nil {
		t.Fatalf("编辑已有规则被拒：%v", err)
	}
	got := getConfig()
	if len(got.Rules) != 1 {
		t.Fatalf("应当原地修改，不该新增，实际 %d 条", len(got.Rules))
	}
	if r := got.Rules[0]; r.MaxSizeMB != 1024 || r.KeepDays != 3 || r.IntervalMin != 30 {
		t.Fatalf("修改没落到规则上：%+v", r)
	}
}

// 新建（表单不带 editing）撞上同名，仍然要拒绝——
// 修编辑不能把这条判据顺手弄松。
func TestRuleSaveWithoutEditingRejectsDuplicate(t *testing.T) {
	guardConfigWrite(t) // 同名判据也可能走到保存分支，同样要兜住
	dir := testCleanDir(t)
	const name = "同名规则"
	withRules(t, []CleanRule{{Name: name, Path: dir}})

	vals := url.Values{"name": {name}, "path": {dir}}
	err := saveRuleFromValues(vals, "")
	if err == nil {
		t.Fatal("新建同名规则应当被拒绝")
	}
	if !strings.Contains(err.Error(), "已存在同名规则") {
		t.Fatalf("应当报「已存在同名规则」，实际：%v", err)
	}
}

// 规则里的目录要支持 %VAR% 写法。
//
// 全程序别处（/inspect、MCP、软件残留）都认 %TEMP%，只有保存规则这处漏了，
// 于是「把 %LOCALAPPDATA%\Temp 粘进来」必然被判成"不存在"。
func TestRulePathAcceptsPercentVars(t *testing.T) {
	guardConfigWrite(t) // 保存成功会真的写配置文件
	dir := testCleanDir(t)
	t.Setenv("DISKMON_TEST_RULE_DIR", dir)
	withRules(t, nil)

	vals := url.Values{"name": {"百分号变量"}, "path": {`%DISKMON_TEST_RULE_DIR%`}}
	if err := saveRuleFromValues(vals, ""); err != nil {
		t.Fatalf("%%VAR%% 应当展开后保存，实际被拒：%v", err)
	}
	got := getConfig()
	if len(got.Rules) != 1 {
		t.Fatalf("应当存下一条规则，实际 %d 条", len(got.Rules))
	}
	if !strings.EqualFold(got.Rules[0].Path, dir) {
		t.Fatalf("落盘的路径应当展开成 %q，实际 %q", dir, got.Rules[0].Path)
	}
}

// 拒绝时要说清是哪一种原因——「不存在」和「是个文件」要能分辨，
// 因为用户接下来要做的事完全不同（改路径 vs 换目标）。
func TestRulePathRejectionsAreDistinguishable(t *testing.T) {
	guardConfigWrite(t)
	withRules(t, nil)

	missing := filepath.Join(t.TempDir(), "definitely-not-here")
	err := saveRuleFromValues(url.Values{"name": {"x"}, "path": {missing}}, "")
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("不存在的目录应当报「不存在」，实际：%v", err)
	}

	file := filepath.Join(t.TempDir(), "a-file.txt")
	if werr := os.WriteFile(file, []byte("x"), 0o644); werr != nil {
		t.Fatalf("准备文件失败：%v", werr)
	}
	err = saveRuleFromValues(url.Values{"name": {"y"}, "path": {file}}, "")
	if err == nil || !strings.Contains(err.Error(), "不是目录") {
		t.Fatalf("传一个文件应当报「不是目录」，实际：%v", err)
	}
}
