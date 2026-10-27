package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// ---------- 分类体系 ----------

type CatMeta struct {
	Label string
	Tier  string // clean / confirm / adjust / keep
	Hint  string
}

var cats = map[string]CatMeta{
	"system_core":      {"系统核心", "keep", "Windows 自身运行所需，删除会破坏系统"},
	"system_winsxs":    {"WinSxS 组件库", "adjust", "不能手删；只能用 DISM /StartComponentCleanup 压缩，通常回收 1-4 GB"},
	"system_installer": {"Windows Installer 缓存", "adjust", "手删会导致软件无法修复/卸载；用专用工具清理"},
	"system_update":    {"Windows 更新缓存", "clean", "可清，Windows 需要时会重下"},
	"system_restore":   {"系统还原点/卷影", "adjust", "vssadmin 调整配额或删旧还原点；删了就没有回滚点"},
	"system_hiber":     {"休眠文件", "adjust", "powercfg /h off 可释放 4-16 GB，代价是失去快速启动与休眠"},
	"system_pagefile":  {"页面文件", "adjust", "可移到其他盘或缩小；完全关闭可能影响稳定性"},
	"system_apps":      {"应用商店程序", "keep", "走 设置-应用 卸载，不要手删目录"},
	"program_install":  {"程序安装", "keep", "走卸载程序；或迁移到其他盘（junction）"},
	"program_data":     {"程序数据/缓存", "confirm", "多为可再生的缓存，但可能含账号数据，需逐项确认"},
	"dev_cache":        {"开发工具缓存", "clean", "npm/pnpm/pip/gradle/nuget 缓存，清了只会让下次安装变慢"},
	"browser":          {"浏览器数据", "confirm", "只清 Cache/GPUCache/Code Cache；不要碰 Cookies 与 Local Storage"},
	"comms_media":      {"通讯软件媒体", "confirm", "微信/QQ 的图片视频：可能含你要留的文件，只能手动挑 + 隔离区"},
	"games":            {"游戏", "confirm", "用启动器迁移库目录；着色器缓存可删会重编译"},
	"ai_models":        {"AI 模型/向量库", "confirm", "删了要重下；建议迁移到其他盘"},
	"user_docs":        {"用户文档", "keep", "这是你的数据，工具不提供任何自动处理"},
	"user_profile":     {"用户目录其他", "confirm", "杂项，需逐项看"},
	"temp":             {"临时文件", "clean", "Temp 类目录，可清；正被占用的会自动跳过"},
	"logs_dumps":       {"日志与转储", "clean", "日志/崩溃转储，可清"},
	"other":            {"未分类", "confirm", "未命中规则，需人工判断"},
}

type rule struct {
	kind string // prefix / contains
	val  string
	cat  string
}

var rules = []rule{
	{"contains", `\hiberfil.sys`, "system_hiber"},
	{"contains", `\swapfile.sys`, "system_hiber"},
	{"contains", `\pagefile.sys`, "system_pagefile"},
	{"prefix", `c:\windows\winsxs`, "system_winsxs"},
	{"prefix", `c:\windows\installer`, "system_installer"},
	{"prefix", `c:\windows\softwaredistribution`, "system_update"},
	{"prefix", `c:\windows\temp`, "temp"},
	{"prefix", `c:\windows\logs`, "logs_dumps"},
	{"prefix", `c:\windows\minidump`, "logs_dumps"},
	{"prefix", `c:\windows\memory.dmp`, "logs_dumps"},
	{"prefix", `c:\windows\livekernelreports`, "logs_dumps"},
	{"prefix", `c:\windows\prefetch`, "logs_dumps"},
	{"prefix", `c:\programdata\microsoft\windows\wer`, "logs_dumps"},
	{"prefix", `c:\system volume information`, "system_restore"},
	{"prefix", `c:\$recycle.bin`, "temp"},
	{"prefix", `c:\windows\`, "system_core"},
	{"prefix", `c:\program files\windowsapps`, "system_apps"},
	{"prefix", `c:\program files`, "program_install"},
	{"prefix", `c:\program files (x86)`, "program_install"},
	{"contains", `\npm-cache`, "dev_cache"},
	{"contains", `\pnpm\`, "dev_cache"},
	{"contains", `\pip\cache`, "dev_cache"},
	{"contains", `\.gradle`, "dev_cache"},
	{"contains", `\.cargo`, "dev_cache"},
	{"contains", `\.nuget`, "dev_cache"},
	{"contains", `\.m2\repository`, "dev_cache"},
	{"contains", `\node_modules`, "dev_cache"},
	{"contains", `\google\chrome\`, "browser"},
	{"contains", `\microsoft\edge\`, "browser"},
	{"contains", `\quark\`, "browser"},
	{"contains", `\firefox\`, "browser"},
	{"contains", `\wechat files`, "comms_media"},
	{"contains", `\tencent files`, "comms_media"},
	{"contains", `\dingtalk`, "comms_media"},
	{"contains", `\huggingface`, "ai_models"},
	{"contains", `\ollama`, "ai_models"},
	{"contains", `\lm-studio`, "ai_models"},
	{"contains", `\steamapps`, "games"},
	{"contains", `\epicgames`, "games"},
	{"contains", `\appdata\local\programs`, "program_install"},
	{"contains", `\appdata\locallow\`, "program_data"},
	{"contains", `\appdata\local\temp`, "temp"},
	{"contains", `\appdata\local\`, "program_data"},
	{"contains", `\appdata\roaming\`, "program_data"},
	{"prefix", `c:\programdata`, "program_data"},
	{"contains", `\documents\`, "user_docs"},
	{"contains", `\desktop\`, "user_docs"},
	{"contains", `\downloads\`, "user_docs"},
	{"contains", `\pictures\`, "user_docs"},
	{"contains", `\videos\`, "user_docs"},
	{"contains", `\music\`, "user_docs"},
	{"prefix", `c:\users\`, "user_profile"},
}

func classify(low string) string {
	for _, r := range rules {
		if r.kind == "prefix" {
			if strings.HasPrefix(low, r.val) {
				return r.cat
			}
		} else if strings.Contains(low, r.val) {
			return r.cat
		}
	}
	return "other"
}

var genericOwners = map[string]bool{
	"lenovo": true, "programs": true, "program files": true, "x86": true, "(x86)": true,
	"temp": true, "local": true, "roaming": true, "low": true, "locallow": true,
	"microsoft": true, "packages": true, "windows": true, "programdata": true,
	"appdata": true, "users": true, "common files": true, "public": true, "default": true,
}

func pickOwner(seg string) string {
	seg = strings.TrimSpace(seg)
	if seg == "" || genericOwners[strings.ToLower(seg)] {
		return ""
	}
	return seg
}

func ownerOf(path string) string {
	low := strings.ToLower(path)
	for _, marker := range []string{`appdata\locallow`, `appdata\local`, `appdata\roaming`} {
		if i := strings.Index(low, marker); i >= 0 {
			rest := strings.Split(strings.Trim(path[i+len(marker):], ` \`), `\`)
			for _, seg := range rest {
				if n := pickOwner(seg); n != "" {
					return n
				}
			}
			return ""
		}
	}
	if strings.HasPrefix(low, `c:\program files`) {
		rest := strings.Split(strings.Trim(path[len(`c:\program files`):], ` \`), `\`)
		if len(rest) > 0 && strings.EqualFold(rest[0], "(x86)") {
			rest = rest[1:]
		}
		if len(rest) > 0 {
			return pickOwner(rest[0])
		}
		return ""
	}
	if strings.HasPrefix(low, `c:\programdata`) {
		rest := strings.Split(strings.Trim(path[len(`c:\programdata`):], ` \`), `\`)
		if len(rest) > 0 {
			return pickOwner(rest[0])
		}
	}
	return ""
}

// ---------- 扫描 ----------

const fileAttributeReparsePoint = 0x400

func isReparse(info fs.FileInfo) bool {
	if info == nil {
		return false
	}
	if d, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return d.FileAttributes&fileAttributeReparsePoint != 0
	}
	return false
}

func scanVolume(cfg Config, root string) *Scan {
	t0 := time.Now()
	sc := &Scan{
		TS: t0.Unix(), Cats: map[string]uint64{}, Owners: map[string]uint64{},
		Protected: map[string]uint64{}, DayWrites: map[string]uint64{},
	}
	nodes := map[string]uint64{}
	type bigFile struct {
		path  string
		bytes uint64
		mtime int64
	}
	var bigs []bigFile
	wl := make([]string, len(cfg.Whitelist))
	for i, w := range cfg.Whitelist {
		wl[i] = strings.ToLower(w)
	}
	rootLen := len(root)
	seen := 0
	// 目录分类缓存：目录只有 24 万个，文件有 68 万个，按目录缓存能省掉绝大多数规则匹配
	dirCat := map[string]string{}
	dirProt := map[string]string{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			sc.Errors++
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			sc.Errors++
			return nil
		}
		if d.IsDir() {
			if isReparse(info) {
				return fs.SkipDir
			}
			if strings.EqualFold(path, cfg.DataDir) {
				return fs.SkipDir // 不把自己算进去
			}
			sc.Dirs++
			scanAdd(0, 1, 0)
			dirCat[path] = classify(strings.ToLower(path) + `\`)
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		size := uint64(info.Size())
		if size == 0 {
			return nil
		}
		seen++
		scanAdd(1, 0, size)
		if seen%2000 == 0 && scanCancelled() {
			return fs.SkipAll
		}
		if seen%200000 == 0 {
			fmt.Printf("  … 已遍历 %d 万项，累计 %.1f GB\n", seen/10000, float64(sc.Total)/1e9)
		}
		sc.Total += size
		sc.Files++

		// 分类：目录缓存 + 少量文件名规则
		dir := parentDir(path)
		cat, ok := dirCat[dir]
		if !ok {
			cat = classify(strings.ToLower(dir) + `\`)
			dirCat[dir] = cat
		}
		base := path[len(dir)+1:]
		switch strings.ToLower(base) {
		case "hiberfil.sys", "swapfile.sys":
			cat = "system_hiber"
		case "pagefile.sys":
			cat = "system_pagefile"
		case "memory.dmp":
			cat = "logs_dumps"
		}
		sc.Cats[cat] += size

		rel := strings.TrimPrefix(path, root)
		rel = strings.TrimPrefix(rel, `\`)
		parts := strings.Split(rel, `\`)
		for depth := 1; depth <= len(parts) && depth <= 5; depth++ {
			key := root + strings.Join(parts[:depth], `\`)
			nodes[key] += size
		}
		day := time.Unix(info.ModTime().Unix(), 0).Format("2006-01-02")
		sc.DayWrites[day] += size

		if size >= 32<<20 {
			bigs = append(bigs, bigFile{path, size, info.ModTime().Unix()})
		}
		if size >= 4<<20 {
			if o := ownerOf(path); o != "" {
				sc.Owners[o] += size
			}
		}
		prot, pok := dirProt[dir]
		if !pok {
			lowDir := strings.ToLower(dir)
			prot = ""
			for _, w := range wl {
				if strings.Contains(lowDir, w) {
					prot = w
					break
				}
			}
			dirProt[dir] = prot
		}
		if prot == "" && strings.Contains(strings.ToLower(base), ".env") {
			prot = ".env"
		}
		if prot != "" {
			sc.Protected[prot] += size
		}
		_ = rootLen
		return nil
	})

	// 深度 3 / 4 目录，按大小排序
	type kv struct {
		k string
		v uint64
	}
	var d3, d4 []kv
	for k, v := range nodes {
		// 绝对层级：并行分块扫描时每块 root 不同，用绝对深度保证语义一致
		depth := strings.Count(strings.TrimRight(k, `\`), `\`) - 1
		if depth == 3 {
			d3 = append(d3, kv{k, v})
		} else if depth == 4 && v >= 64<<20 {
			d4 = append(d4, kv{k, v})
		}
	}
	sort.Slice(d3, func(i, j int) bool { return d3[i].v > d3[j].v })
	sort.Slice(d4, func(i, j int) bool { return d4[i].v > d4[j].v })
	if len(d3) > 15 {
		d3 = d3[:15]
	}
	if len(d4) > 800 {
		d4 = d4[:800]
	}
	for _, e := range d3 {
		sc.Dirs3 = append(sc.Dirs3, DirEntry{Path: e.k, Bytes: e.v, Cat: classify(strings.ToLower(e.k + `\`))})
	}
	for _, e := range d4 {
		sc.Deep = append(sc.Deep, DirEntry{Path: e.k, Bytes: e.v, Cat: classify(strings.ToLower(e.k + `\`))})
	}
	sort.Slice(bigs, func(i, j int) bool { return bigs[i].bytes > bigs[j].bytes })
	if len(bigs) > 15 {
		bigs = bigs[:15]
	}
	for _, b := range bigs {
		sc.TopFiles = append(sc.TopFiles, FileEntry{Path: b.path, Bytes: b.bytes, MTime: b.mtime})
	}
	sc.Duration = time.Since(t0).Seconds()
	return sc
}

func runScan(cfg Config, st *Store) *Scan {
	// 无变更秒回：上次扫描后没有任何写入，直接复用结果
	if last := st.GetScan(); last != nil && last.Root == cfg.Root && usnActive.Load() && usnEventsSince(last.TS) == 0 && time.Since(time.Unix(last.TS, 0)) < 6*time.Hour {
		fmt.Println("自上次扫描后没有任何写入，跳过扫描")
		st.AddAlert("scan", "自上次扫描后没有任何写入，已复用上次结果")
		return last
	}
	scanBegin(cfg.Root)
	defer scanEnd()
	started := time.Now()
	fmt.Printf("扫描 %s（只读，并行）…\n", cfg.Root)
	sc := scanRootParallel(cfg)
	sc.Root = cfg.Root
	sc.Duration = time.Since(started).Seconds()
	if scanCancelled() {
		st.AddAlert("scan", "扫描已取消，结果未保存")
		return sc
	}
	vol, _ := volumeStats(cfg.Root)
	sc.Used, sc.Free = vol.Used, vol.Free
	st.SetScan(sc)
	appendSnapshot(cfg, sc)
	usnMark()
	fmt.Printf("完成：%d 文件 / %d 目录，合计 %s，耗时 %.1fs，跳过 %d 处\n",
		sc.Files, sc.Dirs, human(sc.Total), sc.Duration, sc.Errors)
	for i := 0; i < len(sc.Dirs3) && i < 6; i++ {
		e := sc.Dirs3[i]
		fmt.Printf("  %-60s %10s  %s\n", e.Path, human(e.Bytes), cats[e.Cat].Label)
	}
	return sc
}

// 简单占用统计（不扫描）
func quickUsed(root string) uint64 {
	v, _ := volumeStats(root)
	return v.Used
}

func ensureDir(p string) { _ = os.MkdirAll(p, 0o755) }

func parentDir(p string) string {
	if i := strings.LastIndexByte(p, '\\'); i > 0 {
		return p[:i]
	}
	return p
}
