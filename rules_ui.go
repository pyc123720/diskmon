package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ---------- 规则编辑 UI ----------

func findRule(cfg Config, name string) (CleanRule, bool) {
	for _, r := range cfg.Rules {
		if r.Name == name {
			return r, true
		}
	}
	return CleanRule{}, false
}

func rulesPage(cfg Config, st *Store, editName string) string {
	var b strings.Builder
	b.WriteString("<h2>自动清理规则（L2）</h2>")
	b.WriteString(`<p style="color:#8b94a7">只允许「可清」类目录（临时文件 / 开发缓存 / 更新缓存 / 日志）。
删除是永久的；建议先开试运行。执行窗口留空表示全天。</p>`)

	stats := st.allRuleStats()
	if len(cfg.Rules) == 0 {
		b.WriteString(`<div class="warn">还没有规则，用下面的表单添加第一条。</div>`)
	}
	for _, r := range cfg.Rules {
		s := stats[r.Name]
		state := "停用"
		if r.Enabled {
			state = "已启用（真实删除）"
			if r.DryRun {
				state = "试运行中（只记录）"
			}
		}
		win := "全天"
		if r.WindowStart != "" && r.WindowEnd != "" {
			win = r.WindowStart + "–" + r.WindowEnd
		}
		b.WriteString(fmt.Sprintf("<h3 style='margin-top:20px'>%s　<span style='color:#8b94a7;font-size:13px'>%s</span></h3>",
			htmlEscape(r.Name), state))
		b.WriteString("<p><code>" + htmlEscape(r.Path) + "</code><br>")
		b.WriteString(fmt.Sprintf("阈值 %d MB · 保留 %d 天 · 每 %d 分钟 · 执行窗口 %s", r.MaxSizeMB, r.KeepDays, r.IntervalMin, win))
		if s.Runs > 0 {
			b.WriteString(fmt.Sprintf("<br>上次：%s　累计释放 %s", htmlEscape(s.LastNote), human(s.TotalFreed)))
		}
		b.WriteString("</p>")
		b.WriteString(`<a class="btn" href="/rules/run?name=` + url.QueryEscape(r.Name) + `&mode=dry">立即试运行</a>`)
		if r.Enabled && !r.DryRun {
			b.WriteString(`<a class="btn" style="border-color:#f5486a;color:#ff8fa3" href="/rules/run?name=` + url.QueryEscape(r.Name) + `&mode=real">立即执行</a>`)
		}
		b.WriteString(`<a class="btn" href="/rules?edit=` + url.QueryEscape(r.Name) + `">编辑</a>`)
		b.WriteString(`<a class="btn" href="/rules/delete?name=` + url.QueryEscape(r.Name) + `">删除</a>`)
		if r.Enabled {
			b.WriteString(`<a class="btn" href="/rules/toggle?name=` + url.QueryEscape(r.Name) + `&to=off">停用</a>`)
		} else {
			b.WriteString(`<a class="btn" href="/rules/toggle?name=` + url.QueryEscape(r.Name) + `&to=dry">开启试运行</a>`)
		}
		if r.Enabled && r.DryRun {
			b.WriteString(`<a class="btn" style="border-color:#f5486a;color:#ff8fa3" href="/rules/toggle?name=` + url.QueryEscape(r.Name) + `&to=on&go=1">确认启用（真实删除）</a>`)
		}
	}

	// 表单
	editing, ok := findRule(cfg, editName)
	title := "新建规则"
	if ok {
		title = "编辑规则：" + editing.Name
	}
	val := func(f func(CleanRule) string) string {
		if !ok {
			return ""
		}
		return htmlEscape(f(editing))
	}
	num := func(f func(CleanRule) int, def int) string {
		if !ok {
			return strconv.Itoa(def)
		}
		return strconv.Itoa(f(editing))
	}
	checked := func(c bool) string {
		if c {
			return " checked"
		}
		return ""
	}
	b.WriteString(`<h3 style="margin-top:28px">` + htmlEscape(title) + `</h3>`)
	b.WriteString(`<form method="post" action="/rules/save" style="max-width:680px">`)
	// 编辑态必须把「我正在改哪一条」带回后端。少了这个字段，saveRuleFromValues
	// 会把这次保存当成新建，一头撞进同名判据——界面只报「已存在同名规则」，
	// 而那条规则一个字都改不了。字段名要和 serve.go 里读的 editing 对上。
	if ok {
		b.WriteString(`<input type="hidden" name="editing" value="` + htmlEscape(editing.Name) + `">`)
	}
	b.WriteString(`
<div class="muted" style="margin:6px 0">名称</div>
<input name="name" required value="` + val(func(r CleanRule) string { return r.Name }) + `" style="width:100%;padding:8px;background:#1c212b;border:1px solid #2a3240;border-radius:8px;color:#e7ebf2">
<div class="muted" style="margin:12px 0 6px">目录（必须存在，且属「可清」类）</div>
<input name="path" required value="` + val(func(r CleanRule) string { return r.Path }) + `" placeholder="C:\Users\...\AppData\Local\Temp" style="width:100%;padding:8px;background:#1c212b;border:1px solid #2a3240;border-radius:8px;color:#e7ebf2">
<div style="display:grid;grid-template-columns:1fr 1fr 1fr;gap:12px;margin-top:12px">
  <div><div class="muted">阈值 MB</div><input name="max_size_mb" type="number" min="1" value="` + num(func(r CleanRule) int { return r.MaxSizeMB }, 2048) + `" style="width:100%;padding:8px;background:#1c212b;border:1px solid #2a3240;border-radius:8px;color:#e7ebf2"></div>
  <div><div class="muted">只删 N 天前</div><input name="keep_days" type="number" min="1" value="` + num(func(r CleanRule) int { return r.KeepDays }, 7) + `" style="width:100%;padding:8px;background:#1c212b;border:1px solid #2a3240;border-radius:8px;color:#e7ebf2"></div>
  <div><div class="muted">检查间隔（分钟）</div><input name="interval_min" type="number" min="5" value="` + num(func(r CleanRule) int { return r.IntervalMin }, 60) + `" style="width:100%;padding:8px;background:#1c212b;border:1px solid #2a3240;border-radius:8px;color:#e7ebf2"></div>
</div>
<div style="display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-top:12px">
  <div><div class="muted">执行窗口开始（HH:MM，可空）</div><input name="window_start" value="` + val(func(r CleanRule) string { return r.WindowStart }) + `" placeholder="02:00" style="width:100%;padding:8px;background:#1c212b;border:1px solid #2a3240;border-radius:8px;color:#e7ebf2"></div>
  <div><div class="muted">执行窗口结束（HH:MM，可空）</div><input name="window_end" value="` + val(func(r CleanRule) string { return r.WindowEnd }) + `" placeholder="06:00" style="width:100%;padding:8px;background:#1c212b;border:1px solid #2a3240;border-radius:8px;color:#e7ebf2"></div>
</div>
<div style="margin-top:14px">
  <label style="margin-right:18px"><input type="checkbox" name="enabled" value="1"` + checked(ok && editing.Enabled) + `> 启用</label>
  <label><input type="checkbox" name="dry_run" value="1"` + checked(!ok || editing.DryRun) + `> 试运行（只记录，不删除）</label>
</div>
<div class="muted" style="margin-top:8px">通过表单保存时永远是“试运行”；要真实删除，请在列表里点「确认启用（真实删除）」。</div>
<div style="margin-top:16px"><button class="btn" type="submit" style="cursor:pointer">保存规则</button>
<a class="btn" href="/rules">取消</a></div>
</form>`)
	return pageShell("自动清理规则", b.String())
}

// 表单校验 + 保存
func saveRuleFromValues(vals url.Values, editing string) error {
	cfg := getConfig()
	name := strings.TrimSpace(vals.Get("name"))
	path := os.ExpandEnv(strings.TrimSpace(vals.Get("path")))
	if name == "" || path == "" {
		return fmt.Errorf("名称和目录都不能为空")
	}
	path = filepath.Clean(path)
	st, err := os.Stat(path)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("目录不存在或不可读：%s", path)
	}
	rule := CleanRule{Name: name, Path: path}
	if why, ok := ruleGuard(cfg, rule); !ok {
		return fmt.Errorf("目录不允许用于自动清理：%s", why)
	}
	atoi := func(k string, def, min, max int) int {
		n, err := strconv.Atoi(strings.TrimSpace(vals.Get(k)))
		if err != nil || n < min || n > max {
			return def
		}
		return n
	}
	rule.MaxSizeMB = atoi("max_size_mb", 2048, 1, 1000000)
	rule.KeepDays = atoi("keep_days", 7, 1, 3650)
	rule.IntervalMin = atoi("interval_min", 60, 5, 10080)
	rule.Enabled = vals.Get("enabled") == "1"
	rule.DryRun = vals.Get("dry_run") == "1"
	if rule.Enabled && !rule.DryRun && vals.Get("confirm_delete") != "1" {
		// 真实删除必须显式确认（前端表单没有这个字段时一律降级为试运行）
		rule.DryRun = true
	}
	ws := strings.TrimSpace(vals.Get("window_start"))
	we := strings.TrimSpace(vals.Get("window_end"))
	if (ws == "") != (we == "") {
		return fmt.Errorf("执行窗口的起止时间要同时填写或同时留空")
	}
	if ws != "" {
		if !validClock(ws) || !validClock(we) {
			return fmt.Errorf("执行窗口格式应为 HH:MM（例如 02:00）")
		}
		rule.WindowStart, rule.WindowEnd = ws, we
	}

	cfgMu.Lock()
	defer cfgMu.Unlock()
	// 同名规则：编辑时替换，新建时拒绝
	idx := -1
	for i := range liveConfig.Rules {
		if liveConfig.Rules[i].Name == name {
			idx = i
			break
		}
	}
	if editing != "" && editing != name {
		return fmt.Errorf("不支持改名，请删除后重建")
	}
	if editing == "" && idx >= 0 {
		return fmt.Errorf("已存在同名规则：%s", name)
	}
	if idx >= 0 {
		liveConfig.Rules[idx] = rule
	} else {
		liveConfig.Rules = append(liveConfig.Rules, rule)
	}
	if !saveConfig(liveConfig) {
		return fmt.Errorf("写入 config.json 失败")
	}
	return nil
}

func deleteRule(name string) error {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	var kept []CleanRule
	found := false
	for _, r := range liveConfig.Rules {
		if r.Name == name {
			found = true
			continue
		}
		kept = append(kept, r)
	}
	if !found {
		return fmt.Errorf("找不到规则：%s", name)
	}
	liveConfig.Rules = kept
	if !saveConfig(liveConfig) {
		return fmt.Errorf("写入 config.json 失败")
	}
	return nil
}

func validClock(s string) bool {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return false
	}
	return h >= 0 && h <= 23 && m >= 0 && m <= 59
}

func ruleRunPage(res RuleResult, real bool) string {
	var b strings.Builder
	title := "试运行结果"
	if real {
		title = "执行结果"
	}
	b.WriteString("<h2>" + title + "：" + htmlEscape(res.Name) + "</h2>")
	b.WriteString("<p><code>" + htmlEscape(res.Path) + "</code></p>")
	if res.Skipped {
		b.WriteString(`<div class="warn">跳过：` + htmlEscape(res.Reason) + `</div>`)
		b.WriteString(`<a class="btn" href="/rules">返回规则</a>`)
		return pageShell(title, b.String())
	}
	b.WriteString(fmt.Sprintf("<p>目录当前 %s</p>", human(res.DirSize)))
	if real {
		b.WriteString(fmt.Sprintf(`<div class="ok">已永久删除 <b>%d</b> 个文件，释放 <b>%s</b></div>`, res.Removed, human(res.Freed)))
	} else {
		b.WriteString(fmt.Sprintf(`<div class="ok">试运行：<b>%d</b> 个文件符合条件，可释放 <b>%s</b>（未删除任何文件）</div>`, res.Removed, human(res.WouldFree)))
	}
	if res.Locked > 0 {
		b.WriteString(fmt.Sprintf(`<div class="muted">%d 个文件被占用而跳过</div>`, res.Locked))
	}
	b.WriteString(`<a class="btn" href="/rules">返回规则</a>`)
	return pageShell(title, b.String())
}
