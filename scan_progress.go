package main

import (
	"fmt"
	"strings"
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

	scanExpected atomic.Int64

	scanRecentMu sync.Mutex
	scanRecent   []string
	scanCur      atomic.Value // string
)

var scanProg scanState

// scanTouch 记录"当前正在扫的文件"，供进度页滚动展示。
// 只在每 N 个文件调用一次，避免每次文件都抢锁。
func scanTouch(p string) {
	scanCur.Store(p)
	scanRecentMu.Lock()
	scanRecent = append(scanRecent, p)
	if len(scanRecent) > 10 {
		scanRecent = scanRecent[len(scanRecent)-10:]
	}
	scanRecentMu.Unlock()
}

func scanRecentList() []string {
	scanRecentMu.Lock()
	defer scanRecentMu.Unlock()
	out := make([]string, len(scanRecent))
	copy(out, scanRecent)
	return out
}

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
	scanRecentMu.Lock()
	scanRecent = nil
	scanRecentMu.Unlock()
	scanCur.Store("")
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
	Running   bool     `json:"running"`
	Root      string   `json:"root"`
	Files     int      `json:"files"`
	Dirs      int      `json:"dirs"`
	Bytes     uint64   `json:"bytes"`
	Errors    int      `json:"errors"`
	Elapsed   float64  `json:"elapsed"`
	Pct       float64  `json:"pct"`
	Cancelled bool     `json:"cancelled"`
	Current   string   `json:"current"`
	Recent    []string `json:"recent"`
	Rate      float64  `json:"rate"` // 文件/秒
	MBps      float64  `json:"mbps"` // MB/秒
	ETA       float64  `json:"eta"`  // 预计剩余秒
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
	if v, ok := scanCur.Load().(string); ok {
		p.Current = v
	}
	p.Recent = scanRecentList()
	if p.Elapsed > 0.5 {
		p.Rate = float64(p.Files) / p.Elapsed
		p.MBps = float64(p.Bytes) / p.Elapsed / (1 << 20)
	}
	if p.Running {
		if exp := int(scanExpected.Load()); exp > 0 && p.Files > 0 {
			// 用上次扫描的文件总数更稳：早期扫到大文件时字节占比会虚高
			p.Pct = float64(p.Files) / float64(exp) * 100
			if p.Pct > 99 {
				p.Pct = 99
			}
			if p.Pct > 0.5 {
				p.ETA = p.Elapsed * (float64(exp) - float64(p.Files)) / float64(p.Files)
			}
		} else if p.Root != "" {
			if v, err := volumeStats(p.Root); err == nil && v.Used > 0 {
				p.Pct = float64(p.Bytes) / float64(v.Used) * 100
				if p.Pct > 99 {
					p.Pct = 99
				}
				if p.Pct > 0.5 {
					p.ETA = p.Elapsed * (100 - p.Pct) / p.Pct
				}
			}
		}
	}
	return p
}

// 页面标签（中英）
func L(zh, en string) string {
	if isEN() {
		return en
	}
	return zh
}

func scanPageHTML() string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="` + L("zh-CN", "en") + `"><head><meta charset="utf-8">
<title>` + L("扫描中", "Scanning") + ` · diskmon</title>
<style>
:root{--bg:#0e1116;--card:#151a22;--line:#232a36;--fg:#e7ebf2;--muted:#8b94a7;--green:#46e08a;--blue:#4c8dff}
*{box-sizing:border-box}
body{margin:0;background:radial-gradient(1200px 500px at 50% -200px,#17202b 0%,var(--bg) 60%);color:var(--fg);
 font:15px/1.7 -apple-system,"Segoe UI","Microsoft YaHei",sans-serif;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:40px}
a{color:var(--blue);text-decoration:none}
.card{width:100%;max-width:760px;background:linear-gradient(180deg,#161c25,#12161d);border:1px solid var(--line);border-radius:18px;padding:34px 34px 26px;box-shadow:0 24px 60px #0008}
.hd{display:flex;align-items:center;gap:10px;margin-bottom:6px}
.dot{width:9px;height:9px;border-radius:50%;background:var(--green);box-shadow:0 0 0 0 #46e08a88;animation:pulse 1.6s infinite}
h2{margin:0;font-size:20px;font-weight:650;letter-spacing:.3px}
.root{color:var(--muted);font-size:13px;margin-bottom:22px;word-break:break-all}
.big{display:flex;align-items:baseline;gap:10px;font-variant-numeric:tabular-nums}
.num{font-size:42px;font-weight:700;letter-spacing:-.5px}
.sub{color:var(--muted);font-size:13px}
.bar{position:relative;height:14px;background:#1b2029;border-radius:999px;overflow:hidden;margin:20px 0 12px;border:1px solid #202734}
.fill{height:100%;width:0;border-radius:999px;background:linear-gradient(90deg,#1f9e63,#46e08a 45%,#7df0b3 70%,#46e08a);transition:width .45s cubic-bezier(.22,.61,.36,1)}
.shine{position:absolute;inset:0;background:linear-gradient(100deg,transparent 20%,#ffffff22 45%,transparent 70%);background-size:220px 100%;animation:shine 1.8s linear infinite;pointer-events:none}
.bar.indet .fill{width:38%!important;animation:slide 1.4s ease-in-out infinite}
.cur{margin:14px 0 2px;font:13px/1.5 ui-monospace,Consolas,monospace;color:#9fb0c7;white-space:nowrap;overflow:hidden;position:relative;height:20px}
.cur span{display:inline-block;white-space:nowrap;animation:marquee 18s linear infinite}
.cur:after{content:"";position:absolute;right:0;top:0;bottom:0;width:50px;background:linear-gradient(90deg,transparent,#12161d)}
.row{display:flex;align-items:center;gap:12px}.bar{flex:1;margin:20px 0 12px}.pct{color:#aebbd0;font:13px ui-monospace,Consolas,monospace;min-width:64px;text-align:right}.files{margin-top:14px;border-top:1px solid var(--line);padding-top:12px;font:12px/1.9 ui-monospace,Consolas,monospace;color:#7f8ca1;height:120px;overflow:hidden}
.files div{white-space:nowrap;overflow:hidden;text-overflow:ellipsis;animation:fadein .35s ease;opacity:.85}
.files div:nth-child(1){color:#c7d3e3}
.stats{display:flex;gap:22px;margin-top:10px;flex-wrap:wrap;color:var(--muted);font-size:13px;font-variant-numeric:tabular-nums}
.stats b{color:#dfe7f1;font-weight:600}
.acts{display:flex;gap:12px;align-items:center;margin-top:22px}
.btn{border:1px solid #2a3240;background:#1b222c;color:#dbe3ee;border-radius:10px;padding:8px 14px;cursor:pointer}
.btn:hover{background:#222a36}
.cancel{color:#ff8fa3;border-color:#f5486a55}
@keyframes shine{from{background-position:-220px 0}to{background-position:220px 0}}
@keyframes slide{0%{margin-left:-40%}100%{margin-left:100%}}
@keyframes pulse{0%{box-shadow:0 0 0 0 #46e08a99}70%{box-shadow:0 0 0 10px #46e08a00}100%{box-shadow:0 0 0 0 #46e08a00}}
@keyframes marquee{0%{transform:translateX(0)}100%{transform:translateX(-45%)}}
@keyframes fadein{from{opacity:0;transform:translateY(3px)}to{opacity:1;transform:none}}
@media (max-width:640px){.num{font-size:32px}body{padding:18px}}
</style></head><body><div class="card">
<div class="hd"><span class="dot"></span><h2>` + L("正在扫描", "Scanning") + `</h2></div>
<div class="root" id="root">` + L("正在启动…", "starting…") + `</div>
<div class="big"><span class="num" id="files">0</span><span class="sub">` + L("个文件", "files") + `</span></div>
<div class="row"><div class="bar" id="bar"><div class="fill" id="fill"></div><div class="shine"></div></div><span class="pct" id="pct">–</span></div>
<div class="cur"><span id="cur"></span></div>
<div class="stats">
  <span>` + L("目录", "folders") + ` <b id="dirs">0</b></span>
  <span>` + L("已扫", "size") + ` <b id="bytes">0 GB</b></span>
  <span>` + L("速度", "speed") + ` <b id="rate">–</b></span>
  <span>` + L("已用", "elapsed") + ` <b id="elapsed">0s</b></span>
  <span>` + L("预计剩余", "eta") + ` <b id="eta">–</b></span>
</div>
<div class="files" id="filesList"></div>
<div class="acts">
  <a class="btn" href="/">` + L("后台扫描，先看别的", "Scan in background") + `</a>
  <a class="btn cancel" href="#" onclick="fetch('/scan/cancel').then(function(){location='/'});return false">` + L("取消扫描", "Cancel scan") + `</a>
  <span class="sub" style="margin-left:auto">` + L("只读扫描，不会改动任何文件", "read-only, changes nothing") + `</span>
</div>
</div>
<script>
var lastFiles = 0, lastT = 0;
function fmtBytes(b){ if(b>1073741824) return (b/1073741824).toFixed(1)+' GB'; return (b/1048576).toFixed(0)+' MB'; }
function fmtEta(s){ if(!s||s<0) return '–'; if(s<60) return Math.round(s)+'s'; var m=Math.floor(s/60); return m+'m '+Math.round(s%60)+'s'; }
function setText(id,v){ var e=document.getElementById(id); if(e && e.textContent!==v) e.textContent=v; }
function tick(){
  fetch('/api/scan/progress').then(function(r){return r.json()}).then(function(p){
    if(!p.running){ location='/'; return; }
    setText('root', p.root);
    var n = p.files|0;
    // 数字平滑滚动
    if(lastFiles === 0){ setText('files', n.toLocaleString()); lastFiles = n; }
    else if(n !== lastFiles){ animateNum('files', lastFiles, n); lastFiles = n; }
    setText('dirs', (p.dirs|0).toLocaleString());
    setText('bytes', fmtBytes(p.bytes));
    setText('rate', (p.rate|0).toLocaleString() + ' f/s · ' + (p.mbps||0).toFixed(0) + ' MB/s');
    setText('elapsed', Math.round(p.elapsed)+'s');
    setText('eta', fmtEta(p.eta));
    document.getElementById('pct').textContent = (p.pct||0).toFixed(0)+'%'; document.getElementById('fill').style.width = (p.pct||0).toFixed(1)+'%';
    document.getElementById('bar').classList.toggle('indet', !p.pct || p.pct < 0.8);
    if(p.current){
      var c = document.getElementById('cur');
      c.textContent = p.current;
      c.style.animationDuration = Math.max(10, Math.min(40, (p.current.length||30)/3))+'s';
    }
    if(p.recent && p.recent.length){
      var html='';
      var list = p.recent.slice().reverse();
      for(var i=0;i<Math.min(list.length,7);i++){ html += '<div>'+list[i].replace(/&/g,'&amp;').replace(/</g,'&lt;')+'</div>'; }
      document.getElementById('filesList').innerHTML = html;
    }
  }).catch(function(){});
}
function animateNum(id, from, to){
  var el = document.getElementById(id), start = performance.now(), dur = 400;
  function step(t){
    var k = Math.min(1,(t-start)/dur), v = Math.round(from + (to-from)*k);
    el.textContent = v.toLocaleString();
    if(k<1) requestAnimationFrame(step);
  }
  requestAnimationFrame(step);
}
tick(); setInterval(tick, 400);
</script></body></html>`)
	return b.String()
}

var _ = fmt.Sprintf
