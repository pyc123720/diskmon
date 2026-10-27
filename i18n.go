package main

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// ---------- 语言 ----------

var curLang atomic.Value // "zh" | "en"

func init() { curLang.Store("zh") }

const langCookie = "diskmon_lang"

func langFromRequest(r *http.Request) string {
	if c, err := r.Cookie(langCookie); err == nil {
		if c.Value == "en" {
			return "en"
		}
		if c.Value == "zh" {
			return "zh"
		}
	}
	if r.URL.Query().Get("lang") == "en" {
		return "en"
	}
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "en") {
		return "en"
	}
	return "zh"
}

func currentLang() string {
	if v, ok := curLang.Load().(string); ok && v != "" {
		return v
	}
	return "zh"
}

func isEN() bool { return currentLang() == "en" }

// ---------- 字典 ----------

// 中文 → 英文。按长度从长到短替换，避免短词先吃掉长句。
var zh2en = map[string]string{
	// 导航 / 通用
	"总览": "Overview", "清理": "Clean up", "趋势": "Trends", "全部数据": "All data",
	"自动清理": "Auto-clean", "查路径": "Check path", "设置": "Settings", "导出": "Export",
	"亮色": "Light", "暗色": "Dark", "分区页": "Sections", "全部": "All",
	"只读": "read-only", "会移动/删除文件": "moves/deletes files",
	"数据来源": "Data source", "全量扫描": "full scan", "增量扫描": "incremental scan",
	"缓存（上次扫描后无写入）": "cached (no writes since last scan)",
	"复用": "reused", "个子树": "subtrees", "重扫": "rescanned", "个": "",
	"强制全扫": "Force full scan", "重新扫描": "Rescan",

	// 总览
	"系统盘": "System drive", "剩余": "free", "已用": "used", "总量": "total",
	"今日建议": "Today's suggestion", "懒人清理": "Lazy clean", "一键清理（可撤销）": "Clean now (undoable)",
	"可释放": "reclaimable", "个目录": "folders", "只清临时/开发缓存/更新缓存/日志，全部进隔离区": "temp / dev cache / update cache / logs only, all to quarantine",
	"粘贴一个路径，看看能不能清（不会删除）": "Paste a path to see if it can be cleaned (read-only)",
	"查一下": "Check", "最近告警": "Recent alerts", "搜索（路径 / 名称）": "Search (path / name)",
	"① 扫描一次": "① Scan once", "拿到数据，约 1–2 分钟（只读）": "collect data, 1–2 min (read-only)",
	"② 懒人清理": "② Lazy clean", "只清可清类，随时撤销": "safe caches only, undoable",
	"③ 看趋势": "③ See trends", "谁在涨、多久装满": "who is growing, time to full",
	"未开启自动清理": "auto-clean off", "自动清理：": "Auto-clean: ",

	// 清理
	"清理中心": "Cleanup center", "一键方案": "Presets", "保守": "Conservative",
	"标准": "Standard", "激进": "Aggressive", "演练并执行": "Preview & run",
	"按应用聚合": "Per application", "应用": "App", "可回收": "Reclaimable", "查看": "View",
	"重复文件": "Duplicates", "卸载残留": "Leftovers", "隔离区": "Quarantine",
	"清理记录": "Cleanup history", "回收": "reclaim", "上限": "cap", "保留": "keep", "天": "days",
	"方案": "Preset", "媒体": "Media", "残留": "Leftovers", "重复": "Duplicates",

	// 趋势
	"写满预测": "Time to full", "每日新增": "Daily growth", "增长性质判定": "Growth type",
	"对比上次": "vs last scan", "一周前": "vs a week ago", "后写满（中位）": "until full (median)",
	"区间": "range", "等效增速": "effective rate", "数据来源：": "Source: ",

	// 详情
	"容量": "Capacity", "预测": "Forecast", "每日": "Daily", "性质": "Type",
	"分类": "Categories", "文件": "Files", "系统": "System", "目录排行": "Top folders",
	"Top 大文件": "Top files", "空间分布（Treemap）": "Space (treemap)",
	"风险提示卡（可回收项 · 删前先看后果）": "Risk cards (reclaimable, read consequences first)",
	"按应用聚合的清理建议": "Cleanup suggestions per app",
	"写入热点进程（最近 5 分钟）": "Top writer processes (last 5 min)",
	"引擎 / 自占内存": "Engine / memory", "系统工具": "System tools", "已知限制": "Known limits",
	"分类占用": "Category usage", "可删占比（除系统必要文件外）": "Reclaimable share (excl. system files)",
	"共": "", "项": "items", "到期才真删": "deleted only after retention",

	// 设置
	"监测": "Monitoring", "扫描根目录": "Scan root", "采样间隔（秒）": "Sample interval (s)",
	"增长告警阈值（MB / 5 秒）": "Growth alert threshold (MB / 5 s)",
	"每日自动扫描间隔（小时，0=关闭）": "Auto-scan interval (hours, 0=off)",
	"弹窗通知阈值（MB，低于只记录）": "Notification threshold (MB, below = record only)",
	"自动懒人清理（小时，0–23）": "Auto lazy clean hour (0–23)",
	"自动懒人清理": "Auto lazy clean", "关闭": "Off", "每天一次": "Daily", "每周日一次": "Weekly (Sunday)",
	"低空间阈值": "Low-space thresholds", "警告": "Warn", "危险": "Danger", "紧急": "Critical",
	"保留天数": "Retention (days)", "容量上限（GB）": "Size cap (GB)", "白名单": "Whitelist",
	"每行一条，路径片段包含即命中（不区分大小写）。这些目录永远不会被自动清理。": "One pattern per line; substring match, case-insensitive. These are never cleaned automatically.",
	"通知与数据": "Notifications & data", "使用 Windows 通知（WinRT toast）": "Use Windows notifications (WinRT toast)",
	"数据目录": "Data directory", "配置文件": "Config file",
	"保存设置": "Save settings", "恢复默认": "Reset defaults",

	// 规则
	"自动清理规则（L2）": "Auto-clean rules (L2)",
	"名称": "Name", "目录": "Folder", "阈值 MB": "Threshold MB", "只删 N 天前": "Keep days",
	"检查间隔（分钟）": "Check interval (min)", "执行窗口开始（HH:MM，可空）": "Window start (HH:MM, empty=all day)",
	"执行窗口结束（HH:MM，可空）": "Window end (HH:MM)", "启用": "Enabled",
	"试运行（只记录，不删除）": "Dry-run (record only)", "保存规则": "Save rule", "取消": "Cancel",
	"编辑": "Edit", "删除": "Delete", "停用": "Disable", "立即试运行": "Dry-run now",
	"立即执行": "Run now", "确认启用（真实删除）": "Enable (real delete)",

	// 状态 / 分类
	"可清": "Safe to clean", "需确认": "Confirm first", "可调整": "Advanced", "系统必要": "Required",
	"程序安装": "Installed app", "程序数据/缓存": "App data", "用户文档": "User documents",
	"用户资料": "User profile", "系统核心": "System core", "Windows 组件": "Windows component",
	"临时文件": "Temp files", "开发工具缓存": "Dev caches", "日志与转储": "Logs & dumps",
	"浏览器缓存": "Browser cache", "通讯软件媒体": "Messaging media", "游戏数据": "Game data",
	"AI 模型": "AI models", "Windows 更新缓存": "Update cache", "系统休眠": "Hibernation",
	"页面文件": "Page file", "未分类": "Uncategorized", "程序缓存": "App cache",
	"Windows Installer 缓存": "Installer cache", "系统还原": "System restore",
	"回收站": "Recycle bin", "临时文件/缓存": "Temp/cache",

	// 状态条
	"正常": "Healthy", "注意": "Watch", "警告区": "Warning", "危险区": "Danger", "紧急区": "Critical",
	"引擎": "Engine", "运行中": "running", "已停止": "stopped", "告警": "Alerts",
	// 表头 / 统计
	"时间": "Time", "事件": "Event", "大小": "Size", "类别": "Category", "处置": "Tier",
	"上次扫描": "Last scan", "耗时": "took", "总容量": "Total", "个文件": "files",
	"版本": "Version", "已运行": "uptime", "内存": "Memory",

	// 首启 / 提示
	"第一次使用": "First run", "先扫描一次": "scan once", "回来看「今日建议」": "check Today's suggestion",
	"想省事就点「懒人清理」（全部可撤销）": "use Lazy clean (fully undoable)",
	"监测是只读的；任何清理都先进隔离区，7 天内可还原。": "Monitoring is read-only; cleanups go to quarantine and can be restored for 7 days.",
	"正在扫描": "Scanning", "取消扫描": "Cancel scan", "后台扫描，先看别的": "Scan in background, keep browsing",
	"只读扫描，不会改动任何文件。首次通常 1–2 分钟。": "Read-only scan, changes nothing. First run usually 1–2 minutes.",
}

var (
	dictKeys []string
	dictOnce sync.Once
)

// buildDict 合并基础字典与句子级补充，按长度降序，保证长句优先
func buildDict() {
	dictOnce.Do(func() {
		for k, v := range zh2enExtra {
			if _, exists := zh2en[k]; !exists {
				zh2en[k] = v
			}
		}
		for k := range zh2en {
			if len([]rune(k)) < 2 {
				continue // 单字词条太容易误伤长句
			}
			dictKeys = append(dictKeys, k)
		}
		sort.Slice(dictKeys, func(i, j int) bool { return len(dictKeys[i]) > len(dictKeys[j]) })
	})
}

// translateHTML 把页面里的中文短语替换成英文（只做唯一短语替换，不解析 HTML）
func translateHTML(s string) string {
	if !isEN() {
		return s
	}
	buildDict()
	for _, k := range dictKeys {
		if strings.Contains(s, k) {
			s = strings.ReplaceAll(s, k, zh2en[k])
		}
	}
	return s
}
