package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

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
