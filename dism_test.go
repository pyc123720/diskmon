package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// mustDecode 把 GBK 原始字节解成字符串，测试里要拿它拼输入。
func mustDecode(b []byte) string { return decodeConsoleOutput(b) }

// 本机真实抓到的 DISM 输出（非管理员，GBK/936 原始字节，逐字节照抄）。
// 来源：2026-10-29 20:03 在本机执行
//   cmd /c "C:\Windows\System32\Dism.exe /Online /Cleanup-Image /AnalyzeComponentStore"
// 退出码非 0，输出即为下面这 69 个字节。
// 注意：它是 GBK 不是 UTF-8——这正是 decodeConsoleOutput 存在的理由。
var realDismNotElevated = []byte{
	0x0d, 0x0a, 0xb4, 0xed, 0xce, 0xf3, 0x3a, 0x20, 0x37, 0x34, 0x30, 0x0d, 0x0a, 0x0d, 0x0a,
	0xd0, 0xe8, 0xd2, 0xaa, 0xcc, 0xe1, 0xc9, 0xfd, 0xc8, 0xa8, 0xcf, 0xde, 0xb2, 0xc5, 0xc4,
	0xdc, 0xd4, 0xcb, 0xd0, 0xd0, 0x20, 0x44, 0x49, 0x53, 0x4d, 0xa1, 0xa3, 0x0d, 0x0a, 0xca,
	0xb9, 0xd3, 0xc3, 0xcc, 0xe1, 0xc9, 0xfd, 0xb5, 0xc4, 0xc3, 0xfc, 0xc1, 0xee, 0xcc, 0xe1,
	0xca, 0xbe, 0xb7, 0xfb, 0xcd, 0xea, 0xb3, 0xc9, 0xd5, 0xe2, 0xd0, 0xa9, 0xc8, 0xce, 0xce,
	0xf1, 0xa1, 0xa3, 0x0d, 0x0a,
}

// 中文成功输出的**结构样本**。
//
// 诚实说明：这台机器不是管理员，`/AnalyzeComponentStore` 跑不出成功输出
// （上面那个 740 就是本机的真实答复），所以我拿不到一次真实的中文成功样本。
// 这份夹具的用途只有一个：钉住「字段顺序 + 等式校验」这条解析路径。
//   - 各行的措辞按 DISM 中文版的实际用词写，冒号、缩进、百分比进度条都按真实格式；
//   - 数字是测试填的，模拟微软文档里的那组例子（实际 8.86 GB = 6.27 + 2.58 + 0.01）。
// 它不是「某次真实运行的输出」，报告里我也这么说了。
const dismChineseShape = `部署映像服务和管理工具
版本: 10.0.19041.844

映像版本: 10.0.19041.844

[===========                20.0%                          ]
[==========================100.0%==========================]

组件存储(WinSxS)分析

Windows 资源管理器报告的组件存储大小 : 8.97 GB
组件存储的实际大小 : 8.86 GB
    与 Windows 共享 : 6.27 GB
    备份和禁用的功能 : 2.58 GB
    缓存和临时数据 : 0.01 GB
上次清理日期 : 2024-05-24 10:08:07
可回收的包 : 12 个包
建议清理组件存储 : 是

操作已成功完成。
`

func TestParseComponentStoreChineseShape(t *testing.T) {
	cs, err := parseComponentStore(dismChineseShape)
	if err != nil {
		t.Fatalf("中文结构样本应当解析成功：%v", err)
	}
	if cs.ExplorerBytes != 8_970_000_000 && cs.ExplorerBytes < 8<<30 {
		t.Fatalf("资源管理器口径不对：%d", cs.ExplorerBytes)
	}
	// 8.86 GB（GiB 口径）——只校验量级与相对关系，不锁死 ulp。
	if cs.ActualBytes < 8<<30 || cs.ActualBytes > 9<<30 {
		t.Fatalf("实际大小量级不对：%d", cs.ActualBytes)
	}
	if cs.SharedBytes == 0 || cs.BackupBytes == 0 {
		t.Fatalf("共享/备份没解析出来：%+v", cs)
	}
	if cs.ReclaimablePkgs != 12 {
		t.Fatalf("可回收包数应当 12，实际 %d", cs.ReclaimablePkgs)
	}
	if cs.RecommendClean != 1 {
		t.Fatalf("建议清理应解析为 1，实际 %d", cs.RecommendClean)
	}
}

// 同一个解析器必须也吃得下英文和「全角冒号 + 逗号小数点」——
// 这条不依赖任何语言的标签，只靠「数字 + ASCII 单位」与字段顺序。
func TestParseComponentStoreIsLanguageAgnostic(t *testing.T) {
	english := `Deployment Image Servicing and Management tool
Version: 10.0.19041.844

Component Store (WinSxS) Analysis

Windows Explorer Reported Size of Component Store : 8.97 GB
Actual Size of Component Store : 8.86 GB
    Shared with Windows : 6.27 GB
    Backups and Disabled Features : 2.58 GB
    Cache and Temporary Data : 0.01 GB
Date of Last Cleanup : 2024-05-24 10:08:07
Number of Reclaimable Packages : 0
Component Store Cleanup Recommended : No
`
	cs, err := parseComponentStore(english)
	if err != nil {
		t.Fatalf("英文样本应当解析成功：%v", err)
	}
	if cs.ReclaimablePkgs != 0 || cs.RecommendClean != 0 {
		t.Fatalf("英文样本解析结果不对：%+v", cs)
	}

	// 全角冒号 + 逗号小数点（德语/法语风格）+ 缩进
	de := "组件存储的实际大小 ： 8,86 GB\n与 Windows 共享 ： 6,27 GB\n备份 ： 2,58 GB\n缓存 ： 0,01 GB\n可回收的包 ： 3 个包\n"
	if cs, err := parseComponentStore(de); err != nil || cs.ReclaimablePkgs != 3 {
		t.Fatalf("全角冒号/逗号小数点应当能解析：%v %+v", err, cs)
	}
}

// 解析失败必须响亮：每条都给出「为什么」和原文线索，绝不返回 0 蒙混过关。
func TestParseComponentStoreRefusesUnparsable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"完全空输出", "", "没有任何输出"},
		{"只有进度条没有字段", "[====100.0%====]\n操作已成功完成。", "尺寸口径"},
		{"字段错位（实际 ≠ 共享+备份+缓存）", "尺寸 : 8.97 GB\n实际 : 8.86 GB\n共享 : 1.00 GB\n备份 : 1.00 GB\n缓存 : 1.00 GB\n可回收的包 : 0 个包\n", "字段对不上"},
		{"缺可回收的包那一行", "尺寸 : 8.97 GB\n实际 : 8.86 GB\n共享 : 6.27 GB\n备份 : 2.58 GB\n缓存 : 0.01 GB\n", "可回收的包"},
		{"实际大小为 0", "尺寸 : 8.97 GB\n实际 : 0 GB\n", "不可信"},
		{"DISM 报错输出", string(mustDecode(realDismNotElevated)), "尺寸口径"},
	}
	for _, tc := range cases {
		cs, err := parseComponentStore(tc.in)
		if err == nil {
			t.Fatalf("%s：应当报解析失败", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s：错误信息应当提到 %q，实际 %q", tc.name, tc.want, err.Error())
		}
		if cs.OK() {
			t.Fatalf("%s：解析失败时 OK() 必须是 false", tc.name)
		}
	}
}

// 三条失败路径（非管理员 / 退出码非 0 / 超时）都要有明确返回值，且不 panic。
func TestAnalyzeComponentStoreFailurePaths(t *testing.T) {
	withStubs := func(t *testing.T, elevated bool, out []byte, code int, runErr error) {
		t.Helper()
		oldRun, oldElev := dismRunner, elevatedFunc
		dismRunner = func(time.Duration) ([]byte, int, error) { return out, code, runErr }
		elevatedFunc = func() bool { return elevated }
		t.Cleanup(func() { dismRunner, elevatedFunc = oldRun, oldElev })
	}

	// 非管理员：连进程都不该起。
	withStubs(t, false, nil, 0, nil)
	cs := analyzeComponentStore()
	if cs.OK() || !strings.Contains(cs.Err, "管理员") {
		t.Fatalf("非管理员必须明确拒绝，实际 %+v", cs)
	}

	// 本机真实输出 + 退出码非 0：原文要带回，错误要说得清。
	withStubs(t, true, realDismNotElevated, 740, nil)
	cs = analyzeComponentStore()
	if cs.OK() {
		t.Fatal("退出码非 0 时不许当成成功")
	}
	if !strings.Contains(cs.Err, "740") {
		t.Fatalf("错误应当带上退出码，实际 %q", cs.Err)
	}
	if gbkFixtureUsable(t) && !strings.Contains(cs.Raw, "错误") {
		t.Fatalf("原文片段应当能解码出中文，实际 %q", cs.Raw)
	}

	// 超时：明确说超时，不许假装成功、也不许给 0。
	withStubs(t, true, nil, -1, fmt.Errorf("DISM 超过 3m0s 未返回，已中止"))
	cs = analyzeComponentStore()
	if cs.OK() || !strings.Contains(cs.Err, "超过") {
		t.Fatalf("超时必须明确报错，实际 %+v", cs)
	}

	// 成功：字段落位 + 来源标 DISM。
	withStubs(t, true, []byte(dismChineseShape), 0, nil)
	cs = analyzeComponentStore()
	if !cs.OK() {
		t.Fatalf("成功路径不该 OK()=false：%+v", cs)
	}
	if cs.Src != "dism" || cs.ReclaimablePkgs != 12 {
		t.Fatalf("结果字段不对：%+v", cs)
	}
}

// 展示必须写清「来源是 DISM，不是我们的估算」；失败时要把原文摊给用户看。
func TestComponentStoreHTMLSaysDism(t *testing.T) {
	cs, err := parseComponentStore(dismChineseShape)
	if err != nil {
		t.Fatal(err)
	}
	cs.Cost = 92
	h := componentStoreHTML(cs)
	for _, want := range []string{"DISM", "权威口径", "组件库实际大小", "可回收的包", "建议清理"} {
		if !strings.Contains(h, want) {
			t.Fatalf("展示缺少 %q：%s", want, h)
		}
	}
	if strings.Contains(h, "估算：") {
		t.Fatalf("不该把 DISM 的数字当成本工具的估算：%s", h)
	}

	bad := ComponentStore{Err: "解析不了：没找到尺寸口径", Raw: "错误: 740\n需要提升权限"}
	hb := componentStoreHTML(bad)
	if !strings.Contains(hb, "没有拿到可信结果") || !strings.Contains(hb, "错误: 740") {
		t.Fatalf("失败展示必须显式说明并附原文：%s", hb)
	}
}

// 命令行版同样要写清来源，失败时同样要说人话。
func TestPlainComponentStoreSaysDism(t *testing.T) {
	cs, err := parseComponentStore(dismChineseShape)
	if err != nil {
		t.Fatal(err)
	}
	cs.Cost = 61
	out := plainComponentStore(cs)
	for _, want := range []string{"DISM", "权威口径", "组件存储实际大小", "可回收的包", "建议清理"} {
		if !strings.Contains(out, want) {
			t.Fatalf("命令行输出缺少 %q：\n%s", want, out)
		}
	}
	bad := plainComponentStore(ComponentStore{Err: "需要管理员权限"})
	if !strings.Contains(bad, "没有拿到可信结果") || !strings.Contains(bad, "需要管理员权限") {
		t.Fatalf("失败时必须说清楚：%s", bad)
	}
}

// DISM 往管道里写的是 ANSI 代码页字节，不是 UTF-8：这条夹具就是本机抓到的 GBK 原文。
// gbkFixtureUsable 说明这段 GBK 夹具在当前系统上能不能被解对。
//
// decodeConsoleOutput 走的是 CP_ACP（系统当前 ANSI 代码页），不是硬编码 936——
// 因为 DISM 往管道里写的就是控制台代码页的字节。本机 ACP=936 时夹具有中文；
// GitHub 的英文 Windows 镜像 ACP=1252，同样的字节会被当单字节解码，出来是 "´íÎó"。
//
// 这不是代码错，是夹具对系统有要求。依赖它的断言在别的代码页上应当跳过，
// 并把原因写进日志——不然 CI 红了看上去像功能坏了。
func gbkFixtureUsable(t *testing.T) bool {
	t.Helper()
	cp, _, _ := procGetACP.Call()
	if cp != 936 {
		t.Logf("这台系统的 ANSI 代码页是 %d，不是 936；GBK 夹具在这里解不出中文，跳过依赖它的断言", cp)
		return false
	}
	return true
}
func TestDecodeConsoleOutputGBK(t *testing.T) {
	if !gbkFixtureUsable(t) {
		t.Skip("夹具是 GBK 字节，这台系统的代码页不是 936")
	}
	got := decodeConsoleOutput(realDismNotElevated)
	for _, want := range []string{"错误: 740", "需要提升权限"} {
		if !strings.Contains(got, want) {
			t.Fatalf("GBK 解码后应当包含 %q，实际 %q", want, got)
		}
	}
}
