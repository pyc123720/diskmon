package main

// DISM 组件库分析（只读、按需）。
//
// 为什么要有这东西：我们自己扫出来的 C:\Windows\WinSxS 是「按路径求和」，
// 硬链接会被重复计入——微软文档自己的例子是资源管理器 4.98 GB、实际 507 MB。
// 硬链接去重能给出 Unique/Shared/Total 三口径，但「组件库还能回收多少」只有 DISM 知道。
// 所以这一层不自己算，只负责**如实转述** DISM 的权威口径。
//
// 三条纪律：
//  1. 只在用户明确要求时才跑（子命令 / 页面按钮）。扫描主路径一行都不碰：
//     它要 1–2 分钟、要管理员、会明显占 CPU。
//  2. 解析不出来就说解析不出来，附原文片段。绝不给 0、绝不返回上次的值。
//  3. 非管理员 / DISM 不存在 / 超时 / 退出码非 0 —— 都有明确返回值，不 panic。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"
)

// ComponentStore 是 DISM 报出来的组件库口径。零值没有任何意义，
// 调用方必须看 Err 字段——这里刻意不给"默认 0 但看起来正常"的形态。
type ComponentStore struct {
	AtTS int64 // 什么时候跑的

	// 尺寸口径（字节）。前两项是 DISM 的主口径，后三项是它的细分。
	ExplorerBytes uint64 // 「Windows 资源管理器报告的大小」
	ActualBytes   uint64 // 「组件存储的实际大小」
	SharedBytes   uint64 // 与 Windows 共享
	BackupBytes   uint64 // 备份与禁用的功能
	CacheBytes    uint64 // 缓存与临时数据

	ReclaimablePkgs int  // 可回收的包数量
	RecommendClean  int8 // 1=建议清理 0=不建议 -1=DISM 没给明确结论

	Raw  string // 原始输出（截断后），解析失败时给用户自己看
	Err  string // 非空表示这次没拿到可信结果
	Src  string // 固定 "dism"
	Cost float64
}

// OK 表示这份结果可信。任何解析/执行问题都为 false。
func (c ComponentStore) OK() bool { return c.Err == "" && c.ActualBytes > 0 }

// dismAnalyzeArgs 是唯一一处调用参数。只读：不带 /StartComponentCleanup。
var dismAnalyzeArgs = []string{"/Online", "/Cleanup-Image", "/AnalyzeComponentStore"}

// dismTimeout 给足余量：官方说 1–2 分钟，慢机器上会到 3 分钟。
const dismTimeout = 3 * time.Minute

// dismPath 返回 Dism.exe 的绝对路径；找不到就返回 "dism" 交给 PATH。
func dismPath() string {
	if w := os.Getenv("WINDIR"); w != "" {
		p := filepath.Join(w, "System32", "Dism.exe")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return "dism"
}

// 两个接缝：测试不该真的去跑 DISM，也不该要求测试进程是管理员。
var (
	dismRunner   = runDism
	elevatedFunc = isElevated
)

// runDism 跑一次只读分析，返回合并后的输出与退出码。
func runDism(timeout time.Duration) (out []byte, exitCode int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, dismPath(), dismAnalyzeArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	b, runErr := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		// 超时是明确的失败，不是"大概没结果"。调用方必须告诉用户重跑。
		return b, -1, fmt.Errorf("DISM 超过 %s 未返回，已中止", timeout)
	}
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		return b, ee.ExitCode(), nil // 退出码非 0 交给上层判定，原文照样带回去
	}
	if runErr != nil {
		return b, -1, runErr
	}
	return b, 0, nil
}

// analyzeComponentStore 是按需入口：非管理员直接拒绝（不去 spawn 一个必然失败的进程），
// 否则跑一次 DISM 并解析。无论哪条失败路径，返回的结构体里 Err 都有话说。
func analyzeComponentStore() ComponentStore {
	start := time.Now()
	cs := ComponentStore{AtTS: start.Unix(), Src: "dism", RecommendClean: -1}

	if !elevatedFunc() {
		cs.Err = "需要管理员权限：DISM 的组件库分析必须以提升的权限运行（预计 1–2 分钟）"
		return cs
	}
	out, code, err := dismRunner(dismTimeout)
	cs.Raw = excerpt(string(decodeConsoleOutput(out)), 2000)
	cs.Cost = time.Since(start).Seconds()
	if err != nil {
		cs.Err = err.Error()
		return cs
	}
	if code != 0 {
		cs.Err = fmt.Sprintf("DISM 退出码 %d：%s", code, firstMeaningfulLine(cs.Raw))
		return cs
	}
	parsed, perr := parseComponentStore(cs.Raw)
	if perr != nil {
		// 解析不了就是解析不了：不猜、不给 0、不拿旧值糊弄。
		cs.Err = perr.Error()
		return cs
	}
	parsed.AtTS, parsed.Src, parsed.Cost, parsed.Raw = cs.AtTS, cs.Src, cs.Cost, cs.Raw
	return parsed
}

// ---------- 解析 ----------
//
// 锚点为什么不受语言影响：
//   - 尺寸行永远是「标签 : 数字 单位」，单位是 ASCII 的 KB/MB/GB/TB（各语言版本都不翻译），
//     冒号可能是半角 ':' 也可能是全角 '：'，两种都收；
//   - 数字用 、 或 . 作小数点（法语/德语版本），两种都认；
//   - 行标签被**丢弃**，只按 DISM 输出里固定的字段顺序取（资源管理器 → 实际 → 共享 → 备份 → 缓存）。
//     顺序是 DISM 自己的输出代码定的，不随语言变。
//   - 顺序法有一个风险：某个语言版本多一行，后面的就会错位。所以收尾用一条
//     「实际 ≈ 共享+备份+缓存」的等式校验——对不上就报解析失败，绝不带错数出去。
//
// 唯一依赖语言的是「可回收的包」和「建议清理」两行，它们没有 ASCII 单位可依；
// 这两处做成双语匹配 + 三态结论，匹配不到就明确报错/报未知。

// sizeLine 匹配「任意标签 : 数字 单位」，单位只认 ASCII 的字节量级。
var sizeLine = regexp.MustCompile(`(?i)^\s*([^:：]*?)\s*[:：]\s*([0-9]+(?:[.,][0-9]+)?)\s*(TB|GB|MB|KB|B|bytes|字节)\s*$`)

// pkgLine 匹配「可回收的包」这一行的**数字**。中文与英文两边都认；
// 中文那侧的措辞在不同版本里有细微差别（"可回收的包" / "可回收的包数"），
// 所以只锚定「可回收」与「包」两个字，中间允许任意字符。
var pkgLine = regexp.MustCompile(`(?i)(?:可回收[^\d\n]{0,8}?(\d+)|\bnumber of reclaimable packages\b[^\d\n]{0,8}?(\d+)|(\d+)[^\d\n]{0,4}?个包)`)

// recommendLine 匹配「建议清理」的结论。中文 DISM 有的版本输出 "建议 : 应清理组件存储"、
// 有的输出 "建议清理组件存储 : 是"，英文是 "Component Store Cleanup Recommended : No"。
// 三态：明确是/否就作答，只有建议字样没有布尔值就标未知（-1）——不替 DISM 下结论。
var recommendLine = regexp.MustCompile(`(?i)(?:建议[^\n]*?[:：]\s*(是|否)|cleanup recommended[^\n]*?[:：]\s*(yes|no))`)

func parseComponentStore(raw string) (ComponentStore, error) {
	cs := ComponentStore{Src: "dism", RecommendClean: -1}
	if strings.TrimSpace(raw) == "" {
		return failParse(cs, errors.New("DISM 没有任何输出，无法解析"))
	}
	text := normalizeNewlines(raw)

	var sizes []uint64
	var sizeTexts []string
	for _, line := range strings.Split(text, "\n") {
		m := sizeLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v, err := parseSize(m[2], m[3])
		if err != nil {
			return failParse(cs, fmt.Errorf("尺寸行读不懂（%q）：%v", strings.TrimSpace(line), err))
		}
		sizes = append(sizes, v)
		sizeTexts = append(sizeTexts, strings.TrimSpace(line))
	}
	if len(sizes) < 2 {
		return failParse(cs, fmt.Errorf("没找到 DISM 的尺寸口径（只认到 %d 行「标签: 数字 单位」），解析不了", len(sizes)))
	}
	// 顺序固定：0=资源管理器 1=实际 2=共享 3=备份 4=缓存。
	cs.ExplorerBytes, cs.ActualBytes = sizes[0], sizes[1]
	if len(sizes) > 2 {
		cs.SharedBytes = sizes[2]
	}
	if len(sizes) > 3 {
		cs.BackupBytes = sizes[3]
	}
	if len(sizes) > 4 {
		cs.CacheBytes = sizes[4]
	}
	if cs.ActualBytes == 0 {
		return failParse(cs, fmt.Errorf("组件存储实际大小为 0，不可信（原文：%s）", sizeTexts[1]))
	}
	// 等式校验：实际大小 ≈ 共享 + 备份 + 缓存。对不上说明字段错位了，
	// 这时候宁可报错，也不能把错位的数字当成口径发出去。
	if len(sizes) >= 5 {
		sum := cs.SharedBytes + cs.BackupBytes + cs.CacheBytes
		if !withinTolerance(cs.ActualBytes, sum) {
			return failParse(cs, fmt.Errorf("字段对不上：实际 %d 字节，但共享+备份+缓存 = %d 字节（可能 DISM 版本换了格式）",
				cs.ActualBytes, sum))
		}
	}

	if m := pkgLine.FindStringSubmatch(text); m != nil {
		num := m[1]
		if num == "" {
			num = m[2]
		}
		if num == "" {
			num = m[3]
		}
		n, err := strconv.Atoi(num)
		if err != nil {
			return failParse(cs, fmt.Errorf("可回收包数读不懂：%q", num))
		}
		cs.ReclaimablePkgs = n
	} else {
		return failParse(cs, errors.New("没找到「可回收的包」这一行（可能需要更完整的 DISM 输出）"))
	}

	if m := recommendLine.FindStringSubmatch(strings.ToLower(text)); m != nil {
		v := m[1]
		if v == "" {
			v = m[2]
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "是", "yes":
			cs.RecommendClean = 1
		case "否", "no":
			cs.RecommendClean = 0
		}
	}
	return cs, nil
}

// failParse 让「解析失败」这件事落在结构体自己身上：调用方哪怕只看返回值，
// 也不会把一个半成品当成结果（OK() 必须是 false）。
func failParse(cs ComponentStore, err error) (ComponentStore, error) {
	cs.Err = err.Error()
	return cs, err
}

// withinTolerance 判断两个字节数是否"实质相等"：DISM 的四舍五入到 0.01 GB，
// 允许 1% 误差足够宽，也足够窄到能抓住字段错位。
func withinTolerance(actual, sum uint64) bool {
	if actual == 0 {
		return false
	}
	diff := int64(actual) - int64(sum)
	if diff < 0 {
		diff = -diff
	}
	return diff*100 <= int64(actual)
}

// parseSize 把 "12.34" + "GB" 换成字节。逗号小数点也认（部分语言版本）。
func parseSize(num, unit string) (uint64, error) {
	f, err := strconv.ParseFloat(strings.ReplaceAll(num, ",", "."), 64)
	if err != nil {
		return 0, err
	}
	var mul float64
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case "TB":
		mul = 1 << 40
	case "GB":
		mul = 1 << 30
	case "MB":
		mul = 1 << 20
	case "KB":
		mul = 1 << 10
	case "B", "BYTES", "字节":
		mul = 1
	default:
		return 0, fmt.Errorf("不认识的单位 %q", unit)
	}
	return uint64(f*mul + 0.5), nil
}

// ---------- 输出解码 ----------
//
// DISM 往管道里写的是**控制台 ANSI 代码页**的字节（中文系统上是 GBK/936），
// 不是 UTF-8。直接当 UTF-8 读会把「可回收的包」读成乱码，标签就再也匹配不上。
// 这里用 MultiByteToWideChar 走系统自己的 ANSI 代码页，避免手写码表。
var (
	procMultiByteToWideChar = kernel32.NewProc("MultiByteToWideChar")
	procGetACP              = kernel32.NewProc("GetACP")
)

func decodeConsoleOutput(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	// 先试着当 UTF-8：GBK 的中文字节流几乎不可能是合法 UTF-8（双字节首字节 + 被当成
	// 续字节的高位字符），所以这一步能把「已经是 UTF-8」和「本地代码页字节」分开。
	// 若某段 GBK 恰好也是合法 UTF-8，我们只会解出错的字 —— 那时解析会明确报错，
	// 不会静默给出错的数字。
	if utf8.Valid(b) {
		return string(b)
	}
	cp, _, _ := procGetACP.Call()
	if cp == 65001 {
		return string(b)
	}
	// CP_ACP = 0，让系统用当前 ANSI 代码页（zh-CN 是 936）。
	n, _, _ := procMultiByteToWideChar.Call(0, 0,
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0, 0)
	if n == 0 {
		return string(b) // 解不出来就原样返回，至少不比乱码更糟
	}
	w := make([]uint16, int(n))
	r, _, _ := procMultiByteToWideChar.Call(0, 0,
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)),
		uintptr(unsafe.Pointer(&w[0])), n)
	if r == 0 {
		return string(b)
	}
	return syscall.UTF16ToString(w)
}

// ---------- 展示 ----------
//
// 结果挂在 WinSxS 那一类旁边，必须写清来源是 DISM、不是我们的估算。
// 这个函数只产出一段 HTML，页面自己的按钮/路由由 UI 那边接（本文件不碰路由）。
func componentStoreHTML(cs ComponentStore) string {
	if !cs.OK() {
		msg := cs.Err
		if msg == "" {
			msg = "没有结果"
		}
		h := `<div class="warn">DISM 组件库分析：<b>没有拿到可信结果</b><br>` + htmlEscape(msg)
		if cs.Raw != "" {
			h += `<details style="margin-top:8px"><summary>DISM 原始输出</summary><pre style="white-space:pre-wrap">` +
				htmlEscape(cs.Raw) + `</pre></details>`
		}
		return h + `</div>`
	}
	rec := "DISM 未给出明确结论"
	if cs.RecommendClean == 1 {
		rec = "建议清理"
	} else if cs.RecommendClean == 0 {
		rec = "不建议清理"
	}
	var b strings.Builder
	b.WriteString(`<div class="muted" style="margin-top:6px">来源：<b>DISM</b>（权威口径，非本工具估算）`)
	b.WriteString(fmt.Sprintf("　分析耗时 %.0fs", cs.Cost))
	b.WriteString(`</div><ul style="margin:6px 0 0 18px">`)
	b.WriteString(fmt.Sprintf(`<li>组件库实际大小：<b>%s</b>（资源管理器口径 %s）</li>`,
		human(cs.ActualBytes), human(cs.ExplorerBytes)))
	b.WriteString(fmt.Sprintf(`<li>其中 共享 %s · 备份 %s · 缓存临时 %s</li>`,
		human(cs.SharedBytes), human(cs.BackupBytes), human(cs.CacheBytes)))
	b.WriteString(fmt.Sprintf(`<li>可回收的包：<b>%d</b> 个　%s</li>`, cs.ReclaimablePkgs, rec))
	b.WriteString(`</ul>`)
	return b.String()
}

// plainComponentStore 是命令行版本的同一条信息。同样必须写清来源。
func plainComponentStore(cs ComponentStore) string {
	var b strings.Builder
	if !cs.OK() {
		b.WriteString("DISM 组件库分析：没有拿到可信结果\n  原因：" + cs.Err + "\n")
		if cs.Raw != "" {
			b.WriteString("  DISM 原始输出：\n" + cs.Raw + "\n")
		}
		return b.String()
	}
	rec := "DISM 未给出明确结论"
	if cs.RecommendClean == 1 {
		rec = "建议清理"
	} else if cs.RecommendClean == 0 {
		rec = "不建议清理"
	}
	b.WriteString("组件库（WinSxS）DISM 权威口径\n")
	b.WriteString(fmt.Sprintf("  组件存储实际大小 : %s（资源管理器口径 %s）\n", human(cs.ActualBytes), human(cs.ExplorerBytes)))
	b.WriteString(fmt.Sprintf("  共享 %s · 备份 %s · 缓存临时 %s\n", human(cs.SharedBytes), human(cs.BackupBytes), human(cs.CacheBytes)))
	b.WriteString(fmt.Sprintf("  可回收的包 : %d 个　%s\n", cs.ReclaimablePkgs, rec))
	b.WriteString(fmt.Sprintf("  来源：DISM（权威口径，非本工具估算）· 分析耗时 %.0fs\n", cs.Cost))
	return b.String()
}

// ---------- 小工具 ----------

func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// excerpt 截断原文，保留尾巴——DISM 的错误总是在最后几行。
func excerpt(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "…(前略)…\n" + s[len(s)-max:]
}

// firstMeaningfulLine 挑一条能给用户看的错误行：跳过空行。
func firstMeaningfulLine(s string) string {
	for _, line := range strings.Split(normalizeNewlines(s), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return "（无输出）"
}
