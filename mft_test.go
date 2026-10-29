package main

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"time"
)

// ---------- 测试 ----------

// ---------- 合成记录的工具 ----------

func putNameAttr(rec []byte, off int, parent uint64, name string, ns byte, realSize uint64) int {
	u := []uint16{}
	for _, r := range name {
		u = append(u, uint16(r))
	}
	valLen := 0x42 + len(u)*2
	attrLen := (0x18 + valLen + 7) &^ 7

	binary.LittleEndian.PutUint32(rec[off:], attrFileName)
	binary.LittleEndian.PutUint32(rec[off+4:], uint32(attrLen))
	rec[off+8] = 0 // resident
	binary.LittleEndian.PutUint16(rec[off+0x0E:], 1)
	binary.LittleEndian.PutUint32(rec[off+0x10:], uint32(valLen))
	binary.LittleEndian.PutUint16(rec[off+0x14:], 0x18)

	v := rec[off+0x18:]
	binary.LittleEndian.PutUint64(v[0:], parent)
	binary.LittleEndian.PutUint64(v[0x30:], realSize)
	v[0x40] = byte(len(u))
	v[0x41] = ns
	for i, c := range u {
		binary.LittleEndian.PutUint16(v[0x42+i*2:], c)
	}
	return off + attrLen
}

func putDataAttr(rec []byte, off int, realSize, allocSize uint64, runs []byte) int {
	attrLen := (0x40 + len(runs) + 7) &^ 7
	binary.LittleEndian.PutUint32(rec[off:], attrData)
	binary.LittleEndian.PutUint32(rec[off+4:], uint32(attrLen))
	rec[off+8] = 1 // non-resident
	binary.LittleEndian.PutUint16(rec[off+0x0E:], 2)
	binary.LittleEndian.PutUint64(rec[off+0x18:], 0) // highestVcn
	binary.LittleEndian.PutUint16(rec[off+0x20:], 0x40)
	binary.LittleEndian.PutUint64(rec[off+0x28:], allocSize)
	binary.LittleEndian.PutUint64(rec[off+0x30:], realSize)
	binary.LittleEndian.PutUint64(rec[off+0x38:], realSize)
	copy(rec[off+0x40:], runs)
	return off + attrLen
}

type testName struct {
	name   string
	ns     byte
	parent uint64
}

func buildRecord(inUse, dir bool, links int, baseRef uint64, names []testName, dataSize uint64) []byte {
	rec := make([]byte, 1024)
	binary.LittleEndian.PutUint32(rec[0:], fileRecordMagic)
	binary.LittleEndian.PutUint16(rec[4:], 0x30) // USA 偏移
	binary.LittleEndian.PutUint16(rec[6:], 3)    // USA 条目数 = 扇区数 + 1
	binary.LittleEndian.PutUint16(rec[0x12:], uint16(links))
	binary.LittleEndian.PutUint16(rec[0x14:], 0x38) // 第一个属性
	flags := uint16(0)
	if inUse {
		flags |= flagInUse
	}
	if dir {
		flags |= flagDir
	}
	binary.LittleEndian.PutUint16(rec[0x16:], flags)
	binary.LittleEndian.PutUint64(rec[0x20:], baseRef)

	// 更新序列：扇区末尾两字节放同一个 USN
	const usn = 0x0007
	binary.LittleEndian.PutUint16(rec[0x30:], usn)
	binary.LittleEndian.PutUint16(rec[0x32:], 0xAAAA)
	binary.LittleEndian.PutUint16(rec[0x34:], 0xBBBB)
	binary.LittleEndian.PutUint16(rec[510:], usn)
	binary.LittleEndian.PutUint16(rec[1022:], usn)

	off := 0x38
	for _, n := range names {
		off = putNameAttr(rec, off, n.parent, n.name, n.ns, dataSize)
	}
	if !dir && dataSize > 0 {
		// 一段虚构的 data run
		off = putDataAttr(rec, off, dataSize, dataSize+4096, []byte{0x11, 0x08, 0x10, 0x00})
	}
	binary.LittleEndian.PutUint32(rec[off:], attrEnd)
	return rec
}

// 属性列表把 $DATA 溢出到扩展记录时，主记录里只剩 $FILE_NAME。
// 尺寸必须能从 $FILE_NAME 的 RealSize 读出来，否则这类文件在统计里会变成 0。
func TestParseRecordSizeFromFileName(t *testing.T) {
	const fname = "big.bin"
	const realSz = uint64(3 << 30) // 3 GB

	valLen := 0x42 + len([]rune(fname))*2
	nameAttrLen := (0x18 + valLen + 7) &^ 7

	rec := buildRecord(true, false, 1, 0,
		[]testName{{name: fname, ns: 1, parent: 5}}, realSz)

	// 把紧跟其后的 $DATA 属性改成结束标记，模拟「$DATA 溢出到扩展记录」
	binary.LittleEndian.PutUint32(rec[0x38+nameAttrLen:], attrEnd)

	f, ok := parseRecord(rec)
	if !ok {
		t.Fatal("parseRecord 拒绝了这条记录")
	}
	if f.Name != fname {
		t.Fatalf("名字 = %q，期望 %q", f.Name, fname)
	}
	if f.Size != realSz {
		t.Fatalf("尺寸 = %d，期望 %d（应从 $FILE_NAME 取）", f.Size, realSz)
	}
	if !f.SizeFromName {
		t.Fatal("SizeFromName 应为 true：$DATA 缺席时尺寸来自 $FILE_NAME")
	}
}

// 分桶对账靠这个函数把路径归到顶层目录，错一位整个对账就跑偏。
func TestTopComponent(t *testing.T) {
	cases := []struct{ volume, path, want string }{
		{`C:\`, `C:\Windows\System32\a.dll`, "Windows"},
		{`C:\`, `C:\Users\lenovo\a.txt`, "Users"},
		{`C:\`, `C:\pagefile.sys`, "<根目录文件>"},
		{`C:\`, `D:\Other\x`, ""},
		{`C:\`, `C:\`, ""},
	}
	for _, c := range cases {
		if got := topComponent(c.volume, c.path); got != c.want {
			t.Errorf("topComponent(%q, %q) = %q，期望 %q", c.volume, c.path, got, c.want)
		}
	}
}

// 空闲槽位必须被识别成「不在用」，否则它会混进解析失败的计数里。
func TestInUseRecord(t *testing.T) {
	live := buildRecord(true, false, 1, 0, []testName{{name: "a.txt", ns: 1, parent: 5}}, 10)
	if !inUseRecord(live) {
		t.Fatal("在用记录应判定为在用")
	}
	dead := buildRecord(false, false, 0, 0, nil, 0)
	if inUseRecord(dead) {
		t.Fatal("空闲记录不应判定为在用")
	}
	if inUseRecord([]byte{1, 2, 3}) {
		t.Fatal("残缺缓冲不应判定为在用")
	}
}

// MFT 引擎只接「整卷」这一种活，别的一律回落遍历。
//
// 这条不变量比它看起来重要：MFT 读的就是整个卷，文件那一侧还能用 HasPrefix 过滤掉
// 卷外的路径，但 res.Dirs 是整卷的目录总数、没有对应的过滤。若哪天有人想让
// `scan` 支持子目录根，却不小心让 MFT 接住了，目录数就会静默变成整卷的 247,800。
// 这个测试不碰磁盘：守卫在 readMft 之前就返回了。
func TestScanVolumeViaMftRejectsNonVolumeRoot(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	for _, root := range []string{
		`C:\Users`,
		`C:\Users\lenovo\Documents`,
		`C:\Windows\`,
		`\\server\share`,
		``,
	} {
		if sc, ok := scanVolumeViaMft(cfg, root); ok || sc != nil {
			t.Fatalf("root=%q 不该被 MFT 接走，否则目录数会变成整卷口径", root)
		}
	}
}

// scanVolumeViaMft 用 `base := path[len(dir)+1:]` 取文件名，配合 parentDir。
// 这行很容易被看错成 off-by-one：parentDir 返回的是不带尾反斜杠的 "C:"（2 字符），
// 所以 len(dir)+1 正好落在分隔符之后，切出来的就是纯文件名。
//
// 它必须成立，否则根目录那几个体积最大的文件——hiberfil.sys / pagefile.sys /
// swapfile.sys / memory.dmp——会切错名字，专属分类永远匹配不上，全部掉进兜底分类。
func TestRootLevelBaseNameSplit(t *testing.T) {
	for _, path := range []string{
		`C:\hiberfil.sys`,
		`C:\pagefile.sys`,
		`C:\swapfile.sys`,
		`C:\memory.dmp`,
		`D:\a.bin`,
		`C:\Windows\System32\foo.dll`,
		`C:\Users\lenovo\Documents\x.txt`,
	} {
		dir := parentDir(path)
		base := path[len(dir)+1:]
		want := path[strings.LastIndexByte(path, '\\')+1:]
		if base != want {
			t.Fatalf("%q：切成 %q，应为 %q（parentDir 返回 %q）", path, base, want, dir)
		}
		// 再确认它确实是「父目录 + 分隔符 + 文件名」这个形状
		if dir+`\`+base != path {
			t.Fatalf("%q：dir=%q base=%q 拼不回原路径", path, dir, base)
		}
	}
}

// 诊断用：验证对账基准自己是对的（跳过 junction 之后应当和扫描结果一致）。
// 默认跳过，需要时：
//
//	$env:DISKMON_ROOT_TEST="C:\"; go test -run TestWalkStatsRealVolume -v
func TestWalkStatsRealVolume(t *testing.T) {
	root := os.Getenv("DISKMON_ROOT_TEST")
	if root == "" {
		t.Skip("未设置 DISKMON_ROOT_TEST，跳过真实卷对账基准诊断")
	}
	files, dirs, total, el, _, _, _ := walkStats(root)
	t.Logf("目录遍历：目录 %d、文件 %d、合计 %.2f GB，耗时 %.1fs",
		dirs, files, float64(total)/(1<<30), el.Seconds())

	// 与 scanVolume 的结果对比：跳过 junction 之后两者应当非常接近
	cfg := Config{Root: root, DataDir: t.TempDir()}
	sc := scanVolume(cfg, root)
	t.Logf("scanVolume：目录 %d、文件 %d、合计 %.2f GB", sc.Dirs, sc.Files, float64(sc.Total)/(1<<30))

	diffGB := (float64(total) - float64(sc.Total)) / (1 << 30)
	t.Logf("差异：%.2f GB", diffGB)
	if diffGB > 5 || diffGB < -5 {
		t.Fatalf("对账基准和扫描差 %.2f GB，超过 5 GB——junction 可能没被跳过", diffGB)
	}
}

func TestParseVolumeData(t *testing.T) {
	b := make([]byte, 96)
	binary.LittleEndian.PutUint64(b[0:], 0x1122334455667788)
	binary.LittleEndian.PutUint32(b[44:], 4096) // BytesPerCluster
	binary.LittleEndian.PutUint32(b[48:], 1024) // BytesPerFileRecordSegment
	binary.LittleEndian.PutUint64(b[56:], 700*1024*1024)
	binary.LittleEndian.PutUint64(b[64:], 786432) // MftStartLcn

	vd, err := parseVolumeData(b)
	if err != nil {
		t.Fatal(err)
	}
	if vd.BytesPerCluster != 4096 || vd.BytesPerFileRecordSegment != 1024 {
		t.Fatalf("卷参数解析错：%+v", vd)
	}
	if vd.MftStartLcn != 786432 {
		t.Fatalf("MftStartLcn=%d", vd.MftStartLcn)
	}
	if vd.VolumeSerialNumber != 0x1122334455667788 {
		t.Fatalf("序列号解析错")
	}
}

func TestParseVolumeDataRejectsShortBuffer(t *testing.T) {
	if _, err := parseVolumeData(make([]byte, 48)); err == nil {
		t.Fatal("缓冲区不足 96 字节时应当报错")
	}
}

func TestDecodeRunsSingle(t *testing.T) {
	runs, err := decodeRuns([]byte{0x11, 0x03, 0x04, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].clusters != 3 || runs[0].lcn != 4 || runs[0].vcn != 0 {
		t.Fatalf("解出的 run 不对：%+v", runs)
	}
}

func TestDecodeRunsRelativeOffsets(t *testing.T) {
	// 第一段 LCN=16 长 2，第二段相对 +5 长 1（VC 累加）
	runs, err := decodeRuns([]byte{0x11, 0x02, 0x10, 0x11, 0x01, 0x05, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("run 数=%d，期望 2", len(runs))
	}
	if runs[0].lcn != 16 || runs[0].clusters != 2 || runs[0].vcn != 0 {
		t.Fatalf("第一段错：%+v", runs[0])
	}
	if runs[1].lcn != 21 || runs[1].clusters != 1 || runs[1].vcn != 2 {
		t.Fatalf("第二段错（相对偏移没累加？）：%+v", runs[1])
	}
}

func TestDecodeRunsHandlesNegativeDelta(t *testing.T) {
	// 偏移 0xFF = -1，应回退一段
	runs, err := decodeRuns([]byte{0x11, 0x02, 0x10, 0x11, 0x01, 0xFF, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	if runs[1].lcn != 15 {
		t.Fatalf("有符号偏移没处理：lcn=%d，期望 15", runs[1].lcn)
	}
}

func TestDecodeRunsRejectsGarbage(t *testing.T) {
	if _, err := decodeRuns([]byte{0x00}); err == nil {
		t.Fatal("空 run list 应当报错")
	}
}

func TestParseRecordFile(t *testing.T) {
	raw := buildRecord(true, false, 1, 0, []testName{{"hello.txt", 1, 5}}, 12345)
	f, ok := parseRecord(raw)
	if !ok {
		t.Fatal("合法记录应当被接受")
	}
	if f.Name != "hello.txt" {
		t.Fatalf("Name=%q", f.Name)
	}
	if f.IsDir {
		t.Fatal("不该被判成目录")
	}
	if f.Parent != 5 {
		t.Fatalf("Parent=%d，期望 5", f.Parent)
	}
	if f.Size != 12345 {
		t.Fatalf("Size=%d，期望 12345", f.Size)
	}
}

func TestParseRecordDirectoryHasNoSize(t *testing.T) {
	raw := buildRecord(true, true, 1, 0, []testName{{"Windows", 1, 5}}, 0)
	f, ok := parseRecord(raw)
	if !ok {
		t.Fatal("目录记录也应当被接受")
	}
	if !f.IsDir {
		t.Fatal("应判为目录")
	}
	if f.Size != 0 {
		t.Fatalf("目录不该有文件大小，得到 %d", f.Size)
	}
}

func TestParseRecordRejectsNotInUse(t *testing.T) {
	raw := buildRecord(false, false, 1, 0, []testName{{"gone.txt", 1, 5}}, 10)
	if _, ok := parseRecord(raw); ok {
		t.Fatal("未使用的记录必须被跳过")
	}
}

func TestParseRecordRejectsExtensionRecord(t *testing.T) {
	// baseRef 非 0 = 扩展记录，信息属于主记录
	raw := buildRecord(true, false, 1, 6, []testName{{"ext.bin", 1, 5}}, 10)
	if _, ok := parseRecord(raw); ok {
		t.Fatal("扩展记录必须被跳过，否则同一条文件会被算两次")
	}
}

func TestParseRecordRejectsBadMagic(t *testing.T) {
	raw := buildRecord(true, false, 1, 0, []testName{{"x.bin", 1, 5}}, 10)
	binary.LittleEndian.PutUint32(raw[0:], 0xDEADBEEF)
	if _, ok := parseRecord(raw); ok {
		t.Fatal("非 FILE 记录必须被跳过")
	}
}

func TestParseRecordPrefersWin32OverDOS(t *testing.T) {
	// 同一文件两个名字：DOS 8.3 与 Win32，必须取 Win32
	raw := buildRecord(true, false, 2, 0, []testName{
		{"LONGFI~1.TXT", 2, 5},
		{"longfilename.txt", 1, 5},
	}, 99)
	f, ok := parseRecord(raw)
	if !ok {
		t.Fatal("应当被接受")
	}
	if f.Name != "longfilename.txt" {
		t.Fatalf("Name=%q，期望取 Win32 名字", f.Name)
	}
	if f.Links != 2 {
		t.Fatalf("Links=%d，期望 2（硬链接数）", f.Links)
	}
}

func TestParseRecordSurvivesTruncatedBuffer(t *testing.T) {
	raw := buildRecord(true, false, 1, 0, []testName{{"x.bin", 1, 5}}, 10)
	if _, ok := parseRecord(raw[:40]); ok {
		t.Fatal("太短的缓冲区不该被当成合法记录")
	}
}

func TestBuildPaths(t *testing.T) {
	files := map[uint64]mftFile{
		5:  {Record: 5, Name: "", IsDir: true},
		10: {Record: 10, Parent: 5, Name: "Windows", IsDir: true},
		11: {Record: 11, Parent: 10, Name: "System32", IsDir: true},
		12: {Record: 12, Parent: 11, Name: "kernel32.dll", Size: 1000},
	}
	paths := buildPaths(files, `C:\`)
	if got := paths[12]; got != `C:\Windows\System32\kernel32.dll` {
		t.Fatalf("路径还原错：%q", got)
	}
	if got := paths[10]; got != `C:\Windows` {
		t.Fatalf("中间层路径错：%q", got)
	}
}

func TestBuildPathsBreaksOnCycle(t *testing.T) {
	// 损坏的父引用成环时不能死循环
	files := map[uint64]mftFile{
		20: {Record: 20, Parent: 21, Name: "a"},
		21: {Record: 21, Parent: 20, Name: "b"},
	}
	done := make(chan struct{})
	go func() {
		buildPaths(files, `C:\`)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("父引用成环时卡死了")
	}
}

// $MFT 自己的 $DATA 会因 run list 过长被拆成多个分段。
// 只取第一段会静悄悄丢掉卷的后半部分——WinSxS 少算 3.5 万个目录就是这个原因。
func TestExtractMftRunsMultiSegment(t *testing.T) {
	rec := make([]byte, 1024)
	binary.LittleEndian.PutUint32(rec[0:], fileRecordMagic)
	binary.LittleEndian.PutUint16(rec[0x12:], 1)
	binary.LittleEndian.PutUint16(rec[0x14:], 0x38) // 首个属性
	binary.LittleEndian.PutUint16(rec[0x16:], flagInUse)
	// USA 偏移留 0：applyFixups 直接返回，记录保持原样

	// 故意把「第二段」先写进记录里：extractMftRuns 必须按起始 VCN 排序，而不是按出现顺序
	off := 0x38
	seg2Runs := []byte{0x11, 0x08, 0x40, 0x00} // 8 簇，LCN 0x40
	seg2Start := off
	off = putDataAttr(rec, off, 1<<20, 1<<20, seg2Runs)
	binary.LittleEndian.PutUint64(rec[seg2Start+0x10:], 16) // 起始 VCN = 16

	seg1Runs := []byte{0x11, 0x10, 0x30, 0x00} // 16 簇，LCN 0x30
	seg1Start := off
	off = putDataAttr(rec, off, 1<<21, 1<<21, seg1Runs)
	binary.LittleEndian.PutUint64(rec[seg1Start+0x10:], 0) // 起始 VCN = 0

	binary.LittleEndian.PutUint32(rec[off:], attrEnd)

	runs, segs, hasAttrList, err := extractMftRuns(rec)
	if err != nil {
		t.Fatalf("extractMftRuns 失败：%v", err)
	}
	if segs != 2 {
		t.Fatalf("分段数 = %d，期望 2", segs)
	}
	if hasAttrList {
		t.Fatal("这条记录没有 $ATTRIBUTE_LIST，不该报有")
	}
	if len(runs) != 2 {
		t.Fatalf("run 数 = %d，期望 2（只取第一段就会变成 1）", len(runs))
	}
	// 排序后第一段应当是 VCN 0 的那段（写在记录末尾的那段）
	if runs[0].vcn != 0 || runs[0].clusters != 16 || runs[0].lcn != 0x30 {
		t.Fatalf("第一段错：vcn=%d clusters=%d lcn=%d", runs[0].vcn, runs[0].clusters, runs[0].lcn)
	}
	if runs[1].vcn != 16 || runs[1].clusters != 8 || runs[1].lcn != 0x40 {
		t.Fatalf("第二段错：vcn=%d clusters=%d lcn=%d", runs[1].vcn, runs[1].clusters, runs[1].lcn)
	}
}

// FILETIME 换算：按日期统计写入量靠它，算错一天就会串档。
func TestFiletimeToUnix(t *testing.T) {
	// 2024-01-01 00:00:00 UTC = 1601-01-01 起 13348540800 秒
	// （= 1704067200 + 11644473600）
	ft := int64(13348540800) * 10000000
	if got := filetimeToUnix(ft); got != 1704067200 {
		t.Fatalf("换算错：%d，期望 1704067200", got)
	}
	// 0 和越界值不能让上游算出 1601 年这种鬼日期
	if got := filetimeToUnix(0); got != 0 {
		t.Fatalf("0 应返回 0，实际 %d", got)
	}
	if got := filetimeToUnix(123456789); got != 0 {
		t.Fatalf("越界值应返回 0，实际 %d", got)
	}
}

// 实测样本：C:\hiberfil.sys —— 主记录尺寸 0，6.08 GB 在扩展记录里。
func TestParseExtData(t *testing.T) {
	const base = 3665
	const size = uint64(6 << 30)

	rec := make([]byte, 1024)
	binary.LittleEndian.PutUint32(rec[0:], fileRecordMagic)
	binary.LittleEndian.PutUint16(rec[0x12:], 1)
	binary.LittleEndian.PutUint16(rec[0x14:], 0x38) // 首个属性
	binary.LittleEndian.PutUint16(rec[0x16:], flagInUse)
	binary.LittleEndian.PutUint64(rec[0x20:], base) // baseRef 指向主记录
	// USA 偏移留 0：applyFixups 直接返回 false，记录保持原样

	off := putDataAttr(rec, 0x38, size, size, []byte{0x11, 0x08, 0x10, 0x00})
	binary.LittleEndian.PutUint32(rec[off:], attrEnd)

	gotBase, gotSize, ok := parseExtData(rec)
	if !ok {
		t.Fatal("parseExtData 没认出这条扩展记录")
	}
	if gotBase != base {
		t.Fatalf("baseRef = %d，期望 %d", gotBase, base)
	}
	if gotSize != size {
		t.Fatalf("尺寸 = %d，期望 %d", gotSize, size)
	}

	// 主记录（baseRef=0）不该被当成扩展记录
	main := buildRecord(true, false, 1, 0, []testName{{name: "a.txt", ns: 1, parent: 5}}, 10)
	if _, _, ok := parseExtData(main); ok {
		t.Fatal("主记录不该被 parseExtData 接受")
	}
}
