package main

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// ---------- 设置界面 ----------

func settingsPage(cfg Config, notice string) string {
	var b strings.Builder
	b.WriteString(`<h2 style="margin-top:0">设置</h2>`)
	b.WriteString(`<div class="muted" style="margin-bottom:10px">当前版本一次监测一个盘（默认 C:）。USN 秒级归因跟随这里填的根目录；切换后请重新扫描，两边数据才一致。</div>`)
	if notice != "" {
		b.WriteString(`<div class="ok">` + htmlEscape(notice) + `</div>`)
	}
	b.WriteString(`<p class="muted">改动立即写入 <code>config.json</code>；端口和数据目录需重启托盘生效。规则在「规则」页单独管理。</p>`)

	b.WriteString(`<form method="post" action="/settings/save" style="max-width:760px">`)

	// 监测
	b.WriteString(`<div class="card"><h2>监测</h2>`)
	b.WriteString(`<div class="grid g2">`)
	b.WriteString(fieldText("扫描根目录", "root", cfg.Root, "例如 C:\\"))
	b.WriteString(fieldNum("采样间隔（秒）", "interval_sec", cfg.IntervalSec, 1, 3600))
	b.WriteString(fieldNum("增长告警阈值（MB / 5 秒）", "alert_min_delta_mb", cfg.AlertMinDeltaMB, 1, 1000000))
	b.WriteString(fieldNum("每日自动扫描间隔（小时，0=关闭）", "auto_scan_hours", cfg.AutoScanHours, 0, 720))
	b.WriteString(fieldNum("弹窗通知阈值（MB，低于只记录）", "notify_min_delta_mb", cfg.NotifyMinMB, 0, 1000000))
	b.WriteString(`</div></div>`)

	// 阈值
	b.WriteString(`<div class="card"><h2>低空间阈值</h2><div class="grid g3">`)
	b.WriteString(fieldFloat("警告 剩余 GB", "warn_free_gb", cfg.WarnGB))
	b.WriteString(fieldFloat("危险 剩余 GB", "danger_free_gb", cfg.DangerGB))
	b.WriteString(fieldFloat("紧急 剩余 GB", "critical_free_gb", cfg.CriticalGB))
	b.WriteString(fieldFloat("警告 剩余 %", "warn_free_pct", cfg.WarnPct))
	b.WriteString(fieldFloat("危险 剩余 %", "danger_free_pct", cfg.DangerPct))
	b.WriteString(fieldFloat("紧急 剩余 %", "critical_free_pct", cfg.CriticalPct))
	b.WriteString(`</div></div>`)

	// 隔离区
	b.WriteString(`<div class="card"><h2>隔离区</h2><div class="grid g2">`)
	b.WriteString(fieldNum("保留天数", "quarantine_days", cfg.QuarantineDays, 1, 365))
	b.WriteString(fieldNum("容量上限（GB）", "quarantine_max_gb", cfg.QuarantineMaxGB, 1, 2000))
	b.WriteString(`</div></div>`)

	// 白名单
	b.WriteString(`<div class="card"><h2>白名单</h2>`)
	b.WriteString(`<div class="muted" style="margin-bottom:6px">每行一条，路径片段包含即命中（不区分大小写）。这些目录永远不会被自动清理。</div>`)
	b.WriteString(`<textarea name="whitelist" rows="8" style="width:100%;padding:10px;background:#1c212b;border:1px solid #2a3240;border-radius:10px;color:#e7ebf2;font-family:Consolas,monospace;font-size:12px">`)
	b.WriteString(htmlEscape(strings.Join(cfg.Whitelist, "\n")))
	b.WriteString(`</textarea></div>`)

	// 通知与数据
	b.WriteString(`<div class="card"><h2>通知与数据</h2>`)
	b.WriteString(`<label style="display:block;margin-bottom:8px"><input type="checkbox" name="use_toast" value="1"`)
	if cfg.UseToast {
		b.WriteString(` checked`)
	}
	b.WriteString(`> 使用 Windows 通知（WinRT toast）</label>`)
	b.WriteString(fmt.Sprintf(`<div class="muted">数据目录：<code>%s</code>（改这个要手动编辑 config.json 并重启）</div>`, htmlEscape(cfg.DataDir)))
	b.WriteString(fmt.Sprintf(`<div class="muted">配置文件：<code>%s</code></div>`, htmlEscape(exeDir()+`\config.json`)))
	b.WriteString(`</div>`)

	b.WriteString(`<div style="margin-top:16px"><button class="btn" type="submit" style="cursor:pointer;background:#1f6f4a;border-color:#33c179">保存设置</button>`)
	b.WriteString(`<a class="btn" href="/settings/reset">恢复默认</a>`)
	b.WriteString(`<a class="btn" href="/">返回总览</a></div></form>`)
	return appShell("设置", "settings", b.String())
}

func fieldText(label, name, val, hint string) string {
	return `<div><div class="muted">` + htmlEscape(label) + `</div><input name="` + name + `" value="` + htmlEscape(val) +
		`" placeholder="` + htmlEscape(hint) + `" style="width:100%;padding:8px;background:#1c212b;border:1px solid #2a3240;border-radius:8px;color:#e7ebf2"></div>`
}

func fieldNum(label, name string, val, min, max int) string {
	return fmt.Sprintf(`<div><div class="muted">%s</div><input name="%s" type="number" min="%d" max="%d" value="%d" style="width:100%%;padding:8px;background:#1c212b;border:1px solid #2a3240;border-radius:8px;color:#e7ebf2"></div>`,
		htmlEscape(label), name, min, max, val)
}

func fieldFloat(label, name string, val float64) string {
	return fmt.Sprintf(`<div><div class="muted">%s</div><input name="%s" type="number" step="0.1" min="0" value="%g" style="width:100%%;padding:8px;background:#1c212b;border:1px solid #2a3240;border-radius:8px;color:#e7ebf2"></div>`,
		htmlEscape(label), name, val)
}

// 保存：只改允许的字段，规则与数据目录保留原值
func saveSettingsFromForm(vals url.Values) (notice string, err error) {
	cfgMu.Lock()
	cur := liveConfig
	cfgMu.Unlock()

	next := cur
	atoi := func(k string, def, min, max int) int {
		n, e := strconv.Atoi(strings.TrimSpace(vals.Get(k)))
		if e != nil || n < min || n > max {
			return def
		}
		return n
	}
	atof := func(k string, def float64) float64 {
		f, e := strconv.ParseFloat(strings.TrimSpace(vals.Get(k)), 64)
		if e != nil || f < 0 || f > 100 {
			return def
		}
		return f
	}

	root := strings.TrimSpace(vals.Get("root"))
	if root == "" {
		root = cur.Root
	}
	if !strings.HasSuffix(root, `\`) {
		root += `\`
	}
	if st, e := os.Stat(root); e != nil || !st.IsDir() {
		return "", fmt.Errorf("扫描根目录不存在：%s", root)
	}
	next.Root = root
	next.IntervalSec = atoi("interval_sec", cur.IntervalSec, 1, 3600)
	next.AlertMinDeltaMB = atoi("alert_min_delta_mb", cur.AlertMinDeltaMB, 1, 1000000)
	next.AutoScanHours = atoi("auto_scan_hours", cur.AutoScanHours, 0, 720)
	next.NotifyMinMB = atoi("notify_min_delta_mb", cur.NotifyMinMB, 0, 1000000)
	next.QuarantineDays = atoi("quarantine_days", cur.QuarantineDays, 1, 365)
	next.QuarantineMaxGB = atoi("quarantine_max_gb", cur.QuarantineMaxGB, 1, 2000)

	next.WarnGB = atof("warn_free_gb", cur.WarnGB)
	next.DangerGB = atof("danger_free_gb", cur.DangerGB)
	next.CriticalGB = atof("critical_free_gb", cur.CriticalGB)
	next.WarnPct = atof("warn_free_pct", cur.WarnPct)
	next.DangerPct = atof("danger_free_pct", cur.DangerPct)
	next.CriticalPct = atof("critical_free_pct", cur.CriticalPct)
	if !(next.WarnGB > next.DangerGB && next.DangerGB > next.CriticalGB && next.CriticalGB > 0) {
		return "", fmt.Errorf("GB 阈值必须满足 警告 > 危险 > 紧急 > 0")
	}
	if !(next.WarnPct > next.DangerPct && next.DangerPct > next.CriticalPct && next.CriticalPct > 0) {
		return "", fmt.Errorf("百分比阈值必须满足 警告 > 危险 > 紧急 > 0")
	}

	// 白名单：按行解析，去空、去重
	var wl []string
	seen := map[string]bool{}
	for _, line := range strings.Split(vals.Get("whitelist"), "\n") {
		s := strings.TrimSpace(line)
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		seen[strings.ToLower(s)] = true
		wl = append(wl, s)
	}
	next.Whitelist = wl
	next.UseToast = vals.Get("use_toast") == "1"

	// 端口不在表单里（改了要重启），保留原值
	next.Port = cur.Port
	next.DataDir = cur.DataDir
	next.Rules = cur.Rules

	cfgMu.Lock()
	liveConfig = next
	cfgMu.Unlock()
	if !saveConfig(next) {
		return "", fmt.Errorf("写入 config.json 失败")
	}
	if next.Root != cur.Root {
		notice = "扫描根目录已改，重新扫描后生效"
	}
	return notice, nil
}
