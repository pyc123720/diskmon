package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ---------- 配置 ----------

type Config struct {
	DataDir         string      `json:"data_dir"`
	Root            string      `json:"scan_root"`
	Port            int         `json:"port"`
	IntervalSec     int         `json:"interval_sec"`
	AlertMinDeltaMB int         `json:"alert_min_delta_mb"`
	WarnGB          float64     `json:"warn_free_gb"`
	DangerGB        float64     `json:"danger_free_gb"`
	CriticalGB      float64     `json:"critical_free_gb"`
	WarnPct         float64     `json:"warn_free_pct"`
	DangerPct       float64     `json:"danger_free_pct"`
	CriticalPct     float64     `json:"critical_free_pct"`
	Whitelist       []string    `json:"whitelist"`
	UseToast        bool        `json:"use_toast"` // 用 PowerShell 一次性 toast（默认）；false 则只用气泡
	QuarantineDays  int         `json:"quarantine_days"`
	Rules           []CleanRule `json:"rules"`
	AutoScanHours   int         `json:"auto_scan_hours"`
	QuarantineMaxGB int         `json:"quarantine_max_gb"`
	NotifyMinMB     int         `json:"notify_min_delta_mb"`
	LazyAuto        string      `json:"lazy_auto"`      // off | daily | weekly
	LazyAutoHour    int         `json:"lazy_auto_hour"` // 0-23
	// Winapp2Path 是社区规则文件的位置。留空表示 <数据目录>\winapp2.ini。
	// 它只提供「候选」，真正能不能动由 guardPath 判，执行走隔离区。
	Winapp2Path string `json:"winapp2_path"`
}

func defaultConfig() Config {
	return Config{
		DataDir:         filepath.Join(os.Getenv("LOCALAPPDATA"), "diskmon-go"),
		Root:            `C:\`,
		Port:            8766,
		IntervalSec:     5,
		AlertMinDeltaMB: 120,
		WarnGB:          20, DangerGB: 10, CriticalGB: 5,
		WarnPct: 15, DangerPct: 8, CriticalPct: 4,
		Whitelist:       []string{`\.ssh`, `\.git`, `\documents\`, `\desktop\`, `\source\repos`, `\.env`, `\pictures\`},
		UseToast:        true,
		QuarantineDays:  7,
		AutoScanHours:   24,
		QuarantineMaxGB: 20,
		NotifyMinMB:     500,
		LazyAuto:        "off",
		LazyAutoHour:    3,
	}
}

func loadConfig() Config {
	cfg := defaultConfig()
	p := filepath.Join(exeDir(), "config.json")
	if b, err := os.ReadFile(p); err == nil {
		// PowerShell 的 Set-Content -Encoding UTF8 会带 BOM，json 解析会直接失败
		b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
		if err := json.Unmarshal(b, &cfg); err != nil {
			fmt.Printf("[warn] config.json 解析失败：%v\n", err)
		}
	}
	if !dirWritable(cfg.DataDir) {
		// 备选 1：%LOCALAPPDATA%；备选 2：exe 同级 data 目录（受限环境下唯一可写）
		for _, fallback := range []string{
			filepath.Join(os.Getenv("LOCALAPPDATA"), "diskmon-go"),
			filepath.Join(exeDir(), "data"),
		} {
			if dirWritable(fallback) {
				fmt.Printf("[info] %s 不可写，数据目录降级到 %s\n", cfg.DataDir, fallback)
				dataDirRequested = cfg.DataDir
				dataDirFallback = true
				cfg.DataDir = fallback
				break
			}
		}
	}
	validateConfig(&cfg)
	dataDirActual = cfg.DataDir
	if dataDirRequested == "" {
		dataDirRequested = cfg.DataDir
	}
	// 窗口几何要跨进程共享，所以优先放用户级的固定位置，不能跟着数据目录走。
	//
	// 实情：桌面图标跑的是安装目录的 exe，而真正在出页面的服务可能是另一个目录里的
	// exe。两边数据目录不同，各存一份几何就谁也读不到谁写的——「记住窗口位置」
	// 会永远失效，而且两边看起来各自都正常。
	//
	// 但那个位置不一定写得进去（权限、隔离环境都会变），所以用程序自己的
	// dirWritable 判断，写不进去就退回数据目录——宁可只在单进程内生效，也不要
	// 留一个每次都被拒绝的路径。
	sharedDir := filepath.Join(os.Getenv("LOCALAPPDATA"), "diskmon-go")
	_ = os.MkdirAll(sharedDir, 0o755)
	if dirWritable(sharedDir) {
		uiGeomFile = filepath.Join(sharedDir, "ui-window.json")
	} else {
		uiGeomFile = filepath.Join(cfg.DataDir, "ui-window.json")
		fmt.Printf("[info] %s 不可写，窗口几何改存 %s\n", sharedDir, uiGeomFile)
	}
	rotateLog(cfg.DataDir)
	return cfg
}

func validateConfig(c *Config) {
	if c.IntervalSec < 1 {
		c.IntervalSec = 1
	}
	if c.IntervalSec > 3600 {
		c.IntervalSec = 3600
	}
	if c.AlertMinDeltaMB < 1 {
		c.AlertMinDeltaMB = 1
	}
	if c.Port < 1024 || c.Port > 65535 {
		c.Port = 8766
	}
	if c.QuarantineDays < 1 || c.QuarantineDays > 365 {
		c.QuarantineDays = 7
	}
	if c.AutoScanHours < 0 || c.AutoScanHours > 720 {
		c.AutoScanHours = 24
	}
	if c.QuarantineMaxGB <= 0 || c.QuarantineMaxGB > 2000 {
		c.QuarantineMaxGB = 20
	}
	if c.NotifyMinMB < 0 || c.NotifyMinMB > 1000000 {
		c.NotifyMinMB = 500
	}
	if c.LazyAuto != "daily" && c.LazyAuto != "weekly" {
		c.LazyAuto = "off"
	}
	if c.LazyAutoHour < 0 || c.LazyAutoHour > 23 {
		c.LazyAutoHour = 3
	}
	if c.WarnGB <= 0 {
		c.WarnGB = 20
	}
	if c.DangerGB <= 0 || c.DangerGB >= c.WarnGB {
		c.DangerGB = c.WarnGB / 2
	}
	if c.CriticalGB <= 0 || c.CriticalGB >= c.DangerGB {
		c.CriticalGB = c.DangerGB / 2
	}
	for i := range c.Rules {
		r := &c.Rules[i]
		if r.IntervalMin < 1 {
			r.IntervalMin = 60
		}
		if r.KeepDays < 1 {
			r.KeepDays = 1
		}
		if r.MaxSizeMB < 1 {
			r.MaxSizeMB = 1
		}
	}
}

func dirWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	probe := filepath.Join(dir, ".write-probe")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

// 存储自检：确认数据目录可写
func storeSelftest(cfg Config, st *Store) {
	fmt.Printf("数据目录：%s\n", cfg.DataDir)
	test := filepath.Join(cfg.DataDir, "write-test.txt")
	if err := os.WriteFile(test, []byte("ok"), 0o644); err != nil {
		fmt.Printf("[FAIL] 写入测试失败：%v\n", err)
	} else {
		fmt.Println("[OK] 写入测试成功")
	}
	st.AddAlert("selftest", "存储自检")
	if b, err := os.ReadFile(st.path); err != nil {
		fmt.Printf("[FAIL] state.json 读回失败：%v\n", err)
	} else {
		fmt.Printf("[OK] state.json %d 字节\n", len(b))
	}
}

// 运行时可变的配置（规则开关会热更新，不用重启）
var (
	cfgMu      sync.RWMutex
	liveConfig Config
)

func setLiveConfig(c Config) {
	cfgMu.Lock()
	liveConfig = c
	cfgMu.Unlock()
}

func getConfig() Config {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return liveConfig
}

// 状态
type DirEntry struct {
	Path  string `json:"path"`
	Bytes uint64 `json:"bytes"`
	Cat   string `json:"cat"`
}

type FileEntry struct {
	Path  string `json:"path"`
	Bytes uint64 `json:"bytes"`
	MTime int64  `json:"mtime"`
}

type Scan struct {
	Root            string  `json:"root"`
	Source          string  `json:"source"` // full / incremental / cached
	ReusedChildren  int     `json:"reused_children"`
	ScannedChildren int     `json:"scanned_children"`
	TS              int64   `json:"ts"`
	Total           uint64  `json:"total"`
	Files           int     `json:"files"`
	Dirs            int     `json:"dirs"`
	Errors          int     `json:"errors"`
	Duration        float64 `json:"duration"`
	Used            uint64  `json:"used"`
	Free            uint64  `json:"free"`
	// 硬链接口径。Total 是「按路径求和」（逻辑体积，资源管理器也这么报）；
	// Shared 是其中被重复计入的部分（下限，只覆盖 >=HlProbeMB 的文件）；
	// Unique = Total - Shared，才是敢跟「卷已用」对照的物理占用近似。
	Shared    uint64            `json:"shared"`
	Unique    uint64            `json:"unique"`
	HlProbeMB int               `json:"hl_probe_mb"`
	Cats      map[string]uint64 `json:"cats"`
	// CatsShared 是每个分类里「被硬链接重复计入」的字节（下限，仅覆盖 >=HlProbeMB 的文件）。
	// Cats 仍是按路径求和，与 Total 同口径；用 Cats[x]-CatsShared[x] 才是该分类的物理占用。
	CatsShared map[string]uint64 `json:"cats_shared,omitempty"`
	Dirs3      []DirEntry        `json:"dirs3"`
	Deep       []DirEntry        `json:"deep"`
	TopFiles   []FileEntry       `json:"top_files"`
	Owners     map[string]uint64 `json:"owners"`
	Protected  map[string]uint64 `json:"protected"`
	DayWrites  map[string]uint64 `json:"day_writes"`
}

type Sample struct {
	TS   int64  `json:"ts"`
	Free uint64 `json:"free"`
}

type Alert struct {
	TS   int64  `json:"ts"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type EngineState struct {
	Engine  string `json:"engine"`
	Running bool   `json:"running"`
	TS      int64  `json:"ts"`
}

type State struct {
	Scan    *Scan               `json:"scan"`
	Samples []Sample            `json:"samples"`
	Alerts  []Alert             `json:"alerts"`
	Engine  EngineState         `json:"engine"`
	Rules   map[string]RuleStat `json:"rules"`
}

var (
	dataDirFallback  bool
	dataDirRequested string
	dataDirActual    string
)

type Store struct {
	mu           sync.Mutex
	path         string
	st           State
	lastAlertKey string
	lastAlertTS  int64
	suppressed   int
}

func openStore(cfg Config) *Store {
	s := &Store{path: filepath.Join(cfg.DataDir, "state.json")}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.st)
	}
	return s
}

func (s *Store) saveLocked() {
	b, err := json.Marshal(&s.st)
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err == nil {
		_ = os.Rename(tmp, s.path)
	}
}

func (s *Store) SetScan(sc *Scan) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Scan = sc
	s.saveLocked()
}

func (s *Store) GetScan() *Scan {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Scan
}

func (s *Store) AddSample(free uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Samples = append(s.st.Samples, Sample{TS: time.Now().Unix(), Free: free})
	if len(s.st.Samples) > 5000 {
		s.st.Samples = s.st.Samples[len(s.st.Samples)-5000:]
	}
}

// 同类告警在窗口内只留一条，避免增长告警刷屏
var alertThrottleWindow = 5 * time.Minute

// 把数字抹掉，得到"形态"key：同一目录反复增长会归并
func alertKey(kind, text string) string {
	r := []rune(text)
	for i, c := range r {
		if c >= '0' && c <= '9' {
			r[i] = '#'
		}
	}
	s := string(r)
	if len([]rune(s)) > 80 {
		s = string([]rune(s)[:80])
	}
	return kind + "|" + s
}

// AddAlert 返回是否真的记录（false=被节流，调用方可据此跳过通知）
func (s *Store) AddAlert(kind, text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	key := alertKey(kind, text)
	if key == s.lastAlertKey && now-s.lastAlertTS < int64(alertThrottleWindow.Seconds()) {
		s.suppressed++
		s.lastAlertTS = now
		return false
	}
	if s.suppressed > 0 {
		s.st.Alerts = append(s.st.Alerts, Alert{TS: now, Kind: "digest",
			Text: fmt.Sprintf("同类告警已合并 %d 条（%s）", s.suppressed, kind)})
		s.suppressed = 0
	}
	s.lastAlertKey = key
	s.lastAlertTS = now
	s.st.Alerts = append(s.st.Alerts, Alert{TS: now, Kind: kind, Text: text})
	if len(s.st.Alerts) > 200 {
		s.st.Alerts = s.st.Alerts[len(s.st.Alerts)-200:]
	}
	s.saveLocked()
	return true
}

func (s *Store) Alerts() []Alert {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Alert(nil), s.st.Alerts...)
}

func (s *Store) LastAlert() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.st.Alerts) == 0 {
		return ""
	}
	return s.st.Alerts[len(s.st.Alerts)-1].Text
}

func (s *Store) SetEngine(e string, running bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Engine = EngineState{Engine: e, Running: running, TS: time.Now().Unix()}
	s.saveLocked()
}

func (s *Store) Engine() EngineState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Engine
}

func (s *Store) Save() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveLocked()
}

// FlushDigest 把被节流的告警汇总成一条（由后台循环定期调用）
func (s *Store) FlushDigest() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.suppressed == 0 {
		return
	}
	s.st.Alerts = append(s.st.Alerts, Alert{TS: time.Now().Unix(), Kind: "digest",
		Text: fmt.Sprintf("有 %d 条重复告警被合并（节流窗口 %v）", s.suppressed, alertThrottleWindow)})
	if len(s.st.Alerts) > 200 {
		s.st.Alerts = s.st.Alerts[len(s.st.Alerts)-200:]
	}
	s.suppressed = 0
	s.saveLocked()
}

// rootVolume 从根目录取出盘符（C:\ -> C:），USN 等按卷工作的模块用它
func rootVolume(root string) string {
	if len(root) >= 2 && root[1] == ':' {
		return strings.ToUpper(root[:2])
	}
	if len(root) >= 1 {
		up := strings.ToUpper(root[:1])
		if up >= "A" && up <= "Z" {
			return up + ":"
		}
	}
	return "C:"
}
