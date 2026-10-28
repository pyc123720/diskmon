package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

//go:embed diskmon.ico
var iconBytes []byte

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")

	pGetDiskFreeSpaceExW  = kernel32.NewProc("GetDiskFreeSpaceExW")
	pCreateMutexW         = kernel32.NewProc("CreateMutexW")
	pGetModuleHandleW     = kernel32.NewProc("GetModuleHandleW")
	pRegisterClassExW     = user32.NewProc("RegisterClassExW")
	pCreateWindowExW      = user32.NewProc("CreateWindowExW")
	pDefWindowProcW       = user32.NewProc("DefWindowProcW")
	pGetMessageW          = user32.NewProc("GetMessageW")
	pTranslateMessage     = user32.NewProc("TranslateMessage")
	pDispatchMessageW     = user32.NewProc("DispatchMessageW")
	pPostQuitMessage      = user32.NewProc("PostQuitMessage")
	pCreatePopupMenu      = user32.NewProc("CreatePopupMenu")
	pAppendMenuW          = user32.NewProc("AppendMenuW")
	pTrackPopupMenu       = user32.NewProc("TrackPopupMenu")
	pDestroyMenu          = user32.NewProc("DestroyMenu")
	pSetForegroundWindow  = user32.NewProc("SetForegroundWindow")
	pGetCursorPos         = user32.NewProc("GetCursorPos")
	pLoadImageW           = user32.NewProc("LoadImageW")
	pDestroyIcon          = user32.NewProc("DestroyIcon")
	pPostMessageW         = user32.NewProc("PostMessageW")
	pDestroyWindow        = user32.NewProc("DestroyWindow")
	pDuplicateHandle      = kernel32.NewProc("DuplicateHandle")
	pGetLogicalDrives     = kernel32.NewProc("GetLogicalDrives")
	pShellNotifyIconW     = shell32.NewProc("Shell_NotifyIconW")
	procMessageBox        = user32.NewProc("MessageBoxW")
)

const (
	WM_DESTROY     = 0x0002
	WM_CLOSE       = 0x0010
	WM_COMMAND     = 0x0111
	WM_NULL        = 0x0000
	WM_APP         = 0x8000
	WM_LBUTTONUP   = 0x0202
	WM_RBUTTONUP   = 0x0205
	WM_LBUTTONDBLCLK = 0x0203

	NIM_ADD    = 0
	NIM_MODIFY = 1
	NIM_DELETE = 2

	NIF_MESSAGE = 0x01
	NIF_ICON    = 0x02
	NIF_TIP     = 0x04
	NIF_INFO    = 0x10

	NIIF_INFO    = 0x01
	NIIF_WARNING = 0x02

	MF_STRING    = 0x0000
	MF_SEPARATOR = 0x0800
	MF_CHECKED   = 0x0008
	MF_UNCHECKED = 0x0000

	TPM_RIGHTBUTTON = 0x0002
	TPM_RETURNCMD   = 0x0100

	IMAGE_ICON     = 1
	LR_LOADFROMFILE = 0x0010

	HWND_MESSAGE = ^uintptr(2) // (HWND)-3
)

type Volume struct {
	Total uint64
	Free  uint64
	Used  uint64
}

func volumeStats(root string) (Volume, error) {
	p, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return Volume{}, err
	}
	var free, total, totalFree uint64
	r, _, e := pGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&free)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)))
	if r == 0 {
		return Volume{}, e
	}
	return Volume{Total: total, Free: free, Used: total - free}, nil
}

func human(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(b)/(1<<20))
	default:
		return fmt.Sprintf("%.0f KB", float64(b)/1024)
	}
}

func gb(b uint64) float64 { return float64(b) / (1 << 30) }

func levelOf(v Volume, c Config) string {
	fg := gb(v.Free)
	fp := 0.0
	if v.Total > 0 {
		fp = float64(v.Free) / float64(v.Total) * 100
	}
	switch {
	case fg < c.CriticalGB || fp < c.CriticalPct:
		return "critical"
	case fg < c.DangerGB || fp < c.DangerPct:
		return "danger"
	case fg < c.WarnGB || fp < c.WarnPct:
		return "warn"
	}
	return "ok"
}

func levelText(l string) string {
	switch l {
	case "warn":
		return "注意"
	case "danger":
		return "警告"
	case "critical":
		return "紧急"
	}
	return "正常"
}

// ---------- Win32 结构 ----------

type wndClassExW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     syscall.Handle
	HIcon         syscall.Handle
	HCursor       syscall.Handle
	HbrBackground syscall.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       syscall.Handle
}

type point struct{ X, Y int32 }

type msgT struct {
	HWnd    syscall.Handle
	Message uint32
	_       uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	_       uint32
}

type notifyIconData struct {
	CbSize           uint32
	_                uint32
	HWnd             syscall.Handle
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	_                uint32
	HIcon            syscall.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     syscall.Handle
}

func copyUTF16(dst []uint16, s string) {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		return
	}
	n := len(dst) - 1
	if len(u) < n {
		n = len(u)
	}
	copy(dst[:n], u[:n])
	dst[n] = 0
}

// ---------- 通知 ----------

const notifyScript = `param([string]$Title,[string]$Body)
try {
  [Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType=WindowsRuntime] | Out-Null
  [Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType=WindowsRuntime] | Out-Null
  $doc = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
  $n = $doc.GetElementsByTagName('text')
  $n.Item(0).AppendChild($doc.CreateTextNode($Title)) | Out-Null
  $n.Item(1).AppendChild($doc.CreateTextNode($Body)) | Out-Null
  $toast = New-Object Windows.UI.Notifications.ToastNotification $doc
  [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('diskmon.DiskMonitor').Show($toast)
} catch { }
`

func notifyScriptPath() string {
	return filepath.Join(exeDir(), "notify.ps1")
}

func ensureNotifyScript() {
	writeFileIfChanged(notifyScriptPath(), []byte(notifyScript))
}

// 通知：优先一次性 PowerShell toast（已注册 diskmon AUMID），失败退回托盘气泡
func notify(title, body string, warning bool) {
	if useToast.Load() {
		ensureNotifyScript()
		cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass",
			"-File", notifyScriptPath(), title, body)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cmd.Start(); err == nil {
			go func() { _ = cmd.Wait() }()
			logf("toast: %s | %s", title, body)
			return
		}
	}
	balloon(title, body, warning)
}

func balloon(title, body string, warning bool) {
	if trayHwnd == 0 {
		return
	}
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = trayHwnd
	nid.UID = 1
	nid.UFlags = NIF_INFO
	copyUTF16(nid.SzInfoTitle[:], title)
	copyUTF16(nid.SzInfo[:], body)
	if warning {
		nid.DwInfoFlags = NIIF_WARNING
	} else {
		nid.DwInfoFlags = NIIF_INFO
	}
	pShellNotifyIconW.Call(NIM_MODIFY, uintptr(unsafe.Pointer(&nid)))
	logf("balloon: %s | %s", title, body)
}

// ---------- 托盘 ----------

const (
	idOpen      = 1001
	idScan      = 1002
	idReport    = 1003
	idTest      = 1004
	idPause     = 1005
	idMute1h    = 1009
	idMuteAll   = 1010
	idUnmute    = 1011
	idAutostart = 1006
	idQuit      = 1007
	idHealth    = 1008
	trayMsg     = WM_APP + 1
)

var (
	trayHwnd  syscall.Handle
	trayIcon  syscall.Handle
	menuHndl  syscall.Handle
	paused    atomicBool
	useToast  atomicBool
	mainWnd   syscall.Handle
)

type atomicBool struct {
	v uint32
}

func (b *atomicBool) Store(x bool) {
	if x {
		b.v = 1
	} else {
		b.v = 0
	}
}
func (b *atomicBool) Load() bool { return b.v != 0 }

var wndProcCallback uintptr

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func writeFileIfChanged(path string, data []byte) {
	if old, err := os.ReadFile(path); err == nil && string(old) == string(data) {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

func loadIcon() syscall.Handle {
	ico := filepath.Join(os.Getenv("TEMP"), "diskmon-go.ico")
	_ = os.WriteFile(ico, iconBytes, 0o644)
	p, _ := syscall.UTF16PtrFromString(ico)
	h, _, _ := pLoadImageW.Call(0, uintptr(unsafe.Pointer(p)), IMAGE_ICON, 0, 0, LR_LOADFROMFILE)
	return syscall.Handle(h)
}

func buildMenu() syscall.Handle {
	m, _, _ := pCreatePopupMenu.Call()
	menuHndl = syscall.Handle(m)
	add := func(id uintptr, text string, flags uintptr) {
		u, _ := syscall.UTF16PtrFromString(text)
		pAppendMenuW.Call(uintptr(menuHndl), flags, id, uintptr(unsafe.Pointer(u)))
	}
	add(idOpen, "打开仪表盘", MF_STRING)
	add(idScan, "重新扫描（约 20 秒）", MF_STRING)
	add(idReport, "生成离线报告并打开", MF_STRING)
	add(idTest, "测试通知", MF_STRING)
	add(idHealth, "自检（健康状态）", MF_STRING)
	pauseFlags := uintptr(MF_STRING | MF_UNCHECKED)
	if paused.Load() {
		pauseFlags = MF_STRING | MF_CHECKED
	}
	add(idPause, "暂停实时监测", pauseFlags)
	if muted() {
		add(idUnmute, "取消静音", MF_STRING)
	} else {
		add(idMute1h, "静音 1 小时", MF_STRING)
		add(idMuteAll, "静音直到重启", MF_STRING)
	}
	add(0, "", MF_SEPARATOR)
	autoFlags := uintptr(MF_STRING | MF_UNCHECKED)
	if autostartEnabled() {
		autoFlags = MF_STRING | MF_CHECKED
	}
	add(idAutostart, "开机自启动", autoFlags)
	add(0, "", MF_SEPARATOR)
	add(idQuit, "退出", MF_STRING)
	return menuHndl
}

func showMenu() {
	if menuHndl != 0 {
		pDestroyMenu.Call(uintptr(menuHndl))
	}
	buildMenu()
	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(uintptr(trayHwnd))
	cmd, _, _ := pTrackPopupMenu.Call(uintptr(menuHndl),
		uintptr(TPM_RIGHTBUTTON|TPM_RETURNCMD),
		uintptr(pt.X), uintptr(pt.Y), 0, uintptr(trayHwnd), 0)
	pPostMessageW.Call(uintptr(trayHwnd), WM_NULL, 0, 0)
	if cmd != 0 {
		handleCommand(int(cmd))
	}
}

func handleCommand(id int) {
	switch id {
	case idOpen:
		openAppWindow(curCfg.Port)
	case idScan:
		go func() { runScan(curCfg, curStore) }()
		notify("diskmon", "已开始后台扫描，完成后刷新看板。", false)
	case idReport:
		go func() {
			p := writeReport(curCfg, curStore)
			openPath(p)
		}()
	case idTest:
		notify("diskmon 测试通知", "看到这条说明通知正常，增长告警就长这样。", false)
	case idHealth:
		showHealth()
	case idMute1h:
		setMute(time.Hour)
		notify("diskmon", "通知已静音 1 小时。", false)
	case idMuteAll:
		muteForever()
		notify("diskmon", "通知已静音，直到重启。", false)
	case idUnmute:
		clearMute()
		notify("diskmon", "通知已恢复。", false)
	case idPause:
		paused.Store(!paused.Load())
		if paused.Load() {
			curStore.SetEngine("paused", false)
			notify("diskmon", "实时监测已暂停。", false)
		} else {
			notify("diskmon", "实时监测已恢复。", false)
		}
	case idAutostart:
		toggleAutostart()
	case idQuit:
		quitApp()
	}
}

func quitApp() {
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = trayHwnd
	nid.UID = 1
	pShellNotifyIconW.Call(NIM_DELETE, uintptr(unsafe.Pointer(&nid)))
	if trayIcon != 0 {
		pDestroyIcon.Call(uintptr(trayIcon))
	}
	if mainWnd != 0 {
		pDestroyWindow.Call(uintptr(mainWnd))
	}
}

func wndProc(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case trayMsg:
		switch uint32(lParam) & 0xFFFF {
		case WM_LBUTTONUP, WM_LBUTTONDBLCLK:
			openAppWindow(curCfg.Port)
		case WM_RBUTTONUP:
			showMenu()
		}
		return 0
	case WM_COMMAND:
		handleCommand(int(wParam & 0xFFFF))
		return 0
	case WM_DESTROY:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return r
}

func updateTooltip(title string) {
	if trayHwnd == 0 {
		return
	}
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = trayHwnd
	nid.UID = 1
	nid.UFlags = NIF_TIP
	copyUTF16(nid.SzTip[:], title)
	pShellNotifyIconW.Call(NIM_MODIFY, uintptr(unsafe.Pointer(&nid)))
}

// runTray 启动托盘并进入消息循环（阻塞）
func runTray(cfg Config, st *Store) {
	curCfg, curStore = cfg, st
	useToast.Store(cfg.UseToast)

	// 单实例。CreateMutexW 在锁已存在时仍会返回句柄，只有 GetLastError 是
	// ERROR_ALREADY_EXISTS(183) 才说明已有实例 —— 这时直接把看板打开，而不是
	// 静默退出：用户双击桌面图标时，期望看到的就是看板。
	name, _ := syscall.UTF16PtrFromString("Local\\diskmon-go-single")
	h, _, e := pCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		fmt.Println("无法创建单实例锁")
		return
	}
	if errno, ok := e.(syscall.Errno); ok && errno == 183 {
		openAppWindow(cfg.Port)
		return
	}
	_ = h

	hInst, _, _ := pGetModuleHandleW.Call(0)
	clsName, _ := syscall.UTF16PtrFromString("diskmonGoWindow")
	wndProcCallback = syscall.NewCallback(wndProc)
	wc := wndClassExW{
		CbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		LpfnWndProc:   wndProcCallback,
		HInstance:     syscall.Handle(hInst),
		LpszClassName: clsName,
	}
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	title, _ := syscall.UTF16PtrFromString("diskmon")
	hwnd, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(clsName)),
		uintptr(unsafe.Pointer(title)), 0, 0, 0, 0, 0,
		HWND_MESSAGE, 0, hInst, 0)
	trayHwnd = syscall.Handle(hwnd)
	mainWnd = trayHwnd

	// 注册通知身份（HKCU，无需管理员）
	registerAUMID()

	trayIcon = loadIcon()
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = trayHwnd
	nid.UID = 1
	nid.UFlags = NIF_MESSAGE | NIF_ICON | NIF_TIP
	nid.UCallbackMessage = trayMsg
	nid.HIcon = trayIcon
	copyUTF16(nid.SzTip[:], "diskmon 磁盘监测")
	pShellNotifyIconW.Call(NIM_ADD, uintptr(unsafe.Pointer(&nid)))

	// 后台任务
	go guard("http", func() { serveHTTP(cfg, st) })
	go guard("watch", func() { watchLoop(cfg, st) })
	go guard("rules", func() { rulesLoop(cfg, st) })
	go guard("autoscan", func() { autoScanLoop(cfg, st) })
	go guard("proc", func() { procLoop(cfg) })
	go guard("lazyauto", func() { lazyAutoLoop(cfg, st) })
	go func() {
		for {
			time.Sleep(30 * time.Second)
			v, err := volumeStats(cfg.Root)
			if err != nil {
				continue
			}
			eng := st.Engine()
			suffix := ""
			if eng.Running {
				if eng.Engine == "usn" {
					suffix = " · USN秒级"
				} else {
					suffix = " · 重扫"
				}
			}
			updateTooltip(fmt.Sprintf("C: 剩 %.1f GB (%.1f%%) [%s]%s",
				gb(v.Free), float64(v.Free)/float64(v.Total)*100, levelText(levelOf(v, cfg)), suffix))
		}
	}()

	logf("tray ready")
	notify("diskmon 已启动", "左键开看板，右键菜单。只读监测，不会删除任何文件。", false)

	var m msgT
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// ---------- 外部工具 ----------

func openBrowser(url string) {
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Start()
	logf("open: %s", url)
}

func openPath(p string) {
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", p)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Start()
}

func startHidden(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}

func registerAUMID() {
	_ = exec.Command("reg", "add",
		`HKCU\Software\Classes\AppUserModelId\diskmon.DiskMonitor`,
		"/v", "DisplayName", "/t", "REG_SZ", "/d", "diskmon", "/f").Run()
	icon := filepath.Join(exeDir(), "icon.ico")
	_ = os.WriteFile(icon, iconBytes, 0o644)
	_ = exec.Command("reg", "add",
		`HKCU\Software\Classes\AppUserModelId\diskmon.DiskMonitor`,
		"/v", "IconUri", "/t", "REG_SZ", "/d", icon, "/f").Run()
}

func autostartEnabled() bool {
	cmd := exec.Command("reg", "query",
		`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", "diskmon-go")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run() == nil
}

func toggleAutostart() {
	exe, _ := os.Executable()
	if autostartEnabled() {
		_ = exec.Command("reg", "delete",
			`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", "diskmon-go", "/f").Run()
		notify("diskmon", "已移出开机启动。", false)
	} else {
		_ = exec.Command("reg", "add",
			`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
			"/v", "diskmon-go", "/t", "REG_SZ", "/d", exe, "/f").Run()
		notify("diskmon", "已加入开机启动。", false)
	}
}

func logf(format string, args ...interface{}) {
	line := time.Now().Format("15:04:05 ") + fmt.Sprintf(format, args...)
	f, err := os.OpenFile(filepath.Join(curCfg.DataDir, "diskmon-go.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line + "\n")
}

var (
	curCfg   Config
	curStore *Store
)

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}
