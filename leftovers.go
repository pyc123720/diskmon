package main

import (
	"encoding/json"
	"sync"
	"time"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

// ---------- 卸载残留 ----------

type uninstEntry struct {
	Name        string `json:"DisplayName"`
	Location    string `json:"InstallLocation"`
	Uninstall   string `json:"UninstallString"`
	Publisher   string `json:"Publisher"`
}

type Leftover struct {
	Kind       string // registry-orphan / data-leftover
	Name       string
	Path       string
	Bytes      uint64
	Reason     string
	Confidence string // high / low
}

const uninstScript = `[Console]::OutputEncoding=[Text.Encoding]::UTF8
$paths = @('HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*',
           'HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\*',
           'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*')
$items = foreach ($p in $paths) {
  Get-ItemProperty $p -ErrorAction SilentlyContinue |
    Where-Object { $_.DisplayName } |
    Select-Object DisplayName, InstallLocation, UninstallString, Publisher
}
@($items) | ConvertTo-Json -Compress -Depth 3`

func queryUninstall() []uninstEntry {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", uninstScript)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		logf("leftovers: 查询注册表失败 %v", err)
		return nil
	}
	s := strings.TrimSpace(string(out))
	if s == "" || s == "null" {
		return nil
	}
	var arr []uninstEntry
	if json.Unmarshal([]byte(s), &arr) != nil {
		var one uninstEntry
		if json.Unmarshal([]byte(s), &one) == nil {
			arr = []uninstEntry{one}
		}
	}
	return arr
}

var pctVarRe = regexp.MustCompile(`%([A-Za-z0-9_() .]+)%`)

// expandPath 同时支持 %TEMP% 和 $env:TEMP 两种写法
func expandPath(p string) string {
	p = strings.TrimSpace(strings.Trim(p, `"`))
	p = pctVarRe.ReplaceAllStringFunc(p, func(m string) string {
		name := strings.Trim(m, "%")
		if v := os.Getenv(name); v != "" {
			return v
		}
		return m
	})
	p = os.ExpandEnv(p)
	return strings.TrimRight(p, `\`)
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func normName(s string) string {
	s = strings.ToLower(s)
	repl := strings.NewReplacer(" ", "", "-", "", "_", "", ".", "", "(", "", ")", "")
	return repl.Replace(s)
}

var appxCache []string
var appxOnce bool

// 商店应用（MSIX）不写传统卸载项，单独查一次
func appxInstalled(name string) bool {
	if !appxOnce {
		appxOnce = true
		cmd := exec.Command("powershell.exe", "-NoProfile", "-Command",
			"Get-AppxPackage | Select-Object -ExpandProperty Name")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if out, err := cmd.Output(); err == nil {
			appxCache = strings.Split(string(out), "\r\n")
		}
	}
	n := normName(name)
	if len(n) < 3 {
		return false
	}
	for _, a := range appxCache {
		if strings.Contains(normName(a), n) {
			return true
		}
	}
	return false
}

func looksInstalled(name string, entries []uninstEntry) bool {
	n := normName(name)
	if n == "" {
		return true
	}
	if appxInstalled(name) {
		return true
	}
	for _, e := range entries {
		if strings.Contains(normName(e.Name), n) || strings.Contains(n, normName(e.Name)) {
			return true
		}
		if e.Location != "" && strings.Contains(normName(e.Location), n) {
			return true
		}
	}
	// 常见安装目录是否存在同名文件夹
	for _, base := range []string{
		`C:\Program Files`, `C:\Program Files (x86)`,
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs"),
		filepath.Join(os.Getenv("APPDATA"), "Programs"),
	} {
		if dirExists(filepath.Join(base, name)) {
			return true
		}
	}
	return false
}

// 这些名字的"数据目录"其实是工具本身或系统组件，不算卸载残留
var leftoverNameBlock = map[string]bool{
	"npm": true, "pnpm": true, "npm-cache": true, "pnpm-cache": true, "yarn": true,
	"node-gyp": true, "crashdumps": true, "temp": true, "tmp": true,
	"microsoft": true, "packages": true, "nvidia": true, "d3dscache": true,
	"comms": true, "connecteddevicesplatform": true, "intel": true, "amd": true,
	"google": true, "chromium": true, "edge": true, "webview2": true,
	"programs": true, "low": true, "roaming": true, "local": true,
	"windowsapps": true, "packagedata": true, "programdata": true,
	"tempprofile": true, "pip": true, "cache": true, "wsl": true, "docker": true,
}

func nameBlocked(name string) bool { return leftoverNameBlock[strings.ToLower(strings.TrimSpace(name))] }

// 目录里若含有 exe（两层内，>1MB），说明可能是便携安装，不宜当残留
func dirHasExe(dir string) bool {
	baseDepth := strings.Count(filepath.Clean(dir), string(os.PathSeparator))
	found := false
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || found {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if strings.Count(filepath.Clean(p), string(os.PathSeparator))-baseDepth >= 2 {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".exe") {
			if info, e := d.Info(); e == nil && info.Size() > 1<<20 {
				found = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}

func runningProcessMatches(name string) bool {
	n := normName(name)
	if len(n) < 3 {
		return false
	}
	for _, exe := range enumProcesses() {
		e := normName(strings.TrimSuffix(strings.ToLower(exe), ".exe"))
		if e != "" && (strings.Contains(e, n) || strings.Contains(n, e)) {
			return true
		}
	}
	return false
}

// 残留检测要起 PowerShell 查注册表/商店包，单次约 1.5 秒。
// 页面渲染不能等它，所以放后台算、结果缓存 10 分钟。
var (
	loMu    sync.Mutex
	loCache []Leftover
	loAt    time.Time
	loBusy  bool
)

const loTTL = 10 * time.Minute

func leftoversCached(cfg Config, st *Store) []Leftover {
	loMu.Lock()
	fresh := loCache != nil && time.Since(loAt) < loTTL
	if fresh {
		c := loCache
		loMu.Unlock()
		return c
	}
	kick := !loBusy
	if kick {
		loBusy = true
	}
	c := loCache
	loMu.Unlock()

	if kick {
		go func() {
			items := scanLeftovers(cfg, st)
			loMu.Lock()
			loCache = items
			loAt = time.Now()
			loBusy = false
			loMu.Unlock()
			logf("leftovers: 后台计算完成，%d 项", len(items))
		}()
	}
	return c
}

func leftoversInvalidate() {
	loMu.Lock()
	loAt = time.Time{}
	loMu.Unlock()
}

func leftoversState() (time.Time, bool) {
	loMu.Lock()
	defer loMu.Unlock()
	return loAt, loBusy
}

func scanLeftovers(cfg Config, st *Store) []Leftover {
	entries := queryUninstall()
	var out []Leftover

	// 1) 注册表里登记了安装路径，但目录已不存在
	for _, e := range entries {
		loc := expandPath(e.Location)
		if loc == "" || len(loc) < 8 {
			continue
		}
		if dirExists(loc) {
			continue
		}
		out = append(out, Leftover{
			Kind: "registry-orphan", Name: e.Name, Path: loc, Confidence: "high",
			Reason: "注册表还登记着这个软件，但安装目录已经不存在",
		})
	}

	// 2) 应用数据还在，但程序和卸载登记都没了 → 疑似残留数据
	sc := st.GetScan()
	if sc != nil {
		seen := map[string]bool{}
		for _, d := range sc.Deep {
			meta, ok := cats[d.Cat]
			if !ok || (meta.Tier != "clean" && meta.Tier != "confirm") || d.Bytes < 50<<20 {
				continue
			}
			if deniedForCleanup(d.Path) {
				continue
			}
			name := ownerOf(d.Path)
			if name == "" {
				continue
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			if nameBlocked(name) || looksInstalled(name, entries) {
				continue
			}
			if dirHasExe(d.Path) {
				continue
			}
			if runningProcessMatches(name) {
				continue
			}
			lowPath := strings.ToLower(d.Path)
			if strings.Contains(lowPath, "\\programdata\\package cache") || strings.Contains(lowPath, "\\programdata\\microsoft\\windows") {
				continue
			}
			out = append(out, Leftover{
				Kind: "data-leftover", Name: name, Path: d.Path, Bytes: d.Bytes, Confidence: "low",
				Reason: "没有找到对应的已安装程序或卸载登记（可能是残留，也可能是便携版/未登记应用）",
			})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	if len(out) > 100 {
		out = out[:100]
	}
	return out
}

func printLeftovers(cfg Config, st *Store) {
	items := scanLeftovers(cfg, st)
	if len(items) == 0 {
		fmt.Println("没有发现明显的卸载残留")
		return
	}
	var dataBytes uint64
	for _, it := range items {
		if it.Kind == "data-leftover" {
			dataBytes += it.Bytes
			fmt.Printf("[数据残留] %-20s %10s  %s\n", it.Name, human(it.Bytes), it.Path)
		} else {
			fmt.Printf("[注册表孤立] %-20s %s\n", it.Name, it.Path)
		}
	}
	fmt.Printf("合计：数据残留 %s（可隔离），注册表孤立 %d 条（建议用卸载工具清理）\n",
		human(dataBytes), countKind(items, "registry-orphan"))
}

func countKind(items []Leftover, kind string) int {
	n := 0
	for _, it := range items {
		if it.Kind == kind {
			n++
		}
	}
	return n
}
