package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func dupLeftRoutes(cfg Config, st *Store, q *Quarantine, w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/dup":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(dupPage(cfg, st)))
		return true
	case "/dup/scan":
		minMB := 4
		if v := r.URL.Query().Get("min"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 4096 {
				minMB = n
			}
		}
		startDupScan(cfg, st, cfg.Root, minMB)
		http.Redirect(w, r, "/dup", http.StatusSeeOther)
		return true
	case "/dup/apply":
		kind, hash, ok := takeTicketKind(r.URL.Query().Get("n"))
		var b strings.Builder
		if !ok || kind != "dup" {
			b.WriteString(`<div class="warn">票据无效或已过期，请重新打开重复文件页。</div>`)
		} else {
			n, freed, fails := dupApplyGroup(cfg, st, q, hash, true)
			b.WriteString(fmt.Sprintf(`<div class="ok">已隔离 <b>%d</b> 份副本，释放 <b>%s</b>（每组保留第一份，可在看板还原）</div>`, n, human(freed)))
			if len(fails) > 0 {
				b.WriteString(fmt.Sprintf(`<div class="warn">%d 个失败：<br>`, len(fails)))
				for i, f := range fails {
					if i >= 5 {
						break
					}
					b.WriteString("<code>" + htmlEscape(f) + "</code><br>")
				}
				b.WriteString("</div>")
			}
		}
		b.WriteString(`<a class="btn" href="/dup">返回重复文件</a>`)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(pageShell("重复文件隔离结果", b.String())))
		return true
	case "/leftovers":
		if r.URL.Query().Get("refresh") == "1" {
			leftoversInvalidate()
			leftoversCached(getConfig(), st)
			http.Redirect(w, r, "/leftovers", http.StatusSeeOther)
			return true
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(leftoversPage(cfg, st)))
		return true
	}
	return false
}
