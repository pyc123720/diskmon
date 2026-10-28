package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// diag 生成一份可直接贴进 issue 的诊断包。
// 目的：把「你的版本？什么系统？数据目录在哪？扫描过没有？清过什么？」一次性问完，
// 免得一来一回三四轮。内容只读本机状态，不联网、不上传。
func diagCommand(cfg Config, st *Store) int {
	var b strings.Builder
	now := time.Now()
	p := filepath.Join(cfg.DataDir, "diag-"+now.Format("20060102-150405")+".txt")

	fmt.Fprintf(&b, "diskmon diag bundle\n")
	fmt.Fprintf(&b, "生成时间 %s\n", now.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "\n## 版本与环境\n")
	fmt.Fprintf(&b, "version      %s\n", version)
	fmt.Fprintf(&b, "go           %s\n", runtime.Version())
	fmt.Fprintf(&b, "os/arch      %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "管理员权限   %v\n", isElevated())
	fmt.Fprintf(&b, "数据目录     %s\n", cfg.DataDir)
	if dataDirFallback {
		fmt.Fprintf(&b, "             （请求的是 %s，不可写，已降级）\n", dataDirRequested)
	}
	fmt.Fprintf(&b, "日志文件     %s\n", filepath.Join(cfg.DataDir, "diskmon-go.log"))

	fmt.Fprintf(&b, "\n## 配置\n")
	fmt.Fprintf(&b, "扫描根目录   %s\n", cfg.Root)
	fmt.Fprintf(&b, "端口         %d\n", cfg.Port)
	fmt.Fprintf(&b, "阈值         warn %.1f GB / danger %.1f GB / critical %.1f GB\n", cfg.WarnGB, cfg.DangerGB, cfg.CriticalGB)
	fmt.Fprintf(&b, "百分比阈值   warn %.1f%% / danger %.1f%% / critical %.1f%%\n", cfg.WarnPct, cfg.DangerPct, cfg.CriticalPct)
	fmt.Fprintf(&b, "隔离区       %d 天 / 上限 %d GB\n", cfg.QuarantineDays, cfg.QuarantineMaxGB)
	fmt.Fprintf(&b, "自动扫描     %d 小时一次\n", cfg.AutoScanHours)
	fmt.Fprintf(&b, "懒人自动清理 %s\n", lazyScheduleText(cfg))
	fmt.Fprintf(&b, "L2 规则      %d 条（启用 %d）\n", len(cfg.Rules), countEnabledRules(cfg))
	fmt.Fprintf(&b, "白名单       %d 条\n", len(cfg.Whitelist))

	v, _ := volumeStats(cfg.Root)
	lvl := levelOf(v, cfg)
	fmt.Fprintf(&b, "\n## 磁盘现状\n")
	fmt.Fprintf(&b, "总 %s / 已用 %s / 剩余 %s（%s）\n", human(v.Total), human(v.Total-v.Free), human(v.Free), levelText(lvl))

	sc := st.GetScan()
	fmt.Fprintf(&b, "\n## 最后一次扫描\n")
	if sc == nil {
		fmt.Fprintf(&b, "还没有扫描结果\n")
	} else {
		fmt.Fprintf(&b, "时间 %s（%s）\n", time.Unix(sc.TS, 0).Format("2006-01-02 15:04:05"), humanAge(time.Since(time.Unix(sc.TS, 0))))
		src := sc.Source
		if src == "" {
			src = "未记录（旧版本扫描结果）"
		}
		fmt.Fprintf(&b, "来源 %s · 文件 %d · 目录 %d · 错误 %d · 耗时 %.0fs\n", src, sc.Files, sc.Dirs, sc.Errors, sc.Duration)
		if sc.Source == "incremental" {
			fmt.Fprintf(&b, "增量 复用 %d 个子树 · 重扫 %d 个\n", sc.ReusedChildren, sc.ScannedChildren)
		}
		fmt.Fprintf(&b, "\n### 分类占用（前 12）\n")
		type kv struct {
			k string
			v uint64
		}
		var catList []kv
		for k, n := range sc.Cats {
			catList = append(catList, kv{k, n})
		}
		sort.Slice(catList, func(i, j int) bool { return catList[i].v > catList[j].v })
		for i, c := range catList {
			if i >= 12 {
				break
			}
			label := c.k
			if m, ok := cats[c.k]; ok {
				label = m.Label + "（" + c.k + "，tier=" + m.Tier + "）"
			}
			fmt.Fprintf(&b, "%-52s %12s\n", label, human(c.v))
		}
		fmt.Fprintf(&b, "\n### 最大目录（前 12）\n")
		for i, d := range sc.Deep {
			if i >= 12 {
				break
			}
			fmt.Fprintf(&b, "%-60s %10s  cat=%s\n", shortText(d.Path, 60), human(d.Bytes), d.Cat)
		}
		fmt.Fprintf(&b, "\n### 最大文件（前 10）\n")
		for i, f := range sc.TopFiles {
			if i >= 10 {
				break
			}
			fmt.Fprintf(&b, "%-60s %10s\n", shortText(f.Path, 60), human(f.Bytes))
		}
		fmt.Fprintf(&b, "\n### 写入方 Top 8（近似）\n")
		type okv struct {
			k string
			v uint64
		}
		var owners []okv
		for k, n := range sc.Owners {
			owners = append(owners, okv{k, n})
		}
		sort.Slice(owners, func(i, j int) bool { return owners[i].v > owners[j].v })
		for i, o := range owners {
			if i >= 8 {
				break
			}
			fmt.Fprintf(&b, "%-40s %12s\n", shortText(o.k, 40), human(o.v))
		}
	}

	// 增长采样与预测
	samples := append([]Sample(nil), st.st.Samples...)
	fmt.Fprintf(&b, "\n## 增长采样\n")
	fmt.Fprintf(&b, "样本 %d 条\n", len(samples))
	var deltas []DayDelta
	src := "mtime 估算"
	if net := netGrowthSamples(samples); len(net) >= 3 {
		deltas = net
		src = "真实采样"
	} else if sc != nil {
		deltas = sortedDayWrites(sc.DayWrites)
	}
	if len(deltas) > 0 {
		f := forecastFrom(v.Free, deltas, src)
		fmt.Fprintf(&b, "来源 %s · 覆盖 %d 天 · 中位 %.0f MB/天 · p10 %.0f / p90 %.0f\n", src, f.SampleDay, f.MedianMB, f.P10MB, f.P90MB)
		if f.OK {
			fmt.Fprintf(&b, "预计写满 中位 %s / 快 %s / 慢 %s\n", fmtDur(f.DaysP50, true), fmtDur(f.DaysFast, true), fmtDur(f.DaysSlow, true))
		}
		for i := len(deltas) - 7; i < len(deltas); i++ {
			if i >= 0 {
				fmt.Fprintf(&b, "  %s  %10s\n", deltas[i].Day, human(deltas[i].Bytes))
			}
		}
	} else {
		fmt.Fprintf(&b, "增长数据不足（需要连续 3 天采样）\n")
	}

	// 隔离区
	var qBytes uint64
	qN := 0
	oldest := int64(0)
	for _, e := range openQuarantine(cfg).entries() {
		if e.Restored {
			continue
		}
		qN++
		qBytes += e.Bytes
		if oldest == 0 || e.TS < oldest {
			oldest = e.TS
		}
	}
	fmt.Fprintf(&b, "\n## 隔离区\n")
	if qN == 0 {
		fmt.Fprintf(&b, "空\n")
	} else {
		fmt.Fprintf(&b, "%d 项 · %s · 最早一项 %s\n", qN, human(qBytes), time.Unix(oldest, 0).Format("2006-01-02 15:04"))
	}

	// 告警
	alerts := st.Alerts()
	fmt.Fprintf(&b, "\n## 最近告警（最多 15 条）\n")
	start := len(alerts) - 15
	if start < 0 {
		start = 0
	}
	if len(alerts) == 0 {
		fmt.Fprintf(&b, "无\n")
	}
	for i := start; i < len(alerts); i++ {
		fmt.Fprintf(&b, "%s  [%s] %s\n", time.Unix(alerts[i].TS, 0).Format("01-02 15:04"), alerts[i].Kind, alerts[i].Text)
	}

	// 运行时
	fmt.Fprintf(&b, "\n## 运行时\n")
	fmt.Fprintf(&b, "%s\n", healthSnapshot(cfg, st).Text())

	// 日志尾巴
	if tail := tailFile(filepath.Join(cfg.DataDir, "diskmon-go.log"), 80); tail != "" {
		fmt.Fprintf(&b, "\n## 日志最后 80 行\n%s\n", tail)
	}

	header := "# 这份文件包含本机路径和程序名，贴到公开 issue 前请自己过一眼。\n\n"
	if err := os.WriteFile(p, []byte(header+b.String()), 0o644); err != nil {
		fmt.Printf("写入诊断包失败：%v\n", err)
		fmt.Println(b.String())
		return 1
	}
	fmt.Printf("诊断包已写入：%s\n", p)
	fmt.Printf("版本 %s · %s/%s · 管理员 %v · 剩余 %s · 扫描 %s\n",
		version, runtime.GOOS, runtime.GOARCH, isElevated(), human(v.Free),
		func() string {
			if sc == nil {
				return "未扫描"
			}
			return time.Unix(sc.TS, 0).Format("01-02 15:04")
		}())
	fmt.Println("把它拖进 issue 即可；不想贴全文就只贴「版本与环境」「最后一次扫描」「日志最后 80 行」三段。")
	return 0
}

func countEnabledRules(cfg Config) int {
	n := 0
	for _, r := range cfg.Rules {
		if r.Enabled {
			n++
		}
	}
	return n
}

// tailFile 返回文件最后 n 行；文件不存在时返回空串。
func tailFile(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
