package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- 并行扫描 + 合并 ----------

// 每个顶层子树一个 worker。Windows 盘 I/O 并行收益有限，3 个比较稳。
const scanWorkers = 3

var lastScanSeq atomic.Int64

func scanRootParallel(cfg Config) *Scan {
	entries, err := os.ReadDir(cfg.Root)
	if err != nil {
		return scanVolume(cfg, cfg.Root)
	}
	var children []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), "$") { // $Recycle.Bin 等
			continue
		}
		p := filepath.Join(cfg.Root, e.Name()) + string(os.PathSeparator) // 子扫描根必须带分隔符，否则拼出的路径会缺反斜杠
		if info, ierr := e.Info(); ierr == nil && isReparse(info) {
			continue // 顶层 junction/symlink 不扫
		}
		children = append(children, p)
	}
	if len(children) < 2 {
		return scanVolume(cfg, cfg.Root)
	}

	parts := make([]*Scan, len(children))
	sem := make(chan struct{}, scanWorkers)
	var wg sync.WaitGroup
	for i, c := range children {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if scanCancelled() {
				return
			}
			parts[i] = scanVolume(cfg, path)
		}(i, c)
	}
	wg.Wait()

	parts = compactScans(parts)
	merged := mergeScans(cfg.Root, parts)
	// 根目录自身也算一个目录
	merged.Dirs++
	return merged
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
		Cats: map[string]uint64{}, Owners: map[string]uint64{},
		Protected: map[string]uint64{}, DayWrites: map[string]uint64{},
	}
	for _, p := range parts {
		m.Total += p.Total
		m.Files += p.Files
		m.Dirs += p.Dirs
		m.Errors += p.Errors
		m.Used += p.Used
		m.Free += p.Free
		for k, v := range p.Cats {
			m.Cats[k] += v
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
