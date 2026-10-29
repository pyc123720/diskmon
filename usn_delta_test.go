package main

import (
	"fmt"
	"strings"
	"testing"
)

const (
	testRoot = `C:\`
	testData = `c:\diskmon-data`
)

// 7 条回落条件逐条钉住。每条都断言「不走增量」+「理由说得出来」。
func TestClassifyDeltaRecFallbacks(t *testing.T) {
	base := usnDeltaRec{
		Path: `C:\Users\a\big.bin`, Reason: reasonDataExtend, InSnapshot: true, OldSize: 100, NewSize: 200,
	}
	cases := []struct {
		name   string
		mutate func(r *usnDeltaRec)
		want   string
	}{
		{"①记录指向快照里没有的文件（写）", func(r *usnDeltaRec) { r.InSnapshot = false }, "不在快照里"},
		{"①新增但路径不在扫描根内", func(r *usnDeltaRec) { r.Reason = reasonFileCreate; r.InSnapshot = false; r.Path = `D:\x\a.bin` }, "不在扫描根内"},
		{"①新增落在自己的数据目录里", func(r *usnDeltaRec) { r.Reason = reasonFileCreate; r.InSnapshot = false; r.Path = testData + `\x\a.bin` }, "数据目录"},
		{"②删除但快照里没有", func(r *usnDeltaRec) { r.Reason = reasonFileDelete; r.InSnapshot = false }, "删除记录"},
		{"③改名（旧名）", func(r *usnDeltaRec) { r.Reason = reasonRenameOld }, "改名"},
		{"③改名（新名）", func(r *usnDeltaRec) { r.Reason = reasonRenameNew }, "改名"},
		{"④硬链接变化", func(r *usnDeltaRec) { r.Reason = reasonHardLink }, "硬链接"},
		{"⑤记录落在目录上", func(r *usnDeltaRec) { r.IsDir = true; r.Reason = reasonFileCreate }, "目录"},
		{"⑥压缩属性变化", func(r *usnDeltaRec) { r.Reason = reasonCompression }, "压缩"},
		{"⑥加密属性变化", func(r *usnDeltaRec) { r.Reason = reasonEncryption }, "加密"},
		{"⑥稀疏/完整性属性变化", func(r *usnDeltaRec) { r.Reason = reasonIntegrity }, "压缩/加密/稀疏"},
		{"其他：命名流", func(r *usnDeltaRec) { r.Reason = reasonStream }, "命名流"},
		{"其他：ACL/owner", func(r *usnDeltaRec) { r.Reason = reasonSecurity }, "ACL"},
		{"其他：mtime 属性", func(r *usnDeltaRec) { r.Reason = reasonBasicInfo }, "mtime"},
		{"其他：新建却已在快照里", func(r *usnDeltaRec) { r.Reason = reasonFileCreate; r.InSnapshot = true }, "漏掉了删除"},
		{"其他：不认识的位组合", func(r *usnDeltaRec) { r.Reason = 0x00000040 }, "不认识"},
	}
	for _, tc := range cases {
		r := base
		tc.mutate(&r)
		kind, why := classifyDeltaRec(r, testRoot, testData)
		if kind != deltaFallback {
			t.Fatalf("%s：应当回落，实际 kind=%v", tc.name, kind)
		}
		if !strings.Contains(why, tc.want) {
			t.Fatalf("%s：理由应当提到 %q，实际 %q", tc.name, tc.want, why)
		}
	}

	// 反过来：三条能应用的记录必须被判成对应动作，不能被一刀切拒绝。
	ok := []struct {
		name string
		r    usnDeltaRec
		want deltaKind
	}{
		{"新增（路径可定位）", usnDeltaRec{Path: `C:\Users\a\new.txt`, Reason: reasonFileCreate}, deltaCreate},
		{"删除（快照里有）", usnDeltaRec{Path: `C:\Users\a\old.txt`, Reason: reasonFileDelete, InSnapshot: true, OldSize: 10}, deltaDelete},
		{"写（快照里有）", usnDeltaRec{Path: `C:\Users\a\w.txt`, Reason: reasonDataOverwrite, InSnapshot: true, OldSize: 10, NewSize: 20}, deltaWrite},
	}
	for _, tc := range ok {
		if kind, why := classifyDeltaRec(tc.r, testRoot, testData); kind != tc.want {
			t.Fatalf("%s：应当判成 %v，实际 %v（%s）", tc.name, tc.want, kind, why)
		}
	}

	// CLOSE-only：没有改动，跳过（不是回落）。这跟 t7 的白名单是同一个判断。
	if kind, why := classifyDeltaRec(usnDeltaRec{Path: `C:\Users\a\x`, Reason: reasonClose}, testRoot, testData); kind != deltaSkip {
		t.Fatalf("只有 CLOSE 的记录应当跳过，实际 %v（%s）", kind, why)
	}
}

// scanFromFiles 是「全量路径」的参考实现：按文件清单直接算聚合。
// 刻意不复用 catForPath——两条路若共用同一个分类函数，分类写错时对拍也发现不了。
func scanFromFiles(files []testFile) *Scan {
	sc := &Scan{Root: testRoot, Cats: map[string]uint64{}, Source: "full"}
	for _, f := range files {
		cat := classify(strings.ToLower(parentDir(f.path)) + `\`)
		switch strings.ToLower(f.base()) {
		case "hiberfil.sys", "swapfile.sys":
			cat = "system_hiber"
		case "pagefile.sys":
			cat = "system_pagefile"
		case "memory.dmp":
			cat = "logs_dumps"
		}
		sc.Total += f.size
		sc.Files++
		sc.Cats[cat] += f.size
	}
	sc.Unique = uniqueBytes(sc.Total, sc.Shared)
	return sc
}

type testFile struct {
	path string
	size uint64
}

func (f testFile) base() string {
	if i := strings.LastIndexByte(f.path, '\\'); i >= 0 {
		return f.path[i+1:]
	}
	return f.path
}

// 增量应用的基本功：新增 / 删除 / 涨 / 缩，数值与分类都要对。
func TestApplyUsnDeltasBasicOps(t *testing.T) {
	base := scanFromFiles([]testFile{
		{`C:\Users\a\keep.txt`, 1000},
		{`C:\Users\a\grow.bin`, 2000},
		{`C:\Users\a\shrink.bin`, 500},
		{`C:\Users\a\gone.bin`, 300},
	})
	recs := []usnDeltaRec{
		{Path: `C:\Users\a\new.bin`, Reason: reasonFileCreate, NewSize: 700},
		{Path: `C:\Users\a\gone.bin`, Reason: reasonFileDelete, InSnapshot: true, OldSize: 300},
		{Path: `C:\Users\a\grow.bin`, Reason: reasonDataExtend, InSnapshot: true, OldSize: 2000, NewSize: 2500},
		{Path: `C:\Users\a\shrink.bin`, Reason: reasonDataTruncate, InSnapshot: true, OldSize: 500, NewSize: 100},
	}
	got, why := applyUsnDeltas(base, recs, testRoot, testData)
	if why != "" {
		t.Fatalf("这组记录应当能应用，却回落了：%s", why)
	}
	if got.Total != 1000+2500+100+700 {
		t.Fatalf("Total 不对：%d", got.Total)
	}
	if got.Files != 4 {
		t.Fatalf("Files 不对：%d", got.Files)
	}
	if why := checkScanInvariants(got); why != "" {
		t.Fatalf("应用后不变量不满足：%s", why)
	}
	// 基线不能被改：增量结果是新的一份。
	if base.Total != 3800 || base.Files != 4 {
		t.Fatalf("基线被就地改了：%+v", base)
	}
}

// 不变量闸门：基线自己就自相矛盾时，增量必须拒绝，而不是在上面继续算。
func TestApplyUsnDeltasRefusesBrokenInvariant(t *testing.T) {
	broken := &Scan{Total: 1000, Files: 1, Cats: map[string]uint64{"temp": 800}, Shared: 0}
	if why := applyUsnDeltasMustFail(t, broken); !strings.Contains(why, "不变量") {
		t.Fatalf("分类合计对不上 Total 时应回落，实际理由 %q", why)
	}

	badUnique := &Scan{Total: 1000, Files: 1, Cats: map[string]uint64{"temp": 1000}, Shared: 100, Unique: 1000}
	if why := applyUsnDeltasMustFail(t, badUnique); !strings.Contains(why, "不变量") {
		t.Fatalf("Unique+Shared!=Total 时应回落，实际理由 %q", why)
	}
}

func applyUsnDeltasMustFail(t *testing.T, base *Scan) string {
	t.Helper()
	sc, why := applyUsnDeltas(base, []usnDeltaRec{
		{Path: `C:\Users\a\new.bin`, Reason: reasonFileCreate, NewSize: 10},
	}, testRoot, testData)
	if why == "" {
		t.Fatalf("应当回落，却返回了结果：%+v", sc)
	}
	if sc != nil {
		t.Fatalf("回落时不许交出快照，实际交了一份")
	}
	return why
}

// 对拍：同一组「旧快照 + 记录」，增量路径与全量路径的结果必须一致。
// 不一致时把差在哪一类记录上指出来，而不是只报一句「不一样」。
func TestApplyUsnDeltasDifferential(t *testing.T) {
	before := []testFile{
		{`C:\Users\a\keep.txt`, 1024},
		{`C:\Users\a\grow.bin`, 2048},
		{`C:\Windows\Temp\x.tmp`, 4096},
		{`C:\Users\a\gone.bin`, 512},
	}
	after := []testFile{
		{`C:\Users\a\keep.txt`, 1024},
		{`C:\Users\a\grow.bin`, 3072},          // 涨了
		{`C:\Windows\Temp\x.tmp`, 4096},        // 没动
		{`C:\Users\a\new-download.bin`, 8192},  // 新增
	}
	recs := []usnDeltaRec{
		{Path: `C:\Users\a\grow.bin`, Reason: reasonDataExtend, InSnapshot: true, OldSize: 2048, NewSize: 3072},
		{Path: `C:\Users\a\new-download.bin`, Reason: reasonFileCreate, NewSize: 8192},
		{Path: `C:\Users\a\gone.bin`, Reason: reasonFileDelete, InSnapshot: true, OldSize: 512},
	}

	want := scanFromFiles(after)
	got, why := applyUsnDeltas(scanFromFiles(before), recs, testRoot, testData)
	if why != "" {
		t.Fatalf("这组记录应当能应用：%s", why)
	}
	if diff := diffScans(want, got); diff != "" {
		t.Fatalf("增量与全量不一致：%s", diff)
	}

	// 反向对拍：夹一条改名记录，增量必须回落（而不是硬算出一个偏小的数）。
	withRename := append([]usnDeltaRec{{Path: `C:\Users\a\renamed.bin`, Reason: reasonRenameNew}}, recs...)
	if _, why := applyUsnDeltas(scanFromFiles(before), withRename, testRoot, testData); why == "" {
		t.Fatal("夹了改名记录时必须回落")
	} else if !strings.Contains(why, "改名") {
		t.Fatalf("回落理由应当点明改名，实际 %q", why)
	}
}

// diffScans 逐字段比较，把差异说成人话（差在哪一类）。
func diffScans(want, got *Scan) string {
	if want.Total != got.Total {
		return fmt.Sprintf("Total：全量 %d vs 增量 %d（差 %d）", want.Total, got.Total, int64(want.Total)-int64(got.Total))
	}
	if want.Files != got.Files {
		return fmt.Sprintf("Files：全量 %d vs 增量 %d", want.Files, got.Files)
	}
	if want.Unique != got.Unique || want.Shared != got.Shared {
		return fmt.Sprintf("Unique/Shared：全量 %d/%d vs 增量 %d/%d", want.Unique, want.Shared, got.Unique, got.Shared)
	}
	cats := map[string]bool{}
	for k := range want.Cats {
		cats[k] = true
	}
	for k := range got.Cats {
		cats[k] = true
	}
	for k := range cats {
		if want.Cats[k] != got.Cats[k] {
			return fmt.Sprintf("分类 %q：全量 %d vs 增量 %d", k, want.Cats[k], got.Cats[k])
		}
	}
	return ""
}

// 只有 CLOSE 的记录：不是回落，是「这批量里什么都没发生」，结果应与基线等价。
func TestApplyUsnDeltasIgnoresCloseOnly(t *testing.T) {
	base := scanFromFiles([]testFile{{`C:\Users\a\a.txt`, 100}, {`C:\Temp\b.tmp`, 200}})
	got, why := applyUsnDeltas(base, []usnDeltaRec{
		{Path: `C:\Users\a\a.txt`, Reason: reasonClose},
		{Path: `C:\Temp\b.tmp`, Reason: reasonClose},
	}, testRoot, testData)
	if why != "" {
		t.Fatalf("CLOSE-only 不该回落：%s", why)
	}
	if got == base {
		t.Fatal("结果必须是新的一份，不能把基线原样交出去")
	}
	if diff := diffScans(base, got); diff != "" {
		t.Fatalf("CLOSE-only 不该改变任何数字：%s", diff)
	}
}

// 删除一个「比整个快照还大」的文件：基线不可能对，必须回落而不是减成负数（uint64 会回绕成大数，
// 那会变成「凭空多出 16 EB」——比算少更离谱）。
func TestApplyUsnDeltasRefusesImpossibleDelete(t *testing.T) {
	base := scanFromFiles([]testFile{{`C:\a.txt`, 100}})
	_, why := applyUsnDeltas(base, []usnDeltaRec{
		{Path: `C:\a.txt`, Reason: reasonFileDelete, InSnapshot: true, OldSize: 999999},
	}, testRoot, testData)
	if why == "" || !strings.Contains(why, "变负") {
		t.Fatalf("应回落并说明会变负，实际 %q", why)
	}
}

// 记录太多就不值得增量了：直接回落，别把自己跑成一次全扫。
func TestApplyUsnDeltasTooManyRecords(t *testing.T) {
	base := scanFromFiles([]testFile{{`C:\a.txt`, 1}})
	recs := make([]usnDeltaRec, deltaApplyLimit+1)
	if _, why := applyUsnDeltas(base, recs, testRoot, testData); !strings.Contains(why, "记录太多") {
		t.Fatalf("超限时应回落，实际 %q", why)
	}
}
