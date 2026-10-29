package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTopChild(t *testing.T) {
	if got := topChild(`C:\`, `C:\Users\lenovo\AppData\x.txt`); got != `C:\Users\` {
		t.Fatalf("topChild=%q", got)
	}
	if got := topChild(`C:\`, `C:\Windows\System32`); got != `C:\Windows\` {
		t.Fatalf("topChild=%q", got)
	}
	if got := topChild(`C:\`, `D:\x`); got != "" {
		t.Fatalf("跨盘应为空，实际 %q", got)
	}
}

func TestCanReuseChild(t *testing.T) {
	now := time.Now()
	cache := &scanCache{
		Root: `C:\`, TS: now.Unix(), FullTS: now.Unix(), Session: procSession,
		Children: map[string]*Scan{`c:\windows\`: {Total: 1}, `c:\users\`: {Total: 2}},
	}
	if !canReuseChild(cache, `C:\Windows\`, map[string]bool{}, now) {
		t.Fatal("无脏标记且非空应复用")
	}
	if canReuseChild(cache, `C:\Windows\`, map[string]bool{`c:\windows\`: true}, now) {
		t.Fatal("脏子树不应复用")
	}
	if canReuseChild(cache, `C:\ProgramData\`, map[string]bool{}, now) {
		t.Fatal("缓存里没有的子树不应复用")
	}
	// 超过 24 小时不可复用
	old := *cache
	old.TS = now.Add(-25 * time.Hour).Unix()
	if canReuseChild(&old, `C:\Windows\`, map[string]bool{}, now) {
		t.Fatal("过期缓存不应复用")
	}
}

func TestDirtyAndSession(t *testing.T) {
	clearDirty()
	markDirty(`C:\`, `C:\Users\x\file.txt`, false)
	dirty, all := dirtySnapshot()
	if all || !dirty[`c:\users\`] {
		t.Fatalf("应标记 C:\\Users\\，实际 %v all=%v", dirty, all)
	}
	markDirty(`C:\`, `C:\Users\a`, true)
	if _, all2 := dirtySnapshot(); !all2 {
		t.Fatal("重命名应让全部失效")
	}
	clearDirty()

	// 缓存会话不匹配 → 不可复用
	now := time.Now()
	c := &scanCache{Root: `C:\`, TS: now.Unix(), FullTS: now.Unix(), Session: "other-session",
		Children: map[string]*Scan{`c:\windows\`: {}}}
	if c.Session == procSession {
		t.Fatal("测试前提：会话应不同")
	}
}

func TestCacheRoundtrip(t *testing.T) {
	dir, _ := os.MkdirTemp(".", "cache-test")
	defer os.RemoveAll(dir)
	cfg := Config{DataDir: dir}
	c := &scanCache{Root: `C:\`, TS: 1, FullTS: 2, Session: "s",
		Children: map[string]*Scan{`c:\windows\`: {Total: 42}}}
	saveScanCache(cfg, c)
	got := loadScanCache(cfg)
	if got == nil || got.Children[`c:\windows\`].Total != 42 {
		t.Fatalf("缓存读写失败: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "scan_cache.json")); err != nil {
		t.Fatal("缓存文件不存在")
	}
}

// setScanFlags 临时改全局开关，用例结束自动还原，避免污染同包其它用例。
func setScanFlags(t *testing.T, mft, usn bool) {
	t.Helper()
	oldMft, oldUsn := useMftEngine, usnActive.Load()
	useMftEngine = mft
	usnActive.Store(usn)
	t.Cleanup(func() {
		useMftEngine = oldMft
		usnActive.Store(oldUsn)
	})
}

// stubAnchorReader 把「读卷上 USN 坐标」换成固定答案：单测不该碰真卷，
// 也不该要求测试进程有管理员权限。
func stubAnchorReader(t *testing.T, a usnAnchor, err error) {
	t.Helper()
	old := usnAnchorReader
	usnAnchorReader = func(string) (usnAnchor, error) { return a, err }
	t.Cleanup(func() { usnAnchorReader = old })
}

// stubDeltaReader 把「读 [from,to) 那段 USN 记录」换成固定答案：同样不碰真卷。
func stubDeltaReader(t *testing.T, records int, modifying uint32, err error) {
	t.Helper()
	old := usnDeltaReader
	usnDeltaReader = func(string, int64, int64, uint64) (int, uint32, error) {
		return records, modifying, err
	}
	t.Cleanup(func() { usnDeltaReader = old })
}

// MFT 成功后必须留下缓存——这是「管理员第二次扫描不再重读整张 $MFT」的前提。
func TestMftScanWritesCache(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), Root: `C:\`}
	anchor := usnAnchor{JournalID: 0x5150, First: 100, Next: 900}
	sc := &Scan{
		TS: time.Now().Unix(), Root: `C:\`, Files: 1234, Dirs: 56, Total: 789, Source: "mft",
		Cats: map[string]uint64{"temp": 700}, Dirs3: []DirEntry{{Path: `C:\Temp`, Bytes: 700}},
	}
	saveMftScanCache(cfg, sc, anchor)

	got := loadScanCache(cfg)
	if got == nil || got.Mft == nil {
		t.Fatalf("MFT 成功后应写下缓存，实际 %+v", got)
	}
	if got.Session != procSession {
		t.Fatalf("缓存会话应为当前进程，实际 %q", got.Session)
	}
	if !strings.EqualFold(got.Root, cfg.Root) {
		t.Fatalf("缓存根应为 %q，实际 %q", cfg.Root, got.Root)
	}
	if got.Mft.Files != 1234 || got.Mft.Total != 789 || got.Mft.Dirs != 56 {
		t.Fatalf("缓存内容不对：%+v", got.Mft)
	}
	if len(got.Children) != 0 {
		t.Fatalf("MFT 快照不该带子树缓存，实际 %d 个", len(got.Children))
	}
	// 锚点要跟着快照一起落盘，否则跨进程没有证据可用。
	if got.Usn != anchor {
		t.Fatalf("USN 锚点没落盘：期望 %+v，实际 %+v", anchor, got.Usn)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "scan_cache.json")); err != nil {
		t.Fatal("缓存文件不存在")
	}

	// 落盘的那份必须是深拷贝：调用方拿到 sc 之后还会改它，不能漏进缓存。
	sc.Cats["temp"] = 9999
	sc.Dirs3[0].Path = `C:\changed`
	if again := loadScanCache(cfg); again == nil || again.Mft.Cats["temp"] != 700 || again.Mft.Dirs3[0].Path != `C:\Temp` {
		t.Fatal("缓存和返回值共享了底层数组：改返回值污染了缓存")
	}

	// 失败/回落时不写半成品：没有结果就一个字都不落盘。
	failCfg := Config{DataDir: t.TempDir(), Root: `C:\`}
	saveMftScanCache(failCfg, nil, anchor)
	if _, err := os.Stat(filepath.Join(failCfg.DataDir, "scan_cache.json")); err == nil {
		t.Fatal("没有扫描结果时不该写缓存文件")
	}
}

// 只有 MFT 快照的缓存不能被当成"遍历缓存可复用"——否则会退化成逐子树全扫。
func TestWalkCacheUsableIgnoresMftOnlyCache(t *testing.T) {
	setScanFlags(t, true, true)
	now := time.Now()
	mftOnly := &scanCache{Root: `C:\`, TS: now.Unix(), FullTS: now.Unix(), Session: procSession,
		Mft: &Scan{Files: 10}}
	if walkCacheUsable(mftOnly, `C:\`, false, now) {
		t.Fatal("只有 MFT 快照的缓存不能算遍历缓存可用")
	}
	if c := cachedMftScan(mftOnly, `C:\`, false, nil, false, now); c == nil || c.Files != 10 {
		t.Fatal("MFT 快照本身应当可复用")
	}
	walk := &scanCache{Root: `C:\`, TS: now.Unix(), FullTS: now.Unix(), Session: procSession,
		Children: map[string]*Scan{`c:\windows\`: {}}}
	if !walkCacheUsable(walk, `C:\`, false, now) {
		t.Fatal("有子树的缓存应当可复用")
	}
	if c := cachedMftScan(walk, `C:\`, false, nil, false, now); c != nil {
		t.Fatal("没有 MFT 快照时不该返回结果")
	}
}

// 复用的前提必须和遍历缓存一样严：会话、根、时效、开关、脏集合，缺一不可。
func TestCachedMftScanInvalidation(t *testing.T) {
	setScanFlags(t, true, true)
	now := time.Now()
	base := func() *scanCache {
		return &scanCache{Root: `C:\`, TS: now.Unix(), FullTS: now.Unix(), Session: procSession,
			Mft: &Scan{Files: 10, Total: 100}}
	}
	if c := cachedMftScan(base(), `C:\`, false, map[string]bool{}, false, now); c == nil {
		t.Fatal("整卷无变更时应复用 MFT 快照")
	}
	if c := cachedMftScan(base(), `C:\`, false, map[string]bool{`c:\users\`: true}, false, now); c != nil {
		t.Fatal("有脏子树时不应复用（整卷快照拆不出子树）")
	}
	if c := cachedMftScan(base(), `C:\`, false, nil, true, now); c != nil {
		t.Fatal("重命名导致全部失效时不应复用")
	}
	if c := cachedMftScan(base(), `C:\`, true, nil, false, now); c != nil {
		t.Fatal("强制全扫时不应复用")
	}
	useMftEngine = false
	if c := cachedMftScan(base(), `C:\`, false, nil, false, now); c != nil {
		t.Fatal("--walk / --no-mft 时不应复用 MFT 快照")
	}
	useMftEngine = true
	// 跨进程：会话不同，而且缓存里没有锚点。就算此刻卷上日志"看起来很干净"，
	// 也没有任何证据能把这份快照和当下对上——必须重扫。
	stubAnchorReader(t, usnAnchor{JournalID: 0x5150, First: 100, Next: 900}, nil)
	other := base()
	other.Session = "other-session"
	if c := cachedMftScan(other, `C:\`, false, nil, false, now); c != nil {
		t.Fatal("跨进程但没有 USN 锚点时不该复用")
	}
	old := base()
	old.TS = now.Add(-7 * time.Hour).Unix()
	if c := cachedMftScan(old, `C:\`, false, nil, false, now); c != nil {
		t.Fatal("扫描时间超过 6 小时不应复用")
	}
	oldFull := base()
	oldFull.FullTS = now.Add(-25 * time.Hour).Unix()
	if c := cachedMftScan(oldFull, `C:\`, false, nil, false, now); c != nil {
		t.Fatal("对账基准超过 24 小时不应复用")
	}
	wrongRoot := base()
	if c := cachedMftScan(wrongRoot, `D:\`, false, nil, false, now); c != nil {
		t.Fatal("根不一致时不应复用")
	}
}

// (a) 跨进程：缓存里的 USN 锚点和卷上现在的位置对得上，就说明期间零写入，可以复用。
// 这是「`diskmon scan` 第二次变快」的关键——每次 scan 都是新进程，靠的就是这个。
func TestMftCacheCrossProcessReuse(t *testing.T) {
	setScanFlags(t, true, true)
	now := time.Now()
	anchor := usnAnchor{JournalID: 0x5150, First: 100, Next: 900}
	// 模拟「上一个进程」留下的缓存：会话一定对不上，只有锚点能当证据。
	prev := &scanCache{Root: `C:\`, TS: now.Unix(), FullTS: now.Unix(), Session: "some-other-process",
		Usn: anchor, Mft: &Scan{Files: 10, Total: 100}}
	if prev.Session == procSession {
		t.Fatal("测试前提：会话应当不同")
	}

	// 卷上日志坐标没动 → 零变更 → 复用。
	stubAnchorReader(t, anchor, nil)
	if sc := cachedMftScan(prev, `C:\`, false, nil, false, now); sc == nil || sc.Files != 10 {
		t.Fatal("跨进程：锚点一致且零事件时应复用整卷快照")
	}

	// 锚点之后多了记录，但全是 CLOSE——CLOSE 不改任何数字，这是 t7 放宽的那一步。
	stubAnchorReader(t, usnAnchor{JournalID: 0x5150, First: 100, Next: 905}, nil)
	stubDeltaReader(t, 5, 0, nil)
	if sc := cachedMftScan(prev, `C:\`, false, nil, false, now); sc == nil || sc.Files != 10 {
		t.Fatal("跨进程：锚点之后只有无改动记录（CLOSE）时应复用")
	}

	// 锚点之后有一条写记录 → 必须重扫。这是放宽后的安全底线。
	stubAnchorReader(t, usnAnchor{JournalID: 0x5150, First: 100, Next: 905}, nil)
	stubDeltaReader(t, 5, 0x00000002, nil)
	if sc := cachedMftScan(prev, `C:\`, false, nil, false, now); sc != nil {
		t.Fatal("跨进程：锚点之后有改动类记录时不应复用")
	}

	// 需要读记录却读不到（比如权限不够）→ 不知道，必须重扫。
	stubAnchorReader(t, usnAnchor{JournalID: 0x5150, First: 100, Next: 905}, nil)
	stubDeltaReader(t, 0, 0, fmt.Errorf("读 USN 记录失败"))
	if sc := cachedMftScan(prev, `C:\`, false, nil, false, now); sc != nil {
		t.Fatal("跨进程：读不到 USN 记录时必须重扫")
	}

	// 读不到 USN（没权限 / 日志没开）→ 不知道，必须重扫。
	stubAnchorReader(t, usnAnchor{}, fmt.Errorf("打开卷失败：需要管理员权限"))
	if sc := cachedMftScan(prev, `C:\`, false, nil, false, now); sc != nil {
		t.Fatal("跨进程：读不到 USN 时必须重扫")
	}

	// 根不是卷根（拿不到日志）→ 同样重扫。
	stubAnchorReader(t, anchor, nil)
	if sc := cachedMftScan(prev, `C:\Users\`, false, nil, false, now); sc != nil {
		t.Fatal("根不是卷根时不应复用")
	}
}

// (b) 锚点比对本身：只有「同一本日志、位置没动」才算零变更。
// 其余每一种不确定都必须拒绝，并且拒绝的理由说得出来。
func TestAnchorStillCleanRefusesUncertain(t *testing.T) {
	prev := usnAnchor{JournalID: 7, First: 100, Next: 900}
	cases := []struct {
		name string
		cur  usnAnchor
		want string // 期望理由里出现的关键词；空表示期望放行
	}{
		{"坐标完全一致", prev, ""},
		{"journalID 变了（日志被删掉重建）", usnAnchor{JournalID: 8, First: 100, Next: 900}, "重建"},
		{"USN 回退（截断/回绕）", usnAnchor{JournalID: 7, First: 50, Next: 400}, "回退"},
		{"锚点已滚出日志窗口", usnAnchor{JournalID: 7, First: 950, Next: 1200}, "滚出"},
		{"锚点之后有新记录", usnAnchor{JournalID: 7, First: 100, Next: 901}, "USN 记录"},
		{"当前坐标不可信", usnAnchor{}, "不可信"},
	}
	for _, tc := range cases {
		clean, why := anchorStillClean(prev, tc.cur)
		if tc.want == "" {
			if !clean {
				t.Fatalf("%s：应当放行，实际拒绝（%s）", tc.name, why)
			}
			continue
		}
		if clean {
			t.Fatalf("%s：必须拒绝复用", tc.name)
		}
		if !strings.Contains(why, tc.want) {
			t.Fatalf("%s：拒绝理由应当提到 %q，实际 %q", tc.name, tc.want, why)
		}
	}
	// 缓存里的锚点自己不完整（老版本写的、或写了一半）——同样不许复用。
	if clean, why := anchorStillClean(usnAnchor{}, prev); clean || !strings.Contains(why, "不完整") {
		t.Fatalf("锚点不完整时必须拒绝，实际 clean=%v why=%q", clean, why)
	}
}

// 深拷贝：改副本不能影响原件。缓存的快照会被反复取出来交出去，
// 这里有一条共享底层数组的缝隙，就可能让"复用"变成"复用被污染的数字"。
func TestCloneScanIsDeep(t *testing.T) {
	orig := &Scan{
		Files: 3, Total: 300,
		Cats:       map[string]uint64{"temp": 1},
		CatsShared: map[string]uint64{"temp": 2},
		Owners:     map[string]uint64{"o": 3},
		Protected:  map[string]uint64{"p": 4},
		DayWrites:  map[string]uint64{"d": 5},
		Dirs3:      []DirEntry{{Path: `C:\a`, Bytes: 1}},
		Deep:       []DirEntry{{Path: `C:\b`, Bytes: 2}},
		TopFiles:   []FileEntry{{Path: `C:\c`, Bytes: 3}},
	}
	cp := cloneScan(orig)
	if cp == nil {
		t.Fatal("cloneScan(nil 以外) 不该返回 nil")
	}
	cp.Cats["temp"] = 99
	cp.CatsShared["temp"] = 99
	cp.Owners["o"] = 99
	cp.Protected["p"] = 99
	cp.DayWrites["d"] = 99
	cp.Dirs3[0].Path = "改了"
	cp.Deep[0].Bytes = 99
	cp.TopFiles[0].Path = "改了"
	cp.Deep = append(cp.Deep, DirEntry{Path: `C:\new`})

	if orig.Cats["temp"] != 1 || orig.CatsShared["temp"] != 2 || orig.Owners["o"] != 3 ||
		orig.Protected["p"] != 4 || orig.DayWrites["d"] != 5 {
		t.Fatal("map 没深拷贝：改副本影响到了原件")
	}
	if orig.Dirs3[0].Path != `C:\a` || orig.Deep[0].Bytes != 2 || orig.TopFiles[0].Path != `C:\c` || len(orig.Deep) != 1 {
		t.Fatal("slice 没深拷贝：改副本影响到了原件")
	}
	if cloneScan(nil) != nil {
		t.Fatal("cloneScan(nil) 应当是 nil")
	}
}
