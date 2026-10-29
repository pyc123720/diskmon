package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 测试用：把批次状态清干净。生产代码不提供这个入口——只有测试需要「假装刚启动」。
func resetRestoreLatchForTest() {
	restoreRun.mu.Lock()
	restoreRun.depth = 0
	restoreRun.subject = ""
	restoreRun.store = nil
	restoreRun.attempted = false
	restoreRun.outcome = RestorePointOutcome{}
	restoreRun.lastTry = time.Time{}
	restoreRun.mu.Unlock()
}

// restoreStub 把底层 syscall 换成可控结果，并记录调用次数与描述。
// 这是唯一允许「伪造返回值」的地方：测试覆盖的是我们自己的状态机与文案，
// 不是 SRSetRestorePoint 本身。
type restoreStub struct {
	calls    int32
	lastDesc string
	mu       sync.Mutex
}

func stubRestorePoint(t *testing.T, raw restorePointRaw) *restoreStub {
	t.Helper()
	resetRestoreLatchForTest()
	st := &restoreStub{}
	prev := attemptRestorePointFn
	attemptRestorePointFn = func(desc string) restorePointRaw {
		atomic.AddInt32(&st.calls, 1)
		st.mu.Lock()
		st.lastDesc = desc
		st.mu.Unlock()
		return raw
	}
	t.Cleanup(func() {
		attemptRestorePointFn = prev
		resetRestoreLatchForTest()
	})
	return st
}

func (s *restoreStub) count() int { return int(atomic.LoadInt32(&s.calls)) }

// tempVictim 造一个可以被隔离的目录（含一个小文件），并返回它。
//
// 位置不能再赌「临时目录在哪」：这些用例测的是还原点触发逻辑，不该顺带考分类器。
// 老写法是 os.MkdirTemp(os.TempDir(), ...)——只要 TEMP 落进仓库内（仓库在 Documents
// 下），分类器就判成「用户文档」并拒绝，5 个用例整片变红，而那不是还原点的 bug。
//
// 现在改成 t.TempDir() 下挂一个 node_modules 段：分类规则里 \node_modules 是
// dev_cache（可清），而且它排在 \documents\ 之前，所以无论临时目录落在哪里，
// 判出来的都是「可清」。前提由构造保证，不再靠环境赏脸。
func tempVictim(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "node_modules", name)
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.bin"), make([]byte, 64*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if why, ok := ruleGuard(Config{}, CleanRule{Path: src}); !ok {
		t.Fatalf("测试前提不成立：%q 不算「可清」类目录（%s）", src, why)
	}
	return src
}

func newTestQuarantine(t *testing.T) *Quarantine {
	t.Helper()
	// 隔离区根不走分类器，跟临时目录位置无关；用 t.TempDir() 只是为了自动清理。
	data := t.TempDir()
	q := &Quarantine{root: filepath.Join(data, "quarantine"), index: filepath.Join(data, "quarantine", "index.jsonl")}
	if err := os.MkdirAll(q.root, 0o755); err != nil {
		t.Fatal(err)
	}
	return q
}

// 路径一：成功。还原点落成了，就要如实说落成了，且描述里能认出是本工具。
func TestRestorePointCreatedReported(t *testing.T) {
	st := stubRestorePoint(t, restorePointRaw{OK: true, Seq: 4242})
	src := tempVictim(t, "victim-ok")
	q := newTestQuarantine(t)

	e, err := q.apply(Config{Whitelist: nil}, src, "test")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("清理应当照常完成（还原点落成时不能被它拖住）")
	}
	if e.Files != 1 {
		t.Fatalf("隔离项统计不对：files=%d", e.Files)
	}
	if st.count() != 1 {
		t.Fatalf("应当只落一次点，实际 %d 次", st.count())
	}

	out := restorePointSnapshot()
	if !out.HasPoint() || out.State != RestorePointCreated {
		t.Fatalf("状态应为 created，实际 %q", out.State)
	}
	if out.Seq != 4242 {
		t.Fatalf("序号应透传：%d", out.Seq)
	}
	// 还原点描述：用户日后要在「系统还原」里认出这一条。
	for _, want := range []string{"diskmon-go", "清理前", "victim-ok"} {
		if !strings.Contains(out.Desc, want) {
			t.Fatalf("描述应包含 %q，实际：%s", want, out.Desc)
		}
	}
	if !strings.Contains(st.lastDesc, "diskmon-go") {
		t.Fatalf("传给系统的描述也要带工具名，实际：%s", st.lastDesc)
	}
	if !strings.Contains(out.Msg, "已创建系统还原点") {
		t.Fatalf("成功文案不对：%s", out.Msg)
	}
	html := restorePointHTML(out)
	if !strings.Contains(html, "已创建系统还原点") || strings.Contains(html, "本次没有系统级回滚点") {
		t.Fatalf("成功时不该出现「没有回滚点」：%s", html)
	}
}

// 路径二：被策略禁用（系统保护关掉 / 域策略）。清理必须继续，但结论必须说出来。
func TestRestorePointDisabledStillCleans(t *testing.T) {
	stubRestorePoint(t, restorePointRaw{Code: errServiceDisabled})
	src := tempVictim(t, "victim-disabled")
	q := newTestQuarantine(t)

	if _, err := q.apply(Config{Whitelist: nil}, src, "test"); err != nil {
		t.Fatalf("还原点被禁用不应阻断清理：%v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("还原点被禁用时，清理仍然要完成")
	}
	out := restorePointSnapshot()
	if out.State != RestorePointDisabled {
		t.Fatalf("状态应为 disabled，实际 %q", out.State)
	}
	if !out.Missing() {
		t.Fatal("被禁用属于「没落成」，Missing() 应为 true")
	}
	if !strings.Contains(out.Msg, "本次没有系统级回滚点") {
		t.Fatalf("必须写明没有系统级回滚点：%s", out.Msg)
	}
	if !strings.Contains(out.Msg, "1058") {
		t.Fatalf("要带上错误码，方便用户查：%s", out.Msg)
	}
	html := restorePointHTML(out)
	if !strings.Contains(html, "本次没有系统级回滚点") || !strings.Contains(html, "只剩隔离区这一层") {
		t.Fatalf("结果页要写清只剩隔离区一层：%s", html)
	}
}

// 路径三：无权限（非管理员，最常遇到的一种）。表现同上，但原因要分清。
func TestRestorePointDeniedNonAdmin(t *testing.T) {
	stubRestorePoint(t, restorePointRaw{Code: errAccessDenied})
	src := tempVictim(t, "victim-denied")
	q := newTestQuarantine(t)

	if _, err := q.apply(Config{Whitelist: nil}, src, "test"); err != nil {
		t.Fatalf("无权限不应阻断清理：%v", err)
	}
	out := restorePointSnapshot()
	if out.State != RestorePointDenied {
		t.Fatalf("状态应为 denied，实际 %q", out.State)
	}
	if !strings.Contains(out.Msg, "本次没有系统级回滚点") || !strings.Contains(out.Msg, "管理员") {
		t.Fatalf("无权限的文案要写清原因与出路：%s", out.Msg)
	}
	if !strings.Contains(restorePointHTML(out), "本次没有系统级回滚点") {
		t.Fatal("页面必须可见")
	}
}

// 一次清理只落一次：同一批里连续隔离多个目录，只允许一次落点。
func TestRestorePointOncePerCleanupRun(t *testing.T) {
	st := stubRestorePoint(t, restorePointRaw{OK: true, Seq: 7})
	q := newTestQuarantine(t)

	end := beginCleanupRun(nil, "一键清理「保守」：3 个目录")
	for i := 0; i < 3; i++ {
		src := tempVictim(t, "batch-"+string(rune('a'+i)))
		if _, err := q.apply(Config{Whitelist: nil}, src, "test"); err != nil {
			t.Fatalf("apply %d: %v", i, err)
		}
	}
	end()
	if st.count() != 1 {
		t.Fatalf("同一批 3 个目录应只落 1 次点，实际 %d 次", st.count())
	}
	if got := restorePointSnapshot().Desc; !strings.Contains(got, "3 个目录") {
		t.Fatalf("描述应取批次主题，实际：%s", got)
	}

	// 下一次清理是新的批次：要重新落点，不能被上一批的结论永久压住。
	end2 := beginCleanupRun(nil, "一键清理「标准」：1 个目录")
	if _, err := q.apply(Config{Whitelist: nil}, tempVictim(t, "second-run"), "test"); err != nil {
		t.Fatalf("apply second run: %v", err)
	}
	end2()
	if st.count() != 2 {
		t.Fatalf("第二次清理应再落一次点，实际总数 %d", st.count())
	}
}

// 嵌套批次（自动规则一轮里逐条执行）算同一批，不该每条规则落一次。
func TestRestorePointNestedRunsShareOnePoint(t *testing.T) {
	st := stubRestorePoint(t, restorePointRaw{OK: true, Seq: 9})
	outer := beginCleanupRun(nil, "自动清理一轮：2 条规则")
	inner := beginCleanupRun(nil, "规则「A」")
	ensureRestorePoint("")
	inner2 := beginCleanupRun(nil, "规则「B」")
	ensureRestorePoint("")
	inner2()
	inner()
	outer()
	if st.count() != 1 {
		t.Fatalf("嵌套批次只该落 1 次点，实际 %d 次", st.count())
	}
}

// dry-run 绝不落点：这是「只在真删前落点」的硬约束。
func TestRestorePointDryRunNeverLands(t *testing.T) {
	st := stubRestorePoint(t, restorePointRaw{OK: true, Seq: 11})

	// 同上：位置不依赖 %TEMP%。node_modules 段让分类稳定落在「开发缓存 / 可清」，
	// 否则 TEMP 一变位置，规则守卫就把这条用例跳过——那测的就不是 dry-run 了。
	dir := filepath.Join(t.TempDir(), "node_modules", "dry")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if why, ok := ruleGuard(Config{}, CleanRule{Path: dir}); !ok {
		t.Fatalf("测试前提不成立：%q 不算「可清」类目录（%s）", dir, why)
	}
	old := time.Now().AddDate(0, 0, -30)
	for _, n := range []string{"x.tmp", "y.tmp", "z.tmp"} {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, make([]byte, 2<<20), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	rule := CleanRule{Name: "dry", Path: dir, MaxSizeMB: 1, KeepDays: 1, Enabled: true, DryRun: true}

	res := runRule(Config{}, nil, rule, true)
	if !res.DryRun {
		t.Fatalf("应当处于试运行：%+v", res)
	}
	if res.Skipped {
		t.Fatalf("这条规则不该被跳过（测的是试运行分支）：%s", res.Reason)
	}
	if res.WouldFree == 0 {
		t.Fatal("试运行应算出可释放量")
	}
	if res.Removed != 0 {
		t.Fatalf("试运行不许删文件，实际删了 %d 个", res.Removed)
	}
	if st.count() != 0 {
		t.Fatalf("试运行不许落还原点，实际落了 %d 次", st.count())
	}
	if res.Restore.State != RestorePointNone {
		t.Fatalf("试运行的 Restore 应为空，实际 %q", res.Restore.State)
	}
	if got := restorePointHTML(res.Restore); got != "" {
		t.Fatalf("没尝试过就不要在页面上渲染回滚点提示，实际：%s", got)
	}

	// 同一条规则改成真删：这时才该落点，且文件真的被删。
	rule.DryRun = false
	res2 := runRule(Config{}, nil, rule, false)
	if res2.Removed == 0 {
		t.Fatalf("真删模式应当删掉文件：%+v", res2)
	}
	if st.count() != 1 {
		t.Fatalf("真删前应落 1 次点，实际 %d 次", st.count())
	}
	if !res2.Restore.HasPoint() {
		t.Fatalf("真删的 Restore 应为 created，实际 %q", res2.Restore.State)
	}
}

// 结果页要能载出「本次没有系统级回滚点」——op 结果页是用户最常看到的那一页。
func TestOpPageShowsMissingRestorePoint(t *testing.T) {
	opMu.Lock()
	prev := opCur
	opCur = opState{
		Running: false, Kind: "quarantine", Label: "一键清理 · 保守",
		Total: 2, Done: 2, Bytes: 3 << 20, Files: 12,
		IDs: []string{"20260101-000000-abcd"}, Started: time.Now(),
		Restore: classifyRestorePoint(restorePointRaw{Code: errServiceDisabled}),
	}
	opMu.Unlock()
	t.Cleanup(func() {
		opMu.Lock()
		opCur = prev
		opMu.Unlock()
	})

	html := opPage(Config{}, nil)
	if !strings.Contains(html, "本次没有系统级回滚点") {
		t.Fatalf("op 结果页必须写明没有系统级回滚点：\n%s", html)
	}
	if !strings.Contains(html, "只剩隔离区这一层") {
		t.Fatal("op 结果页要写清只剩隔离区一层")
	}
}

// 非管理员下的真实行为：不在测试里伪造，而是把分类逻辑钉死——
// 系统保护关闭时 Windows 常报 1058/1062/1815，非管理员报 5。
func TestClassifyRestorePointCodes(t *testing.T) {
	cases := []struct {
		raw   restorePointRaw
		state RestorePointState
	}{
		{restorePointRaw{Code: errServiceDisabled}, RestorePointDisabled},
		{restorePointRaw{Code: errServiceNotActive}, RestorePointDisabled},
		{restorePointRaw{Code: errResourceDisabled}, RestorePointDisabled},
		{restorePointRaw{Code: errAccessDenied}, RestorePointDenied},
		{restorePointRaw{Unsupported: true, Code: errProcNotFound}, RestorePointUnsupported},
		{restorePointRaw{Code: 1450}, RestorePointFailed},
	}
	for _, c := range cases {
		out := classifyRestorePoint(c.raw)
		if out.State != c.state {
			t.Fatalf("code=%d 应归为 %q，实际 %q", c.raw.Code, c.state, out.State)
		}
		if !out.Missing() {
			t.Fatalf("code=%d 属于没落成，Missing() 应为 true", c.raw.Code)
		}
		if !strings.Contains(out.Msg, "本次没有系统级回滚点") {
			t.Fatalf("code=%d 的文案必须点明没落成：%s", c.raw.Code, out.Msg)
		}
	}
}

// 探针回归：这个导出在真正能导出它的 DLL 里。踩过的坑——
// 最早绑的是 powrprof.dll，实测返回 127（找不到导出），于是功能被误判成
// 「系统不支持」，等于永远不落点却还显示得体面。这条测试钉住候选顺序。
func TestSRSetRestorePointResolves(t *testing.T) {
	if srDLLCandidates[0] != "sfc.dll" {
		t.Fatalf("首个候选必须是 sfc.dll（实测 sfc.dll 才有这个导出），实际 %v", srDLLCandidates)
	}
	if p := srSetRestorePointProc(); p == nil {
		t.Fatal("本机应当能解析到 SRSetRestorePointW；解析不到说明候选 DLL 全错了")
	}
}

// 描述要截断在 MAX_DESC 以内，且不把中文切成半个。
func TestRestorePointDescClipped(t *testing.T) {
	long := strings.Repeat("很长的清理目标", 200)
	desc := restorePointDesc(long)
	if r := []rune(desc); len(r) > srMaxDesc-1 {
		t.Fatalf("描述超长：%d 个 rune", len(r))
	}
	if !strings.HasPrefix(desc, "diskmon-go") {
		t.Fatalf("描述开头应带工具名：%s", desc)
	}
}
