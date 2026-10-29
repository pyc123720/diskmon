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
// 规则护栏只放行临时文件 / 缓存类目录，所以测试目录必须落在真实的用户 Temp 下——
// 随便在项目里建个目录会被 ruleGuard 拒掉，测的就不是保存流程了。
func testCleanDir(t *testing.T) string {
	t.Helper()
	base := filepath.Join(os.Getenv("LOCALAPPDATA"), "Temp")
	if os.Getenv("LOCALAPPDATA") == "" {
		base = os.TempDir()
	}
	dir, err := os.MkdirTemp(base, "diskmon-rules-")
	if err != nil {
		t.Fatalf("建临时目录失败：%v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
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

// 编辑已有规则必须能存进去。
//
// 这条链上真的断过一环：编辑页表单里没有 editing 字段，后端读到的永远是空串，
// 于是每次保存都被当成「新建」，一头撞进同名判据，只报「已存在同名规则」——
// 规则一个字都改不了。所以这里分两段验：表单要发，后端要认。
func TestRuleEditFormCarriesEditing(t *testing.T) {
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
		"name":        {oldName},
		"path":        {dir},
		"max_size_mb": {"1024"},
		"keep_days":   {"3"},
		"interval_min": {"30"},
		"enabled":     {"1"},
		"dry_run":     {"1"},
		"editing":     {m[1]},
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
