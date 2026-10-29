package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// ---------- 并行扫描 + 合并 ----------

// scanWorkers 是 scanVolume 的并发上限，由 ScanRootCached 的 sem 使用。
// 注意：真正决定并发的是 ScanRootCached（按顶层子树并发），不是这里。
const scanWorkers = 3

var lastScanSeq atomic.Int64

// scanJob 是一个可被 worker 抢占的扫描任务。
// parent 记住它属于哪个顶层子树——结果要按子树归并回去，缓存键才不会失效。
type scanJob struct {
	path   string
	parent string
}

// buildJobs 把一个顶层子树拆成「它的直接子目录」级任务。
// 拆不动（读不了、或子目录少于 2 个）就退化成扫它自己，避免拆了反而更慢。
func buildJobs(child string) []scanJob {
	subs, err := os.ReadDir(child)
	if err != nil {
		return []scanJob{{path: child, parent: child}}
	}
	jobs := make([]scanJob, 0, len(subs))
	for _, e := range subs {
		if !e.IsDir() {
			continue
		}
		if info, ierr := e.Info(); ierr == nil && isReparse(info) {
			continue
		}
		jobs = append(jobs, scanJob{
			path:   filepath.Join(child, e.Name()) + string(os.PathSeparator),
			parent: child,
		})
	}
	if len(jobs) < 2 {
		return []scanJob{{path: child, parent: child}}
	}
	return jobs
}

// mergeChildParts 把「一个顶层子树的若干分片」归并回一个 Scan，供增量缓存按子树存取。
// 拆成多个任务时子树自身不再作为扫描根出现，所以 Dirs 要补回 1。
func mergeChildParts(child string, parts []*Scan) *Scan {
	if len(parts) == 1 && strings.EqualFold(parts[0].Root, child) {
		return parts[0] // 没拆开，原样返回
	}
	m := mergeScans(child, parts)
	m.Dirs++
	return m
}

// listRootFiles 列出扫描根下的「直接文件」（pagefile.sys / hiberfil.sys / bootmgr 等）。
// `$` 开头的系统伪文件跳过。
func listRootFiles(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), "$") {
			continue
		}
		out = append(out, filepath.Join(root, e.Name()))
	}
	return out
}

// scanRootFiles 只统计扫描根目录下的直接文件，不递归。
func scanRootFiles(cfg Config, paths []string) *Scan {
	if len(paths) == 0 {
		return nil
	}
	sc := &Scan{
		Root: cfg.Root, TS: time.Now().Unix(),
		Cats: map[string]uint64{}, CatsShared: map[string]uint64{},
		Owners:    map[string]uint64{},
		Protected: map[string]uint64{}, DayWrites: map[string]uint64{},
	}
	hl := activeHardlinks()
	var sharedBytes uint64
	rootCat := classify(strings.ToLower(strings.TrimRight(cfg.Root, `\`)) + `\`)
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			sc.Errors++
			continue
		}
		size := uint64(info.Size())
		if size == 0 {
			continue
		}
		sc.Total += size
		sc.Files++
		cat := rootCat
		switch strings.ToLower(filepath.Base(p)) {
		case "hiberfil.sys", "swapfile.sys":
			cat = "system_hiber"
		case "pagefile.sys":
			cat = "system_pagefile"
		case "memory.dmp":
			cat = "logs_dumps"
		}
		sc.Cats[cat] += size
		if size >= 32<<20 {
			sc.TopFiles = append(sc.TopFiles, FileEntry{Path: p, Bytes: size, MTime: info.ModTime().Unix()})
		}
		if hardlinkProbeMin > 0 && size >= hardlinkProbeMin && hl.observe(p, size) {
			sharedBytes += size
			sc.CatsShared[cat] += size
		}
	}
	sc.Shared = sharedBytes
	sc.Unique = uniqueBytes(sc.Total, sc.Shared)
	return sc
}

func compactScans(parts []*Scan) []*Scan {
	out := parts[:0]
	for _, p := range parts {
		if p != nil {
			out = append(out, p)
		}
	}
	return out
}

func mergeScans(root string, parts []*Scan) *Scan {
	m := &Scan{
		Root: root, TS: time.Now().Unix(),
		Cats: map[string]uint64{}, CatsShared: map[string]uint64{},
		Owners:    map[string]uint64{},
		Protected: map[string]uint64{}, DayWrites: map[string]uint64{},
	}
	for _, p := range parts {
		m.Total += p.Total
		m.Files += p.Files
		m.Dirs += p.Dirs
		m.Errors += p.Errors
		m.Used += p.Used
		m.Free += p.Free
		m.Shared += p.Shared
		if p.HlProbeMB > m.HlProbeMB {
			m.HlProbeMB = p.HlProbeMB
		}
		for k, v := range p.Cats {
			m.Cats[k] += v
		}
		for k, v := range p.CatsShared {
			m.CatsShared[k] += v
		}
		for k, v := range p.Owners {
			m.Owners[k] += v
		}
		for k, v := range p.Protected {
			m.Protected[k] += v
		}
		for k, v := range p.DayWrites {
			m.DayWrites[k] += v
		}
		m.Dirs3 = append(m.Dirs3, p.Dirs3...)
		m.Deep = append(m.Deep, p.Deep...)
		m.TopFiles = append(m.TopFiles, p.TopFiles...)
	}
	sort.Slice(m.Dirs3, func(i, j int) bool { return m.Dirs3[i].Bytes > m.Dirs3[j].Bytes })
	if len(m.Dirs3) > 400 {
		m.Dirs3 = m.Dirs3[:400]
	}
	sort.Slice(m.Deep, func(i, j int) bool { return m.Deep[i].Bytes > m.Deep[j].Bytes })
	sort.Slice(m.TopFiles, func(i, j int) bool { return m.TopFiles[i].Bytes > m.TopFiles[j].Bytes })
	if len(m.TopFiles) > 300 {
		m.TopFiles = m.TopFiles[:300]
	}
	m.Unique = uniqueBytes(m.Total, m.Shared)
	return m
}

// ---------- USN 事件计数（用于"无变更秒回"） ----------

var usnSeq atomic.Int64
var lastScanUSN atomic.Int64

func usnEventsSince(scanTS int64) int64 {
	// scanTS 之前的事件不算；这里用序号差近似
	return usnSeq.Load() - lastScanUSN.Load()
}

func usnMark() { lastScanUSN.Store(usnSeq.Load()) }

// usnActive：本进程是否真的在跟踪 USN。没有它就不能判断"无变更"，必须全扫。
var usnActive atomic.Bool
