package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// uiGeom 是上次窗口的位置和大小。存在 ui-profile 旁边——
// 那里一定是可写的（profile 就在那儿），不依赖 cfg.DataDir 是否降级。
type uiGeom struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// uiGeomFile 是窗口几何的落盘位置，由 openStore 在启动时设成「数据目录」下的文件。
//
// 曾经把它放在 Edge 的 ui-profile 旁边，结果写入报 Access is denied——
// 那个目录归浏览器所有，不是我们的地盘。数据目录才是这个程序一直在写的地方
// （state.json、日志都在那儿），放这里才一定写得进去。
var uiGeomFile string

func uiGeomPath() string {
	if uiGeomFile != "" {
		return uiGeomFile
	}
	// 兜底：万一没被初始化（比如单元测试直接调），退回 LOCALAPPDATA。
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "diskmon", "ui-window.json")
}

// uiGeomCandidates 列出「可能存过窗口几何」的位置，按可信度排序。
//
// 主位置是 uiGeomFile；再带上数据目录里的那一份，是为了兼容「另一个 exe 把几何
// 写在自己的数据目录里」的情形——直接读得到，就不用再失败一次、退回默认尺寸。
func uiGeomCandidates() []string {
	var out []string
	primary := uiGeomPath()
	out = append(out, primary)
	if dataDirActual != "" {
		if p := filepath.Join(dataDirActual, "ui-window.json"); p != primary {
			out = append(out, p)
		}
	}
	return out
}

func loadUIGeom() (uiGeom, bool) {
	for _, p := range uiGeomCandidates() {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var g uiGeom
		if json.Unmarshal(b, &g) != nil {
			continue
		}
		// 太小的值不认——那多半是窗口最小化或异常时记下来的。
		if g.W < 640 || g.H < 480 {
			continue
		}
		return g, true
	}
	return uiGeom{}, false
}

func saveUIGeom(g uiGeom) error {
	if g.W < 640 || g.H < 480 {
		return nil
	}
	// 差值在 4 像素内就当没动过，沿用旧值。
	//
	// 原因：Edge 的 --window-size 和页面读到的 outerWidth 差 1 像素（边框/阴影的
	// 换算），把读到的值原样存回去再传给它，每开一次窗口就长 1 像素。一天开十次
	// 窗口就大一圈，用不了一个月就撑满屏幕。4 像素的容差既能挡住这个漂移，也不会
	// 吞掉用户真的去拉的那一下。
	if prev, ok := loadUIGeom(); ok &&
		absInt(g.W-prev.W) <= 4 && absInt(g.H-prev.H) <= 4 {
		g.W, g.H = prev.W, prev.H
	}
	_ = os.MkdirAll(filepath.Dir(uiGeomPath()), 0o755)
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	tmp := uiGeomPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, uiGeomPath())
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// portListening 探测本机端口上有没有服务在听。目标是 127.0.0.1，250ms 足够。
func portListening(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// ensureServer 保证仪表盘有服务可连：已经在跑就直接用，没在跑就自己拉起一个。
//
// 桌面图标执行的是 `open`，而 `open` 原来只开浏览器、不管服务——服务没运行时
// 点图标只会得到一个「无法访问」的窗口，还不给任何解释。用户管这叫「又进不去了」，
// 那个「又」字说明这不是第一次。所以这里补上自愈：先探端口，没有再起一个。
func ensureServer(port int) bool {
	if portListening(port) {
		return true
	}
	exe, err := os.Executable()
	if err != nil {
		logf("ui: 取不到自身路径，无法拉起后台服务：%v", err)
		return false
	}
	cmd := exec.Command(exe, "tray")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		logf("ui: 后台服务起不来：%v", err)
		return false
	}
	_ = cmd.Process.Release() // 不等它，也不让它随本进程退出而结束
	logf("ui: 检测到服务没在跑，已拉起托盘+服务（端口 %d）", port)

	// 最多等 5 秒。首次启动要读配置、起 HTTP 服务，通常 1 秒内就绪。
	for i := 0; i < 50; i++ {
		if portListening(port) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// openAppWindow 用 Edge/Chrome 的 --app 模式打开，外观是独立窗口（无地址栏、无标签页），
// 使用独立 profile，所以任务栏图标和窗口分组独立于日常浏览器。
func openAppWindow(port int) {
	// app=1 让页面知道自己是「应用窗口」：据此禁用 F5/右键/F12 这类浏览器键。
	// 普通浏览器打开同一个地址不会被这个标记影响。
	url := fmt.Sprintf("http://127.0.0.1:%d/?app=1", port)
	profile := filepath.Join(os.Getenv("LOCALAPPDATA"), "diskmon", "ui-profile")
	_ = os.MkdirAll(profile, 0o755)

	// 上次的窗口位置和大小。Edge 的 --app 窗口不会自己记住，所以由页面回传、这里还原。
	args := []string{
		"--app=" + url,
		"--user-data-dir=" + profile,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-features=Translate,MediaRouter",
	}
	if g, ok := loadUIGeom(); ok {
		args = append(args,
			fmt.Sprintf("--window-size=%d,%d", g.W, g.H),
			fmt.Sprintf("--window-position=%d,%d", g.X, g.Y))
	} else {
		args = append(args, "--window-size=1280,880")
	}

	cands := []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	}
	for _, exe := range cands {
		if _, err := os.Stat(exe); err != nil {
			continue
		}
		cmd := exec.Command(exe, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: false}
		if cmd.Start() == nil {
			logf("ui: 独立窗口已用 %s 打开", filepath.Base(exe))
			return
		}
	}
	logf("ui: 没找到 Edge/Chrome，退回默认浏览器")
	openBrowser(url)
}
