package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

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
	cmd := exec.Command(exe, "serve")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		logf("ui: 后台服务起不来：%v", err)
		return false
	}
	_ = cmd.Process.Release() // 不等它，也不让它随本进程退出而结束
	logf("ui: 检测到服务没在跑，已拉起后台服务（端口 %d）", port)

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
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	profile := filepath.Join(os.Getenv("LOCALAPPDATA"), "diskmon", "ui-profile")
	_ = os.MkdirAll(profile, 0o755)
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
		cmd := exec.Command(exe,
			"--app="+url,
			"--window-size=1280,880",
			"--user-data-dir="+profile,
			"--no-first-run",
			"--no-default-browser-check",
			"--disable-features=Translate,MediaRouter",
		)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: false}
		if cmd.Start() == nil {
			logf("ui: 独立窗口已用 %s 打开", filepath.Base(exe))
			return
		}
	}
	logf("ui: 没找到 Edge/Chrome，退回默认浏览器")
	openBrowser(url)
}
