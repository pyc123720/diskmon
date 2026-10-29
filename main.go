package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"
	"unsafe"
)

// version 由 build.ps1 用 -ldflags -X main.version=<tag> 注入；直接 go build 时是 dev。
var version = "dev"

var (
	pGetProcessMemoryInfo = syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo")
	pGetCurrentProcess    = kernel32.NewProc("GetCurrentProcess")
)

type processMemoryCounters struct {
	Cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

func memMB() float64 {
	var pmc processMemoryCounters
	pmc.Cb = uint32(unsafe.Sizeof(pmc))
	h, _, _ := pGetCurrentProcess.Call()
	r, _, _ := pGetProcessMemoryInfo.Call(h, uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.Cb))
	if r == 0 {
		return 0
	}
	return float64(pmc.WorkingSetSize) / 1024 / 1024
}

func printStatus(cfg Config, st *Store) {
	v, _ := volumeStats(cfg.Root)
	fmt.Printf("[%s] C: 已用 %.2f GB / %.2f GB，剩余 %.2f GB (%.1f%%)\n",
		levelText(levelOf(v, cfg)), gb(v.Used), gb(v.Total), gb(v.Free),
		float64(v.Free)/float64(v.Total)*100)
	eng := st.Engine()
	state := "未运行"
	if eng.Running {
		state = "运行中"
	}
	fmt.Printf("引擎：%s（%s）\n", eng.Engine, state)
	if sc := st.GetScan(); sc != nil {
		fmt.Printf("最近扫描：%s，%d 文件，%s，耗时 %.0fs\n",
			time.Unix(sc.TS, 0).Format("2006-01-02 15:04"), sc.Files, human(sc.Total), sc.Duration)
	}
	if a := st.LastAlert(); a != "" {
		fmt.Println("最近告警：", a)
	}
	fmt.Printf("自身内存：%.0f MB\n", memMB())
}

func probeJSON(cfg Config, st *Store) string {
	v, _ := volumeStats(cfg.Root)
	eng := st.Engine()
	deltas := []DayDelta{}
	if sc := st.GetScan(); sc != nil {
		deltas = sortedDayWrites(sc.DayWrites)
	}
	f := forecastFrom(v.Free, deltas, "mtime")
	payload := map[string]interface{}{
		"free_gb":        gb(v.Free),
		"free_pct":       float64(v.Free) / float64(v.Total) * 100,
		"level":          levelOf(v, cfg),
		"days":           f.DaysP50,
		"engine":         eng.Engine,
		"engine_running": eng.Running,
		"last_alert":     st.LastAlert(),
		"rss_mb":         memMB(),
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

var pAttachConsole = kernel32.NewProc("AttachConsole")
var pGetStdHandle = kernel32.NewProc("GetStdHandle")

// GUI 子系统的 exe 默认没有控制台；带参数运行时挂到父控制台，CLI 输出才看得见
func setupConsole() {
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	pAttachConsole.Call(attachParentProcess)
	for _, id := range []uintptr{^uintptr(11), ^uintptr(12)} { // STD_OUTPUT_HANDLE, STD_ERROR_HANDLE
		h, _, _ := pGetStdHandle.Call(id)
		if h == 0 || h == ^uintptr(0) {
			continue
		}
		f := os.NewFile(h, "console")
		if id == ^uintptr(11) {
			os.Stdout = f
		} else {
			os.Stderr = f
		}
	}
}

func main() {
	cfg := loadConfig()
	setLiveConfig(cfg)
	st := openStore(cfg)
	cmd := "tray"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if cmd != "tray" && cmd != "mcp" {
		setupConsole() // mcp 用 stdio 协议，不能动句柄
	}
	switch cmd {
	case "mcp":
		if err := mcpServe(); err != nil {
			fmt.Fprintln(os.Stderr, "mcp:", err)
			os.Exit(1)
		}
	case "tray":
		runTray(cfg, st)
	case "scan":
		// 一次性扫描默认走 MFT 直读（约 16× 加速：3s vs 52s）。
		// 常驻路径不开——它们靠 USN 增量缓存，MFT 成功时不写缓存，反而会每轮全量。
		// --walk / --no-mft 是出问题时的退路，必须留着。
		useMftEngine = true
		for _, a := range os.Args[2:] {
			switch a {
			case "--mft", "mft":
				useMftEngine = true
			case "--walk", "--no-mft", "walk":
				useMftEngine = false
			}
		}
		runScan(cfg, st)
		st.Save()
	case "winsxs", "dism-analyze":
		// 只读、按需：DISM 的组件库分析。约 1–2 分钟、要管理员、明显占 CPU，
		// 所以它是一条独立子命令，**不接进扫描路径**。
		fmt.Println("正在分析组件库（DISM，约 1–2 分钟，需要管理员权限）…")
		fmt.Print(plainComponentStore(analyzeComponentStore()))
	case "report":
		fmt.Println("报告：", writeReport(cfg, st))
	case "report-open":
		openPath(writeReport(cfg, st))
	case "serve":
		fmt.Printf("http://127.0.0.1:%d/\n", cfg.Port)
		serveHTTP(cfg, st)
	case "watch":
		fmt.Println("常驻监测（Ctrl+C 停止）")
		watchLoop(cfg, st)
	case "status":
		printStatus(cfg, st)
	case "probe":
		fmt.Println(probeJSON(cfg, st))
	case "store-test":
		storeSelftest(cfg, st)
	case "dryrun":
		if len(os.Args) < 3 {
			fmt.Println("用法：diskmon-go dryrun <路径>")
			return
		}
		printRehearsal(cfg, os.Args[2])
	case "quarantine":
		if len(os.Args) < 3 {
			fmt.Println("用法：diskmon-go quarantine <路径>")
			return
		}
		q := openQuarantine(cfg)
		e, err := q.apply(cfg, os.Args[2], "cli")
		if err != nil {
			fmt.Println("隔离失败：", err)
			return
		}
		fmt.Printf("已隔离：%s → %s（%s，%d 个文件，ID=%s）\n", e.Original, e.Stored, human(e.Bytes), e.Files, e.ID)
	case "quarantine-rebuild":
		q := openQuarantine(cfg)
		rec, orph, skip := q.rebuild()
		fmt.Printf("重建隔离索引：恢复 %d 条，孤儿目录 %d，失效 %d\n", rec, orph, skip)
	case "quarantine-list":
		q := openQuarantine(cfg)
		list := q.entries()
		if len(list) == 0 {
			fmt.Println("隔离区为空")
		}
		for _, e := range list {
			flag := ""
			if e.Restored {
				flag = "（已还原）"
			}
			fmt.Printf("%s  %10s  %s%s\n", e.ID, human(e.Bytes), e.Original, flag)
		}
	case "restore":
		if len(os.Args) < 3 {
			fmt.Println("用法：diskmon-go restore <ID>")
			return
		}
		q := openQuarantine(cfg)
		e, err := q.restore(os.Args[2])
		if err != nil {
			fmt.Println("还原失败：", err)
			return
		}
		fmt.Println("已还原到：", e.Original)
	case "purge":
		q := openQuarantine(cfg)
		n, b := q.purge(cfg.QuarantineDays)
		fmt.Printf("清理 %d 项，释放 %s\n", n, human(b))
	case "mute":
		if len(os.Args) > 2 {
			switch os.Args[2] {
			case "off":
				clearMute()
				fmt.Println("已取消静音（仅本进程；托盘请用右键菜单或 /mute）")
			case "forever":
				muteForever()
				fmt.Println("已静音直到本进程退出")
			default:
				setMute(time.Hour)
				fmt.Println("已静音 1 小时（仅本进程）")
			}
		} else {
			fmt.Println("静音状态：" + func() string {
				if s := muteStatusText(); s != "" {
					return s
				}
				return "未静音"
			}())
		}
	case "open":
		// 桌面图标跑的就是这条。以前它只开浏览器、不管服务，服务没运行时
		// 点图标只会得到一个打不开的窗口，还不给任何解释——用户管这叫「又进不去了」。
		// 现在：先探端口，没服务就自己拉起来；实在起不来，至少把静态报告打开。
		if ensureServer(cfg.Port) {
			openAppWindow(cfg.Port)
		} else {
			p := writeReport(cfg, st)
			fmt.Println("后台服务没起来，已改为打开静态报告：", p)
			fmt.Println("要看可交互的界面（清理、设置），请手动运行： diskmon-go tray")
			openPath(p)
		}
	case "health":
		fmt.Println(healthSnapshot(cfg, st).Text())
	case "diag":
		if code := diagCommand(cfg, st); code != 0 {
			os.Exit(code)
		}
	case "procs":
		printProcs()
	case "dup":
		root := cfg.Root
		minMB := 4
		if len(os.Args) > 2 {
			root = os.Args[2]
		}
		if len(os.Args) > 3 {
			if n, err := strconv.Atoi(os.Args[3]); err == nil {
				minMB = n
			}
		}
		runDupScan(cfg, st, root, minMB)
	case "dups":
		if r := loadDups(cfg); r != nil {
			fmt.Printf("%d 组 · 可释放 %s · 扫描于 %s\n", len(r.Groups), human(r.TotalWasted), time.Unix(r.TS, 0).Format("01-02 15:04"))
		} else {
			fmt.Println("还没有重复文件扫描结果，先运行 dup")
		}
	case "leftovers":
		printLeftovers(cfg, st)
	case "winapp2":
		if len(os.Args) < 3 {
			fmt.Println("用法: diskmon-go.exe winapp2 <winapp2.ini>")
			fmt.Println("只解析并列出规则，不删除任何文件。")
			return
		}
		reportWinapp2(os.Args[2])
	case "mft-dump":
		letter := "C"
		if len(os.Args) > 2 {
			letter = os.Args[2]
			// 接受 C: / C:\ / c 这几种写法
			for len(letter) > 0 && (letter[len(letter)-1] == ':' || letter[len(letter)-1] == '\\') {
				letter = letter[:len(letter)-1]
			}
		}
		// 默认连对账一起做——这个命令存在的意义就是证明数字对得上
		// 第 4 个参数可以是 nocompare（跳过对账），也可以是一个文件名（定点查询）
		compare := true
		probe := ""
		pathDiff := false
		for _, a := range os.Args[3:] {
			switch a {
			case "nocompare":
				compare = false
			case "paths":
				// 路径集合直接做差：打印只在遍历/MFT 一方出现的具体路径
				pathDiff = true
			case "":
			default:
				probe = a
			}
		}
		if pathDiff {
			reportMftPathDiff(letter)
			break
		}
		reportMftDump(letter, compare, probe)
	case "walk-count":
		// 纯遍历计数：不需要管理员，用来量化「遍历自己漏了多少」。
		// 存在的理由：MFT 和目录遍历对不上时，必须先知道遍历这一侧的盲区有多大，
		// 否则所有差异都会被默认记到 MFT 头上。
		letter := "C"
		if len(os.Args) > 2 {
			raw := os.Args[2]
			t, ok := walkCountLetter(raw)
			if !ok {
				// 这里以前把入参原样当盘符，reportWalkCount 再拼成 "<入参>:\"：
				// 传一个路径进去会去遍历一个不存在的根，报出 0 目录 0 文件，
				// 还打印「本次遍历没有漏，可以直接当参照物用」。一个给 MFT 对账
				// 提供参照物的诊断工具，宁可报错，也不能给出「可信」的错误结论。
				fmt.Printf("用法：diskmon-go walk-count [盘符]　例如 walk-count C 或 walk-count C:\n")
				fmt.Printf("  收到的是 %q —— 这个子命令只统计整个卷，不接受路径。\n", raw)
				fmt.Printf("  要看某个目录的遍历数，用 inspect 或 dryrun。\n")
				os.Exit(2)
			}
			letter = t
		}
		if !reportWalkCount(letter) {
			os.Exit(3)
		}
	case "plans":
		for _, pl := range allPlans(getConfig(), st) {
			fmt.Printf("[%s] %d 个目录 · %s\n    %s\n", pl.Label, len(pl.Dirs), human(pl.Bytes), pl.Desc)
		}
	case "diff":
		printDiff(cfg)
	case "apps":
		printApps(getConfig(), st)
	case "rules":
		printRules(getConfig(), st)
	case "rules-run":
		only := ""
		if len(os.Args) > 2 {
			only = os.Args[2]
		}
		results := runDueRules(getConfig(), st, true, only)
		if len(results) == 0 {
			fmt.Println("没有可执行的规则")
		}
		for _, r := range results {
			fmt.Printf("[%s] %s\n", r.Name, r.Reason)
		}
	case "rules-enable", "rules-disable", "rules-dry":
		if len(os.Args) < 3 {
			fmt.Println("用法：diskmon-go rules-enable|rules-disable|rules-dry <规则名>")
			return
		}
		mode := map[string]string{"rules-enable": "on", "rules-disable": "off", "rules-dry": "dry"}[cmd]
		if setRuleMode(os.Args[2], mode) {
			fmt.Println("已更新规则模式：", mode)
		} else {
			fmt.Println("找不到规则：", os.Args[2])
		}
	case "notify-test":
		notify("diskmon 测试通知", "看到这条说明通知正常。", false)
		time.Sleep(2 * time.Second)
	case "usn-selftest":
		os.Exit(usnSelftest(cfg))
	case "autostart-on":
		exe, _ := os.Executable()
		toggleAutostart()
		fmt.Println("已切换开机自启动：", exe)
	default:
		fmt.Println("用法：diskmon-go [tray|scan|report|report-open|serve|watch|status|probe|diff|apps|procs|cases]")
		fmt.Println("      diskmon-go mft-dump [C:] [nocompare|paths|文件名]  只读 MFT 直读 + 对账（需管理员）")
		fmt.Println("      diskmon-go walk-count [C:]                        只做目录遍历计数，报出自身盲区（不需管理员）")
		fmt.Println("      diskmon-go winapp2 <file.ini> 解析 winapp2.ini 规则（只解析不删）")
	}
}
