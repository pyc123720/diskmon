package main

// t16 / B2 Phase 2A 的「量代价」用的测量用例。
//
// 它**不参与**常规测试：没有 DISKMON_MFTIDX_MEASURE=1 就直接 Skip。
// 因为它要读一整块真卷，几分钟起步，绝不会是跑 CI 该干的事。
//
// 两个口径，取决于有没有管理员权限，报告里必须说清用的是哪一个：
//
//	A) 有管理员 → readMft 直读 $MFT，拿到的是「MFT 记录集」——和生产路径完全一致。
//	B) 没管理员 → 目录遍历（WalkDir）+ 每个文件一次 GetFileInformationByHandle
//	   取真实 FRN 与链接数。得到的是「遍历可见集」：少了 ACL 挡住的目录、
//	   多了 junction 造成的重复路径。条数会偏少、FRN 差值会偏大（体积偏大）。
//
// B 口径的每个数字都是实测，不是估算——只是集合不同。这一条在报告里要写明。
//
// 用法：
//
//	set DISKMON_MFTIDX_MEASURE=1
//	set DISKMON_MFTIDX_ROOT=C:\  (可选，默认 C:\)
//	diskmon.test.exe -test.run TestMeasureMftIndex -test.v -test.timeout 0

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fileReadAttributes 是「只读属性」的访问权：只取元数据，不读内容，
// 所以对正在被占用的文件也能打开（配合全共享标志）。
const fileReadAttributes = 0x0080

// fileIndexOf 取一个文件的 (FRN, size, links)。
// FRN 就是 NTFS 的 MFT 记录号（低 64 位），和 parseRecord 里 f.Record 的口径一致。
//
// 为什么不用 os.Stat：它不给 FRN，也不给链接数。这两个正是索引要存的。
func fileIndexOf(path string) (frn uint64, size uint64, links uint8, err error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, 0, err
	}
	h, err := syscall.CreateFile(p,
		fileReadAttributes,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, 0, 0, err
	}
	defer func() { _ = syscall.CloseHandle(h) }()
	var d syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &d); err != nil {
		return 0, 0, 0, err
	}
	frn = uint64(d.FileIndexHigh)<<32 | uint64(d.FileIndexLow)
	size = uint64(d.FileSizeHigh)<<32 | uint64(d.FileSizeLow)
	links = uint8(d.NumberOfLinks)
	return frn, size, links, nil
}

// collectByWalk 是 B 口径的采集器：遍历 + 逐文件取 FRN。
func collectByWalk(root string, limit int) (files map[uint64]mftFile, stats struct {
	seen, opened, openFailed int
	walkErr                  int
}) {
	files = make(map[uint64]mftFile, 1<<18)
	stats.walkErr = 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			stats.walkErr++
			return nil // 读不进去就算了，别中断整次测量
		}
		if d.IsDir() {
			return nil
		}
		stats.seen++
		if limit > 0 && len(files) >= limit {
			return filepath.SkipAll
		}
		frn, size, links, gerr := fileIndexOf(path)
		if gerr != nil {
			stats.openFailed++
			return nil
		}
		stats.opened++
		if frn != 0 {
			files[frn] = mftFile{Record: frn, Name: d.Name(), Size: size, Links: int(links)}
		}
		return nil
	})
	return files, stats
}

func TestMeasureMftIndex(t *testing.T) {
	if os.Getenv("DISKMON_MFTIDX_MEASURE") != "1" {
		t.Skip("测量用例：置 DISKMON_MFTIDX_MEASURE=1 才跑（要读整卷，几分钟）")
	}
	root := os.Getenv("DISKMON_MFTIDX_ROOT")
	if root == "" {
		root = `C:\`
	}
	limit := 0
	var files map[uint64]mftFile
	letter, _ := volumeLetter(root)
	source := ""

	// 复用模式：直接读上一次真实落盘的那份索引，跳过采集。
	// 用途是「不再花 4 分钟重走一遍卷，也能重复量加载耗时和常驻内存」。
	// 它复用的一定是**实测产出的文件**，不是造出来的数据。
	cachePath := os.Getenv("DISKMON_MFTIDX_INDEX")
	if cachePath != "" {
		if _, err := os.Stat(cachePath); err == nil {
			tIdx, lerr := loadMftIndex(cachePath)
			if lerr != nil {
				t.Fatalf("复用索引读不出来：%v", lerr)
			}
			t.Logf("════ B2 Phase 2A 复用量测（口径：复用上次实测落盘的 %s）════", cachePath)
			st, _ := os.Stat(cachePath)
			var mm0, mm1 runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&mm0)
			_, _ = loadMftIndex(cachePath) // 第二份，活着，量它
			second, _ := loadMftIndex(cachePath)
			runtime.GC()
			runtime.ReadMemStats(&mm1)
			held2 := int64(0)
			if mm1.HeapAlloc > mm0.HeapAlloc {
				held2 = int64(mm1.HeapAlloc - mm0.HeapAlloc)
			}
			tl := time.Now()
			th, _ := loadMftIndex(cachePath)
			el := time.Since(tl)
			t.Logf("索引条数        %d", tIdx.Len())
			t.Logf("落盘体积        %d 字节", st.Size())
			t.Logf("加载耗时        %s（含 ReadFile；文件在系统缓存里，属乐观值）", el.Round(time.Microsecond))
			t.Logf("常驻内存        %d 字节（HeapAlloc 差值，第二份索引）/ 理论 %d 字节（17 字节/条）",
				held2, tIdx.Len()*17)
			runtime.KeepAlive(second)
			runtime.KeepAlive(th)
			return
		}
	}

	t0 := time.Now()
	if isElevated() && letter != "" {
		res, _, mf, err := readMft(letter, "")
		if err != nil {
			t.Logf("MFT 直读失败（%v），改用遍历口径", err)
		} else {
			files = mf
			source = fmt.Sprintf("MFT 直读（$MFT 记录集）：%d 文件 / %d 目录", res.Files, res.Dirs)
		}
	}
	var st struct {
		seen, opened, openFailed int
		walkErr                  int
	}
	if files == nil {
		var files2 map[uint64]mftFile
		files2, st = collectByWalk(root, limit)
		files = files2
		source = fmt.Sprintf("目录遍历（可见集）：看到 %d 个文件，打开成功 %d，打开失败 %d，目录读不进 %d 处",
			st.seen, st.opened, st.openFailed, st.walkErr)
	}
	collectElapsed := time.Since(t0)

	// —— 构建 ——
	tb := time.Now()
	idx := buildMftIndex(files)
	idx.volume = strings.TrimSuffix(letter, `\`)
	buildElapsed := time.Since(tb)

	// —— 体积 ——
	encoded := encodeMftIndex(nil, idx)
	dir, err := os.MkdirTemp(".", "mftidx-measure")
	if err != nil {
		t.Fatalf("建临时目录失败：%v", err)
	}
	defer os.RemoveAll(dir)
	path := mftIndexPath(dir, "C")
	written, err := saveMftIndex(path, idx)
	if err != nil {
		t.Fatalf("落盘失败：%v", err)
	}
	diskSize := int64(-1)
	if si, err := os.Stat(path); err == nil {
		diskSize = si.Size()
	}

	// —— 加载（同一进程、文件刚写完 → 一定命中系统缓存，属乐观值）——
	tl := time.Now()
	loaded, err := loadMftIndex(path)
	loadElapsed := time.Since(tl)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	// 再读一次，量的是纯解析（不含写路径的缓存预热影响）
	tl2 := time.Now()
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	reloadElapsed := time.Since(tl2)
	tp := time.Now()
	if _, err := decodeMftIndex(buf); err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	decodeOnly := time.Since(tp)

	// —— 常驻内存 ——
	// 量法要避开一个坑：encode 会临时造一份几 MB 的字节，那是垃圾不是索引。
	// 所以先把编码结果准备好（它活着），GC 取基线，**再**解出第二份索引，
	// GC 后再取一次——两次读数里都包含那份字节，差值才是索引本身。
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	liveCopy, err := decodeMftIndex(encoded)
	if err != nil {
		t.Fatalf("内存测量用副本解码失败：%v", err)
	}
	runtime.GC()
	runtime.ReadMemStats(&m1)
	held := int64(0)
	if m1.HeapAlloc > m0.HeapAlloc {
		held = int64(m1.HeapAlloc - m0.HeapAlloc)
	}
	// 把这份真实索引留一份在盘上，方便之后用复用模式反复量，不必再走一遍卷。
	if keep := os.Getenv("DISKMON_MFTIDX_KEEP"); keep != "" {
		if _, kerr := saveMftIndex(keep, idx); kerr != nil {
			t.Logf("保留索引失败（不影响本次数字）：%v", kerr)
		} else {
			t.Logf("已保留索引：%s", keep)
		}
	}
	runtime.KeepAlive(liveCopy)

	// —— 正确性抽查：索引里任意一条的答案要和采集到的一致 ——
	checked := 0
	for frn, f := range files {
		size, links, ok := idx.lookup(frn)
		if !ok {
			if f.IsDir {
				continue
			}
			t.Fatalf("FRN %d 在采集结果里，却查不到", frn)
		}
		if size != f.Size || int(links) != f.Links {
			t.Fatalf("FRN %d 对不上：索引 %d/%d，采集 %d/%d", frn, size, links, f.Size, f.Links)
		}
		// 落盘再读回的那份也要给出同一个答案——不然「体积」和「加载」这两个
		// 数字量的就是另一份东西了。
		if ls, ll, lok := loaded.lookup(frn); !lok || ls != size || ll != links {
			t.Fatalf("FRN %d 落盘读回后答案不一致：%d/%d/%v", frn, ls, ll, lok)
		}
		checked++
		if checked >= 200 {
			break
		}
	}

	n := idx.Len()
	perEntry := 0.0
	perEntryDisk := 0.0
	if n > 0 {
		perEntry = float64(len(encoded)) / float64(n)
		perEntryDisk = float64(written) / float64(n)
	}

	// 大小分布，报告里要用来解释「为什么 varint 有效」
	var buckets = map[string]int{}
	links1 := 0
	for i := 0; i < n; i++ {
		_, size, links := idx.entry(i)
		switch {
		case size == 0:
			buckets["0"]++
		case size < 1<<10:
			buckets["<1KB"]++
		case size < 16<<10:
			buckets["1-16KB"]++
		case size < 1<<20:
			buckets["16KB-1MB"]++
		default:
			buckets[">=1MB"]++
		}
		if links == 1 {
			links1++
		}
	}
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	dist := make([]string, 0, len(keys))
	for _, k := range keys {
		dist = append(dist, fmt.Sprintf("%s=%d", k, buckets[k]))
	}

	t.Logf("════ B2 Phase 2A 实测（口径：%s）════", source)
	t.Logf("采集耗时        %s", collectElapsed.Round(time.Millisecond))
	t.Logf("索引条数        %d", n)
	t.Logf("构建耗时        %s（排序+填表，不含采集）", buildElapsed.Round(time.Microsecond))
	t.Logf("编码体积        内存 %d 字节 / 落盘 %d 字节", len(encoded), written)
	t.Logf("平均每条        内存 %.2f 字节 / 落盘 %.2f 字节", perEntry, perEntryDisk)
	t.Logf("磁盘实际占用    %d 字节", diskSize)
	t.Logf("加载耗时        %s（含 ReadFile，文件在系统缓存里）；纯解析 %s；再次 ReadFile %s",
		loadElapsed.Round(time.Microsecond), decodeOnly.Round(time.Microsecond), reloadElapsed.Round(time.Microsecond))
	t.Logf("常驻内存        %d 字节（HeapAlloc 差值）/ 理论 %d 字节（17 字节/条）", held, n*17)
	t.Logf("尺寸分布        %s", strings.Join(dist, "  "))
	t.Logf("单链接文件      %d / %d（%.1f%%）", links1, n, 100*float64(links1)/float64(maxInt(n, 1)))
	t.Logf("抽查一致        %d 条", checked)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
