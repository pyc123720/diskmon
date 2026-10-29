package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ---------- L2 自动清理规则 ----------

type CleanRule struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	MaxSizeMB   int    `json:"max_size_mb"`  // 目录超过这个大小才动手
	KeepDays    int    `json:"keep_days"`    // 只删这么久没动过的文件
	IntervalMin int    `json:"interval_min"` // 多久跑一次
	WindowStart string `json:"window_start,omitempty"` // HH:MM，空=全天
	WindowEnd   string `json:"window_end,omitempty"`
	Enabled     bool   `json:"enabled"`
	DryRun      bool   `json:"dry_run"` // 只记录不删；先跑 7 天
}

type RuleStat struct {
	LastRun    int64  `json:"last_run"`
	LastFreed  uint64 `json:"last_freed"`
	TotalFreed uint64 `json:"total_freed"`
	Runs       int    `json:"runs"`
	WouldFree  uint64 `json:"would_free"`
	LastNote   string `json:"last_note"`
}

type RuleResult struct {
	Name      string
	Path      string
	DirSize   uint64
	Skipped   bool
	Reason    string
	WouldFree uint64
	Freed     uint64
	Removed   int
	Locked    int
	DryRun    bool
	Restore   RestorePointOutcome // 本次真删前的系统还原点结论；试运行/跳过时为空
}

func (st *Store) rulesStatsPath() string {
	return filepath.Join(filepath.Dir(st.path), "rules-stats.json")
}

// 规则统计单独落盘：托盘进程和命令行进程都会写，放在 state.json 里会互相覆盖
func (st *Store) loadRuleStats() map[string]RuleStat {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.loadRuleStatsLocked()
}

func (st *Store) loadRuleStatsLocked() map[string]RuleStat {
	out := map[string]RuleStat{}
	if b, err := os.ReadFile(st.rulesStatsPath()); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func (st *Store) saveRuleStatsLocked(m map[string]RuleStat) {
	if b, err := json.MarshalIndent(m, "", "  "); err == nil {
		_ = writeFileAtomic(st.rulesStatsPath(), b)
	}
}

func (st *Store) ruleStat(name string) RuleStat {
	m := st.loadRuleStats()
	return m[name]
}

func (st *Store) setRuleStat(name string, s RuleStat) {
	st.mu.Lock()
	defer st.mu.Unlock()
	m := st.loadRuleStatsLocked()
	m[name] = s
	st.saveRuleStatsLocked(m)
}

func (st *Store) allRuleStats() map[string]RuleStat {
	return st.loadRuleStats()
}

// 规则只允许落在「可清」类目录：临时文件 / 开发缓存 / 更新缓存 / 日志转储
func ruleGuard(cfg Config, rule CleanRule) (string, bool) {
	cat, tier, refuse := guardPath(cfg, rule.Path)
	if refuse != "" {
		return refuse, false
	}
	if tier != "clean" {
		return fmt.Sprintf("该目录属于「%s」，自动清理只允许「%s」类目录", cats[cat].Label, tierText["clean"]), false
	}
	return "", true
}

// 执行一条规则；dryRun 强制只统计
func runRule(cfg Config, st *Store, rule CleanRule, forceDry bool) RuleResult {
	res := RuleResult{Name: rule.Name, Path: rule.Path, DryRun: rule.DryRun || forceDry}
	if why, ok := ruleGuard(cfg, rule); !ok {
		res.Skipped = true
		res.Reason = why
		return res
	}
	st0, err := os.Stat(rule.Path)
	if err != nil || !st0.IsDir() {
		res.Skipped = true
		res.Reason = "目录不存在或不可读"
		return res
	}

	cutoff := time.Now().AddDate(0, 0, -max(rule.KeepDays, 1)).Unix()
	type cand struct {
		p   string
		n   uint64
		mt  int64
	}
	var cands []cand
	var dirSize uint64
	_ = filepath.WalkDir(rule.Path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, e := d.Info()
		if e != nil {
			return nil
		}
		size := uint64(info.Size())
		dirSize += size
		if info.ModTime().Unix() > cutoff {
			return nil
		}
		if isLocked(p) {
			res.Locked++
			return nil
		}
		cands = append(cands, cand{p, size, info.ModTime().Unix()})
		return nil
	})
	res.DirSize = dirSize

	threshold := uint64(max(rule.MaxSizeMB, 1)) << 20
	if dirSize < threshold {
		res.Skipped = true
		res.Reason = fmt.Sprintf("目录 %s，未超过阈值 %d MB", human(dirSize), rule.MaxSizeMB)
		return res
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].n > cands[j].n })
	for _, c := range cands {
		res.WouldFree += c.n
	}
	if res.DryRun || len(cands) == 0 {
		res.Reason = fmt.Sprintf("试运行：可释放 %s（%d 个文件，超过 %d 天未动）",
			human(res.WouldFree), len(cands), rule.KeepDays)
	} else {
		// 真删之前先落一次系统还原点。规则清理是唯一绕过隔离区、直接 os.Remove 的
		// 路径，所以它必须自己挂一次落点；同一次清理（runDueRules 一轮 / 手工执行
		// 一条）里只落一次，多批不重复。
		end := beginCleanupRun(st, fmt.Sprintf("规则「%s」：%s（%d 个超过 %d 天的文件）",
			rule.Name, rule.Path, len(cands), rule.KeepDays))
		res.Restore = ensureRestorePoint("")
		for _, c := range cands {
			if err := os.Remove(c.p); err == nil {
				res.Freed += c.n
				res.Removed++
			} else {
				res.Locked++
			}
		}
		end()
		res.Reason = fmt.Sprintf("已清理 %s（%d 个文件，跳过 %d）", human(res.Freed), res.Removed, res.Locked)
	}
	return res
}

func runDueRules(cfg Config, st *Store, force bool, only string) []RuleResult {
	var out []RuleResult
	// 一轮规则清理算「一次清理」：整轮只落一个还原点。逐条规则里那句
	// beginCleanupRun 此时是嵌套调用，不会重置批次（见 restore_point.go）。
	end := beginCleanupRun(st, fmt.Sprintf("自动清理一轮：%d 条规则", len(cfg.Rules)))
	defer end()
	for _, rule := range cfg.Rules {
		if only != "" && rule.Name != only {
			continue
		}
		if !rule.Enabled && !force {
			continue
		}
		stat := st.ruleStat(rule.Name)
		interval := int64(max(rule.IntervalMin, 5)) * 60
		if !force && time.Now().Unix()-stat.LastRun < interval {
			continue
		}
		if rule.WindowStart != "" && rule.WindowEnd != "" && !inWindow(time.Now(), rule.WindowStart, rule.WindowEnd) {
			continue
		}
		r := runRule(cfg, st, rule, false)
		now := time.Now().Unix()
		stat.LastRun = now
		stat.Runs++
		stat.LastNote = r.Reason
		if r.DryRun {
			stat.WouldFree = r.WouldFree
		} else {
			stat.LastFreed = r.Freed
			stat.TotalFreed += r.Freed
		}
		st.setRuleStat(rule.Name, stat)

		if !r.Skipped {
			if r.DryRun {
				if r.WouldFree > 0 {
					st.AddAlert("rule-dry", fmt.Sprintf("[%s] %s", rule.Name, r.Reason))
				}
			} else if r.Freed > 0 {
				st.AddAlert("rule-run", fmt.Sprintf("[%s] %s", rule.Name, r.Reason))
				notify("diskmon 自动清理", fmt.Sprintf("%s：%s", rule.Name, r.Reason), false)
			}
		}
		logf("rule %s: %s", rule.Name, r.Reason)
		out = append(out, r)
	}
	return out
}

func rulesLoop(cfg Config, st *Store) {
	lastNotice := ""
	time.Sleep(30 * time.Second) // 启动后稍等，别和首轮采样抢
	for {
		st.FlushDigest()
		if !paused.Load() {
			runDueRules(getConfig(), st, false, "")
			// 每天提醒一次：隔离项即将到期
			q := openQuarantine(getConfig())
			today := time.Now().Format("2006-01-02")
			if exp := q.expiringSoon(getConfig().QuarantineDays, 1); len(exp) > 0 && lastNotice != today {
				var bytes uint64
				for _, e := range exp {
					bytes += e.Bytes
				}
				msg := fmt.Sprintf("%d 个隔离项将在 24 小时内到期（%s），需要保留请尽快还原", len(exp), human(bytes))
				st.AddAlert("quarantine-expiry", msg)
				notify("diskmon 隔离区提醒", msg, false)
				lastNotice = today
			}
		}
		time.Sleep(50 * time.Minute)
	}
}

func printRules(cfg Config, st *Store) {
	if len(cfg.Rules) == 0 {
		fmt.Println("还没有自动清理规则。在 config.json 的 rules 数组里添加，例如：")
		fmt.Println(fmt.Sprintf(`  {"name":"Temp清理","path":%q,"max_size_mb":2048,"keep_days":7,"interval_min":60,"enabled":true,"dry_run":true}`, os.TempDir()))
		return
	}
	stats := st.allRuleStats()
	for _, r := range cfg.Rules {
		s := stats[r.Name]
		state := "停用"
		if r.Enabled {
			state = "已启用"
			if r.DryRun {
				state = "试运行中"
			}
		}
		fmt.Printf("[%s] %s\n  路径 %s\n  阈值 %d MB · 只删 %d 天前 · 每 %d 分钟 · %s\n",
			r.Name, state, r.Path, r.MaxSizeMB, r.KeepDays, r.IntervalMin, state)
		fmt.Printf("  上次：%s\n  累计释放：%s", s.LastNote, human(s.TotalFreed))
		if s.WouldFree > 0 {
			fmt.Printf("　· 试运行可释放：%s", human(s.WouldFree))
		}
		fmt.Println()
	}
}

func setRuleMode(name, mode string) bool {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	for i := range liveConfig.Rules {
		if liveConfig.Rules[i].Name != name {
			continue
		}
		switch mode {
		case "off":
			liveConfig.Rules[i].Enabled = false
		case "dry":
			liveConfig.Rules[i].Enabled = true
			liveConfig.Rules[i].DryRun = true
		case "on":
			liveConfig.Rules[i].Enabled = true
			liveConfig.Rules[i].DryRun = false
		default:
			return false
		}
		return saveConfig(liveConfig)
	}
	return false
}

func saveConfig(cfg Config) bool {
	p := filepath.Join(exeDir(), "config.json")
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return false
	}
	return os.WriteFile(p, b, 0o644) == nil
}

func urlQuery(s string) string { return url.QueryEscape(s) }

