package main

import (
	"net/http"
	"sync/atomic"
)

// ---------- 主题（暗色/亮色） ----------

const themeCookie = "diskmon_theme"

var uiTheme atomic.Value // "dark" | "light"

func init() { uiTheme.Store("dark") }

func themeFromRequest(r *http.Request) string {
	if c, err := r.Cookie(themeCookie); err == nil && c.Value == "light" {
		return "light"
	}
	return "dark"
}

func currentTheme() string {
	if v, ok := uiTheme.Load().(string); ok && v != "" {
		return v
	}
	return "dark"
}

// 亮色主题：覆盖常用选择器（原样式是写死的深色，这里做变量式覆盖）
const lightCSS = `
html{color-scheme:light}
body{background:#f5f7fb!important;color:#1c2330!important}
a{color:#2563eb!important}
.wrap{color:#1c2330}
.nav,.subnav{background:#ffffff!important;border-color:#e3e8f0!important}
.nav a,.subnav a{color:#5b6474!important}
.nav a.on,.subnav a.on{background:#e8eefc!important;color:#1d4ed8!important}
.nav .brand{color:#1c2330!important}
.card,.tile,.side{background:#ffffff!important;border-color:#e3e8f0!important;color:#1c2330!important}
.tile:hover{border-color:#93b4f5!important}
.muted,.tile .d,.rec .why{color:#5b6474!important}
h1,h2,h3{color:#111827!important}
.kpi .big,.big{color:#111827!important}
table{color:#1c2330}
th{color:#5b6474!important;border-color:#e3e8f0!important}
td{border-color:#eef1f6!important}
code{color:#334155!important;background:#f1f5f9;border-radius:4px;padding:1px 4px}
.btn,.act{background:#f1f5f9!important;border-color:#d7dfea!important;color:#1c2330!important}
.btn:hover,.act:hover{border-color:#93b4f5!important}
.warn{background:#fff7ed!important;border-color:#fdba7455!important;color:#7c2d12!important}
.ok{background:#ecfdf5!important;border-color:#6ee7b755!important;color:#065f46!important}
.empty{color:#5b6474!important}
.ring .inner{background:#ffffff!important}
.pill{border-color:#d7dfea!important}
.rc{background:#ffffff!important;border-color:#e3e8f0!important}
.rc .path,.rc .risk,.rc .row{color:#5b6474!important}
.rc .n,.rc .sz{color:#111827!important}
.rec{background:linear-gradient(180deg,#ecfdf5,#ffffff)!important;border-color:#6ee7b755!important}
.tsearch input,input,textarea{background:#ffffff!important;border-color:#d7dfea!important;color:#1c2330!important}
.brow .btrack{background:#e8edf5!important}
th[style],td[style]{color:inherit}
.nav a,.nav a:visited,.subnav a,.subnav a:visited{color:#334155!important}
.nav a.on,.subnav a.on{color:#1d4ed8!important}
.nav .brand{color:#111827!important}

`
