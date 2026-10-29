package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 用真实的 NTFS 硬链接验证：两个路径、同一份数据、同一身份。
func TestHardlinkShareIdentity(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(src, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "b.bin")
	if err := os.Link(src, dst); err != nil {
		t.Skipf("此文件系统不支持硬链接，跳过: %v", err)
	}

	k1, l1, ok1 := fileIdentity(src)
	k2, l2, ok2 := fileIdentity(dst)
	if !ok1 || !ok2 {
		t.Fatalf("取不到文件身份：%v %v", ok1, ok2)
	}
	if k1 != k2 {
		t.Fatalf("硬链接应共享同一身份：%+v vs %+v", k1, k2)
	}
	if l1 < 2 || l2 < 2 {
		t.Fatalf("硬链接数应 >=2，得到 %d / %d", l1, l2)
	}
}

// 第一个名字算独占，第二个名字才算重复——这是「只扣一次」的关键。
func TestHardlinkTrackerCountsSecondNameOnly(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(src, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "b.bin")
	if err := os.Link(src, dst); err != nil {
		t.Skipf("此文件系统不支持硬链接，跳过: %v", err)
	}

	const size = uint64(1 << 20)
	tr := newHardlinkTracker()
	var shared uint64
	if tr.observe(src, size) {
		t.Fatal("第一个名字不该被算成重复")
	}
	if !tr.observe(dst, size) {
		t.Fatal("第二个名字应被算成重复计入")
	}
	shared += size // 调用方负责记账，判重只返回 bool
	if shared != size {
		t.Fatalf("shared=%d，期望 %d", shared, size)
	}
}

// 普通单链接文件永远不该被扣。
func TestHardlinkTrackerIgnoresSingleLink(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "solo.bin")
	if err := os.WriteFile(p, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := newHardlinkTracker()
	if tr.observe(p, 1024) {
		t.Fatal("单链接文件不该被算成重复")
	}
}

// 身份表是共享的：换一个调用点再遇到同一个文件，也必须判成重复。
// 这条正是「拆并行分片后跨分片硬链接要查得出来」的单元级保险。
func TestHardlinkTrackerSharedAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(src, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "b.bin")
	if err := os.Link(src, dst); err != nil {
		t.Skipf("此文件系统不支持硬链接，跳过: %v", err)
	}

	restore := beginHardlinkScope()
	defer restore()

	// 模拟两个不同的扫描分片各自调用
	if activeHardlinks().observe(src, 4096) {
		t.Fatal("第一份不该重复")
	}
	if !activeHardlinks().observe(dst, 4096) {
		t.Fatal("跨调用点也必须判出重复——这就是并行拆片后 2.37 GB 漏账的根因")
	}
}

// 探测失败（路径不存在）必须安静地返回 false，不能影响扫描。
func TestHardlinkProbeMissingPathIsSilent(t *testing.T) {
	tr := newHardlinkTracker()
	if tr.observe(filepath.Join(t.TempDir(), "nope.bin"), 999) {
		t.Fatal("不存在的文件不该被判为重复")
	}
	if _, _, ok := fileIdentity(filepath.Join(t.TempDir(), "nope.bin")); ok {
		t.Fatal("不存在的文件不该取到身份")
	}
}

// Unique + Shared == Total 这条恒等式必须永远成立，含边界情况。
func TestUniqueBytesIdentity(t *testing.T) {
	cases := []struct{ total, shared, want uint64 }{
		{100, 0, 100},
		{100, 30, 70},
		{100, 100, 0},
		{100, 130, 0}, // shared 超过 total 时不许下溢成天文数字
		{0, 0, 0},
	}
	for _, c := range cases {
		if got := uniqueBytes(c.total, c.shared); got != c.want {
			t.Fatalf("uniqueBytes(%d,%d)=%d，期望 %d", c.total, c.shared, got, c.want)
		}
	}
}

// 分类层的「共享」账必须和全局 Shared 对上：每份重复只归到一个分类，不能多算也不能漏。
// 这是「分类数字不再骗人」的核心不变量。
func TestScanVolumeAttributesSharedToCategories(t *testing.T) {
	dir := t.TempDir()
	dataDir := t.TempDir()
	const size = 1 << 20

	sub := filepath.Join(dir, "cache")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(sub, "a.bin")
	if err := os.WriteFile(a, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(a, filepath.Join(sub, "b.bin")); err != nil {
		t.Skipf("此文件系统不支持硬链接，跳过: %v", err)
	}

	old := hardlinkProbeMin
	hardlinkProbeMin = 1
	defer func() { hardlinkProbeMin = old }()

	sc := scanVolume(Config{Root: dir, DataDir: dataDir}, dir)

	var sum uint64
	for _, v := range sc.CatsShared {
		sum += v
	}
	if sum != sc.Shared {
		t.Fatalf("分类 Shared 合计=%d，全局 Shared=%d，两者必须严格相等", sum, sc.Shared)
	}
	if sc.Shared != size {
		t.Fatalf("Shared=%d，期望 %d", sc.Shared, size)
	}
	if len(sc.CatsShared) == 0 {
		t.Fatal("应当至少有一个分类被记上共享字节")
	}
	// Cats 仍然是路径求和，不能因为去重而变小
	var catSum uint64
	for _, v := range sc.Cats {
		catSum += v
	}
	if catSum != sc.Total {
		t.Fatalf("Cats 合计=%d 与 Total=%d 不等（Cats 必须与 Total 同口径）", catSum, sc.Total)
	}
}

// 走完整扫描路径的集成测试：造一棵含硬链接的小树，验证 Total / Shared / Unique 三个数。
// 单元测试只能证明「判重逻辑对」，这个能证明「全盘扫出来的数对」。
func TestScanVolumeAccountsHardlinks(t *testing.T) {
	dir := t.TempDir()
	dataDir := t.TempDir()
	const size = 1 << 20

	if err := os.WriteFile(filepath.Join(dir, "solo.bin"), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(sub, "a.bin")
	if err := os.WriteFile(a, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(a, filepath.Join(sub, "b.bin")); err != nil {
		t.Skipf("此文件系统不支持硬链接，跳过: %v", err)
	}

	// 把探测阈值降到 1 字节，让测试文件落在探测范围内
	old := hardlinkProbeMin
	hardlinkProbeMin = 1
	defer func() { hardlinkProbeMin = old }()

	cfg := Config{Root: dir, DataDir: dataDir}
	sc := scanVolume(cfg, dir)

	wantTotal := uint64(3 * size) // solo + a + b：按路径求和，硬链接本来就该算两次
	if sc.Total != wantTotal {
		t.Fatalf("Total=%d，期望 %d（Total 是逻辑体积，不该被去重改变）", sc.Total, wantTotal)
	}
	if sc.Shared != size {
		t.Fatalf("Shared=%d，期望 %d（只该扣掉第二个名字）", sc.Shared, size)
	}
	if sc.Unique != wantTotal-size {
		t.Fatalf("Unique=%d，期望 %d", sc.Unique, wantTotal-size)
	}
	if sc.Unique+sc.Shared != sc.Total {
		t.Fatalf("恒等式被打破：Unique(%d)+Shared(%d) != Total(%d)", sc.Unique, sc.Shared, sc.Total)
	}
}

func TestMergeScansRecomputesUnique(t *testing.T) {
	a := &Scan{Total: 100, Shared: 30}
	b := &Scan{Total: 50, Shared: 20}
	m := mergeScans(`C:\demo`, []*Scan{a, b})
	if m.Total != 150 {
		t.Fatalf("Total=%d，期望 150", m.Total)
	}
	if m.Shared != 50 {
		t.Fatalf("Shared=%d，期望 50", m.Shared)
	}
	if m.Unique != 100 {
		t.Fatalf("Unique=%d，期望 100", m.Unique)
	}
}
