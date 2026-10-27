package main

import (
	"fmt"
	"net/http"
	"strconv"
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
		if !ok || kind != "dup" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(pageShell("重复文件", `<div class="warn">票据无效或已过期，请重新打开重复文件页。</div><a class="btn" href="/dup">返回重复文件</a>`)))
			return true
		}
		var paths []string
		if res := loadDups(cfg); res != nil {
			for i := range res.Groups {
				if res.Groups[i].Hash == hash {
					paths = append(paths, res.Groups[i].Paths[1:]...)
					break
				}
			}
		}
		var movable []string
		skippedInstall := 0
		for _, p := range paths {
			if isInstallPath(p) {
				skippedInstall++
				continue
			}
			movable = append(movable, p)
		}
		if len(movable) == 0 {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(pageShell("重复文件", `<div class="warn">这一组没有可隔离的副本（可能都在安装目录里）。</div><a class="btn" href="/dup">返回重复文件</a>`)))
			return true
		}
		if !startQuarantineOpReason(cfg, st, movable, fmt.Sprintf("重复文件副本（%d 份，跳过安装目录 %d 份）", len(movable), skippedInstall), "dup") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(pageShell("繁忙", `<div class="warn">已有任务在运行。</div><a class="btn" href="/op">看进度</a>`)))
			return true
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(opPage(cfg, st)))
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
