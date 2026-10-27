package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// ---------- 稳定性：自证与自愈 ----------

var (
	startedAt = time.Now()
	httpReqs  int64
	httpPanics int64
	usnRestarts int64
	scanFails   int64
	notifyFails int64
	panics      int64
)

func inc(p *int64) { atomic.AddInt64(p, 1) }
func get(p *int64) int64 { return atomic.LoadInt64(p) }

// guard 包住一个后台循环：panic 不会带走整个进程，5 秒后重启
func guard(name string, fn func()) {
	for {
		func() {
			defer func() {
				if r := recover(); r != nil {
					inc(&panics)
					logf("PANIC in %s: %v\n%s", name, r, debug.Stack())
				}
			}()
			fn()
		}()
		logf("supervisor: %s 退出，5 秒后重启", name)
		time.Sleep(5 * time.Second)
	}
}

type Health struct {
	Version    string            `json:"version"`
	UptimeSec  int64             `json:"uptime_sec"`
	RSSMB      float64           `json:"rss_mb"`
	Goroutines int               `json:"goroutines"`
	Engine     string            `json:"engine"`
	EngineRun  bool              `json:"engine_running"`
	ScanTS     int64             `json:"last_scan"`
	AlertTS    int64             `json:"last_alert"`
	DataDir    string            `json:"data_dir"`
	DataDirFallback  bool         `json:"data_dir_fallback"`
	DataDirRequested string       `json:"data_dir_requested"`
	Counters   map[string]int64  `json:"counters"`
	Scans      int               `json:"snapshots"`
}

func healthSnapshot(cfg Config, st *Store) Health {
	h := Health{
		Version: version, UptimeSec: int64(time.Since(startedAt).Seconds()),
		RSSMB: memMB(), Goroutines: runtime.NumGoroutine(),
		DataDir: cfg.DataDir, DataDirFallback: dataDirFallback, DataDirRequested: dataDirRequested,
		Counters: map[string]int64{
			"http_requests": get(&httpReqs),
			"http_panics":   get(&httpPanics),
			"usn_restarts":  get(&usnRestarts),
			"scan_fails":    get(&scanFails),
			"notify_fails":  get(&notifyFails),
			"panics":        get(&panics),
		},
	}
	eng := st.Engine()
	h.Engine, h.EngineRun = eng.Engine, eng.Running
	if sc := st.GetScan(); sc != nil {
		h.ScanTS = sc.TS
	}
	if a := st.Alerts(); len(a) > 0 {
		h.AlertTS = a[len(a)-1].TS
	}
	h.Scans = len(loadSnapshots(cfg))
	return h
}

func (h Health) JSON() string {
	b, _ := json.Marshal(h)
	return string(b)
}

func (h Health) Text() string {
	st := "未运行"
	if h.EngineRun {
		st = "运行中"
	}
	return fmt.Sprintf("版本 %s · 已运行 %s · 内存 %.1f MB · goroutine %d\n引擎 %s（%s）\n数据目录 %s\n告警 %d · 请求 %d · 吞掉的 panic %d · USN 重启 %d",
		h.Version, durText(h.UptimeSec), h.RSSMB, h.Goroutines, h.Engine, st, h.DataDir,
		h.Counters["panics"]+h.Counters["http_panics"], h.Counters["http_requests"],
		h.Counters["panics"]+h.Counters["http_panics"], h.Counters["usn_restarts"])
}

func durText(sec int64) string {
	if sec < 60 {
		return fmt.Sprintf("%d 秒", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%d 分钟", sec/60)
	}
	if sec < 86400 {
		return fmt.Sprintf("%d 小时 %d 分", sec/3600, (sec%3600)/60)
	}
	return fmt.Sprintf("%d 天 %d 小时", sec/86400, (sec%86400)/3600)
}

// ---------- 日志轮转 ----------

func rotateLog(dir string) {
	p := filepath.Join(dir, "diskmon-go.log")
	st, err := os.Stat(p)
	if err != nil || st.Size() < 4<<20 {
		return
	}
	_ = os.Rename(p, p+".1") // 只保留一份历史，避免无限增长
}

// 原子写小文件（procs.json / rules-stats.json 等）
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func showHealth() {
	text := healthSnapshot(getConfig(), curStore).Text()
	title, _ := syscall.UTF16PtrFromString("diskmon 自检")
	body, _ := syscall.UTF16PtrFromString(text)
	procMessageBox.Call(0, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(title)), 0x40)
}
