package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func mkChild(t *testing.T, root, name string, subs ...string) string {
	t.Helper()
	child := filepath.Join(root, name)
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, s := range subs {
		if err := os.MkdirAll(filepath.Join(child, s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return child + string(os.PathSeparator)
}

// 有多个子目录的子树要拆开，每个任务记住自己的 parent。
func TestBuildJobsSplitsSubdirs(t *testing.T) {
	root := t.TempDir()
	child := mkChild(t, root, "Big", "a", "b", "c")

	jobs := buildJobs(child)
	if len(jobs) != 3 {
		t.Fatalf("任务数=%d，期望 3", len(jobs))
	}
	names := make([]string, 0, len(jobs))
	for _, j := range jobs {
		if j.parent != child {
			t.Fatalf("parent=%q，期望 %q", j.parent, child)
		}
		names = append(names, filepath.Base(filepath.Clean(j.path)))
	}
	sort.Strings(names)
	want := []string{"a", "b", "c"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("任务=%v，期望 %v", names, want)
		}
	}
}

// 子目录不足 2 个时不该拆——拆了反而多一次合并开销。
func TestBuildJobsDegradesToSelf(t *testing.T) {
	root := t.TempDir()
	solo := mkChild(t, root, "Solo")
	jobs := buildJobs(solo)
	if len(jobs) != 1 || jobs[0].path != solo || jobs[0].parent != solo {
		t.Fatalf("退化失败：%+v", jobs)
	}

	one := mkChild(t, root, "OneChild", "only")
	jobs = buildJobs(one)
	if len(jobs) != 1 || jobs[0].path != one {
		t.Fatalf("单子目录也应退化成自己：%+v", jobs)
	}
}

// 归并要把子树的目录数补回 1（子树自身不再作为扫描根出现）。
func TestMergeChildPartsAddsParentDir(t *testing.T) {
	child := `C:\Demo\Big\`
	a := &Scan{Root: `C:\Demo\Big\a\`, Total: 100, Dirs: 5, Files: 2}
	b := &Scan{Root: `C:\Demo\Big\b\`, Total: 50, Dirs: 3, Files: 1}

	m := mergeChildParts(child, []*Scan{a, b})
	if m.Total != 150 {
		t.Fatalf("Total=%d，期望 150", m.Total)
	}
	if m.Dirs != 9 { // 5+3 + 子树自身
		t.Fatalf("Dirs=%d，期望 9（子分片之和 + 子树自身）", m.Dirs)
	}
	if m.Files != 3 {
		t.Fatalf("Files=%d，期望 3", m.Files)
	}
	if m.Root != child {
		t.Fatalf("Root=%q，期望 %q（缓存键靠它）", m.Root, child)
	}
}

// 没拆开的情况必须原样返回，不能重复补 Dirs。
func TestMergeChildPartsPassesThroughSingle(t *testing.T) {
	child := `C:\Demo\Solo\`
	s := &Scan{Root: child, Total: 10, Dirs: 1}
	got := mergeChildParts(child, []*Scan{s})
	if got != s {
		t.Fatal("未拆开的子树应当原样返回同一个指针")
	}
	if got.Dirs != 1 {
		t.Fatalf("Dirs 被多补了一次：%d", got.Dirs)
	}
}

// 合并后 Unique 必须按合并后的总量重算，恒等式不能破。
func TestMergeChildPartsKeepsIdentity(t *testing.T) {
	child := `C:\Demo\X\`
	a := &Scan{Root: `C:\Demo\X\a\`, Total: 100, Shared: 30}
	b := &Scan{Root: `C:\Demo\X\b\`, Total: 50, Shared: 20}
	m := mergeChildParts(child, []*Scan{a, b})
	if m.Unique+m.Shared != m.Total {
		t.Fatalf("恒等式被打破：%d + %d != %d", m.Unique, m.Shared, m.Total)
	}
	if m.Shared != 50 || m.Unique != 100 {
		t.Fatalf("Shared=%d Unique=%d，期望 50/100", m.Shared, m.Unique)
	}
}
