package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- 扫描进度 ----------

type scanState struct {
	mu      sync.Mutex
	running bool
	root    string
	errs    int
	started time.Time
	cancel  atomic.Bool
}

var (
	scanFilesCnt atomic.Int64
	scanDirsCnt  atomic.Int64
	scanBytesCnt atomic.Uint64
)

var scanProg scanState

func scanBegin(root string) {
	scanProg.mu.Lock()
	scanProg.running = true
	scanProg.root = root
	scanFilesCnt.Store(0)
	scanDirsCnt.Store(0)
	scanBytesCnt.Store(0)
	scanProg.errs = 0
	scanProg.started = time.Now()
	scanProg.cancel.Store(false)
	scanProg.mu.Unlock()
}

func scanEnd() {
	scanProg.mu.Lock()
	scanProg.running = false
	scanProg.mu.Unlock()
}

// scanAdd 累加进度（并行扫描时多个 worker 同时调用）
func scanAdd(files, dirs int, bytes uint64) {
	if files != 0 {
		scanFilesCnt.Add(int64(files))
	}
	if dirs != 0 {
		scanDirsCnt.Add(int64(dirs))
	}
	if bytes != 0 {
		scanBytesCnt.Add(bytes)
	}
}

func scanError() {
	scanProg.mu.Lock()
	scanProg.errs++
	scanProg.mu.Unlock()
}

func scanCancelled() bool { return scanProg.cancel.Load() }
func cancelScan() bool {
	scanProg.mu.Lock()
	running := scanProg.running
	scanProg.mu.Unlock()
	if running {
		scanProg.cancel.Store(true)
	}
	return running
}

type scanProgress struct {
	Running bool    `json:"running"`
	Root    string  `json:"root"`
	Files   int     `json:"files"`
	Dirs    int     `json:"dirs"`
	Bytes   uint64  `json:"bytes"`
	Errors  int     `json:"errors"`
	Elapsed float64 `json:"elapsed"`
	Pct     float64 `json:"pct"`
	Cancelled bool  `json:"cancelled"`
}

func scanProgressNow() scanProgress {
	scanProg.mu.Lock()
	p := scanProgress{
		Running: scanProg.running,
		Root:    scanProg.root,
		Files:   int(scanFilesCnt.Load()),
		Dirs:    int(scanDirsCnt.Load()),
		Bytes:   scanBytesCnt.Load(),
		Errors:  scanProg.errs,
	}
	if !scanProg.started.IsZero() {
		p.Elapsed = time.Since(scanProg.started).Seconds()
	}
	scanProg.mu.Unlock()
	p.Cancelled = scanProg.cancel.Load()
	if p.Running && p.Root != "" {
		if v, err := volumeStats(p.Root); err == nil && v.Used > 0 {
			p.Pct = float64(p.Bytes) / float64(v.Used) * 100
			if p.Pct > 99 {
				p.Pct = 99
			}
		}
	}
	return p
}

func scanPageHTML() string {
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<title>扫描中 · diskmon</title>
<style>body{background:#0e1116;color:#e7ebf2;font:15px/1.8 -apple-system,"Segoe UI","Microsoft YaHei",sans-serif;padding:60px;max-width:760px;margin:0 auto}
a{color:#4c8dff;text-decoration:none}
.bar{height:12px;background:#1c212b;border-radius:8px;overflow:hidden;margin:18px 0 10px}
.fill{height:100%;width:0;background:linear-gradient(90deg,#2fbf6e,#46e08a);transition:width .6s}
.muted{color:#8b94a7;font-size:13px}
.cancel{display:inline-block;margin-left:14px;color:#ff8fa3;border:1px solid #f5486a55;border-radius:8px;padding:6px 12px}
</style></head><body>
<h2>正在扫描 <span id="root">…</span></h2>
<div class="bar"><div class="fill" id="fill"></div></div>
<div id="stat" class="muted">正在启动…</div>
<p class="muted">只读扫描，不会改动任何文件。首次通常 1–2 分钟。</p>
<p><a href="/">← 后台扫描，先看别的</a><a class="cancel" href="#" onclick="fetch('/scan/cancel').then(function(){location='/'});return false">取消扫描</a></p>
<script>
function tick(){
  fetch('/api/scan/progress').then(function(r){return r.json()}).then(function(p){
    if(!p.running){ location='/'; return; }
    document.getElementById('fill').style.width=(p.pct||0).toFixed(1)+'%';
    document.getElementById('root').textContent=p.root;
    document.getElementById('stat').textContent=
      (p.bytes/1073741824).toFixed(1)+' GB · '+p.files+' 文件 · '+p.dirs+' 目录 · '+Math.round(p.elapsed)+' 秒';
  }).catch(function(){});
}
setInterval(tick,1000); tick();
</script></body></html>`
}

var _ = fmt.Sprintf
