package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------- 路径向导：识别"这是什么" ----------

type identity struct {
	App   string
	What  string
	IfDel string
	How   []string
	Level string
}

type extStat struct {
	Ext   string
	Bytes uint64
	Count int
}

type childInfo struct {
	Path  string
	Name  string
	Bytes uint64
	Cat   string
	Tier  string
	Clean bool
}

type pathGuide struct {
	Kind      string
	Exts      []extStat
	TopFiles  []FileEntry
	Children  []childInfo
	Truncated bool
	Scanned   int
}

type pathIdent struct {
	Needle string
	ID     identity
}

var identRules = []pathIdent{
	{`\google\chrome\`, identity{App: "Google Chrome", What: "浏览器数据（缓存、Cookie、扩展、配置）",
		IfDel: "Cache / Code Cache / GPUCache 是可重建的缓存，删了没影响；但 Cookie、登录状态、扩展和设置也在这个目录里，整体删掉需要重新登录、重新装扩展。",
		How: []string{"官方方式：Chrome → 设置 → 隐私和安全 → 清除浏览数据",
			"看子目录：Cache、Code Cache、GPUCache 可以清；User Data 里的 Default 含登录态和书签，别动"},
		Level: "confirm"}},
	{`\microsoft\edge\`, identity{App: "Microsoft Edge", What: "浏览器数据（缓存、Cookie、扩展、配置）",
		IfDel: "缓存可删；Cookie、登录、收藏、扩展整体删会丢。",
		How: []string{"Edge → 设置 → 隐私、搜索和服务 → 清除浏览数据",
			"只清 Cache / Code Cache / GPUCache 子目录最安全"},
		Level: "confirm"}},
	{`\tencent\`, identity{App: "腾讯系应用（微信/QQ 等）", What: "聊天缓存、接收的文件、表情包、更新包",
		IfDel: "聊天记录数据库和接收的文件可能在这里，整体删可能丢聊天记录或要重新登录；update / Temp / Cache 可以清。",
		How: []string{"官方方式：微信 → 设置 → 文件管理 → 清理缓存（不动聊天记录）",
			"看子目录名：update、Temp、Cache 可清；Msg、FileStorage、FileRecv 是聊天数据，别动"},
		Level: "confirm"}},
	{`\dingtalk\`, identity{App: "钉钉", What: "聊天缓存与接收文件",
		IfDel: "可能含聊天记录与文件，整体删有丢数据风险。",
		How: []string{"钉钉 → 设置 → 存储 → 清理缓存"}, Level: "confirm"}},
	{`\wechat files`, identity{App: "微信", What: "聊天记录与接收的文件",
		IfDel: "这是聊天数据本体，删了可能无法恢复。",
		How: []string{"在微信「设置 → 文件管理」里清理，或迁移到别的盘", "删前先另存重要文件"},
		Level: "required"}},
	{`\npm-cache`, identity{App: "Node.js npm", What: "npm 下载的包缓存",
		IfDel: "只是缓存，删了下次 npm install 会重新下载（变慢，但不会坏）。",
		How: []string{"推荐：npm cache clean --force"}, Level: "clean"}},
	{`\node_modules`, identity{App: "Node.js 项目", What: "项目依赖（第三方库代码）",
		IfDel: "删了项目就跑不起来，需要重新 npm install；这不是垃圾文件。",
		How: []string{"确认这是你自己的项目再决定", "清空间不如删掉整个旧项目"}, Level: "required"}},
	{`\huggingface`, identity{App: "Hugging Face", What: "AI 模型文件",
		IfDel: "删了要重新下载（几 GB 到几十 GB）。",
		How: []string{"看 hub 子目录里的模型名，确认不再用再删"}, Level: "confirm"}},
	{`\ollama`, identity{App: "Ollama", What: "本地 AI 模型",
		IfDel: "模型会被删除，需要 ollama pull 重新下载。",
		How: []string{"推荐：ollama rm <模型名>"}, Level: "confirm"}},
	{`\steamapps`, identity{App: "Steam", What: "游戏文件",
		IfDel: "游戏需要重新下载（可能几十 GB）。",
		How: []string{"在 Steam 里右键游戏 → 管理 → 卸载"}, Level: "confirm"}},
	{`\windows\winsxs`, identity{App: "Windows 组件存储", What: "系统组件与更新备份",
		IfDel: "手删会导致系统更新失败、组件损坏，且难以修复。",
		How: []string{"管理员运行 DISM /Online /Cleanup-Image /StartComponentCleanup", "或「磁盘清理 → 清理系统文件」"},
		Level: "required"}},
	{`\windows\installer`, identity{App: "Windows Installer 缓存", What: "MSI 安装包缓存",
		IfDel: "删了以后软件可能无法修复、无法卸载。",
		How: []string{"不要手删；用系统自带磁盘清理"}, Level: "required"}},
	{`\windows\softwaredistribution`, identity{App: "Windows Update", What: "系统更新下载缓存",
		IfDel: "删了会重新下载更新；一般安全，但可能让正在进行的更新失败。",
		How: []string{"设置 → 系统 → 存储 → 临时文件 → Windows 更新清理"}, Level: "confirm"}},
	{`\windows\logs`, identity{App: "Windows 日志", What: "系统日志与转储",
		IfDel: "可以删，系统会重新生成；删了就没有历史排错记录。",
		How: []string{"事件查看器可清日志；转储文件可直接删"}, Level: "clean"}},
	{`\windows\temp`, identity{App: "Windows 临时目录", What: "临时文件",
		IfDel: "可以删，正在使用的文件会被跳过。",
		How: []string{"系统磁盘清理也会清这里"}, Level: "clean"}},
	{`\appdata\local\temp`, identity{App: "应用临时目录", What: "各程序运行时的临时文件",
		IfDel: "可以删，正在运行的程序占用的会被跳过。",
		How: []string{"关掉不用的程序再清更彻底"}, Level: "clean"}},
	{`\crashdumps`, identity{App: "应用崩溃转储", What: "崩溃时的内存快照",
		IfDel: "可以删，只是排错用的。", Level: "clean"}},
	{`\appdata\local\programs`, identity{App: "已安装程序", What: "程序本体（exe/dll）",
		IfDel: "删了程序就没了，卸载记录还在，会变成残留。",
		How: []string{"用「设置 → 应用」卸载"}, Level: "required"}},
	{`\downloads`, identity{App: "下载文件夹", What: "你下载的文件",
		IfDel: "这是你的文件，不是缓存。", Level: "required"}},
	{`\documents`, identity{App: "文档", What: "你的文档", IfDel: "你的数据，不要删。", Level: "required"}},
	{`\pictures`, identity{App: "图片", What: "你的照片/图片", IfDel: "你的数据，不要删。", Level: "required"}},
	{`\videos`, identity{App: "视频", What: "你的视频", IfDel: "你的数据，不要删。", Level: "required"}},
	{`\appdata\`, identity{App: "应用数据目录", What: "某个应用保存的数据（配置、缓存、账号信息）",
		IfDel: "里面通常混着可重建的缓存和删了就丢的配置/账号。整体删可能丢设置或要重新登录。",
		How: []string{"先看子目录：带 Cache / Temp / Logs / GPUCache 的可以清",
			"带 Data、DB、sqlite、User Data 的通常含账号或历史，别删",
			"最稳的是在该应用自己的设置里清缓存"},
		Level: "confirm"}},
	{`pagefile.sys`, identity{App: "Windows 页面文件", What: "内存不足时的交换文件",
		IfDel: "系统运行必需，删了会出问题。", How: []string{"在「系统属性 → 高级 → 性能 → 虚拟内存」里调整"}, Level: "required"}},
	{`hiberfil.sys`, identity{App: "Windows 休眠文件", What: "休眠/快速启动用的内存镜像",
		IfDel: "删了休眠和快速启动会失效。", How: []string{"管理员运行 powercfg /h off 可关闭休眠并释放"}, Level: "required"}},
	{`$recycle.bin`, identity{App: "回收站", What: "已删除但未清空的文件",
		IfDel: "清空回收站即可，注意里面可能还有想找回的文件。", Level: "clean"}},
	{`\windows\`, identity{App: "Windows 系统目录", What: "系统组件与驱动",
		IfDel: "不要手动删，会破坏系统。", Level: "required"}},
}

func identifyPath(path, cat string) identity {
	low := strings.ToLower(strings.TrimRight(path, `\`) + `\`)
	for _, r := range identRules {
		if strings.Contains(low, r.Needle) {
			return r.ID
		}
	}
	switch cat {
	case "temp":
		return identity{App: "临时文件", What: "程序运行时产生的临时数据", IfDel: "可以删，程序会重新生成。", Level: "clean"}
	case "dev_cache":
		return identity{App: "开发工具缓存", What: "包管理器/构建工具的下载缓存", IfDel: "可以删，下次构建会重新下载。", Level: "clean"}
	case "logs_dumps":
		return identity{App: "日志", What: "程序运行日志与转储", IfDel: "可以删，只是排错记录。", Level: "clean"}
	case "program_data":
		return identity{App: "应用数据", What: "应用保存的数据", IfDel: "可能混有配置/账号信息，建议只清其中的缓存子目录。", Level: "confirm"}
	case "program_install":
		return identity{App: "程序安装目录", What: "程序本体", IfDel: "删了程序会坏，应该走卸载。", Level: "required"}
	case "user_docs", "user_profile":
		return identity{App: "用户数据", What: "你的文件/个人数据", IfDel: "这是你的数据，不要删。", Level: "required"}
	}
	return identity{App: "未知", What: "无法从路径判断用途", IfDel: "建议先按下面的「怎么确认」自己看一眼，再决定。", Level: "confirm"}
}

func extMeaning(ext string) (what, verdict string) {
	switch ext {
	case ".log", ".txt":
		return "日志/文本记录", "可删"
	case ".tmp", ".temp", ".~tmp", ".part", ".crdownload":
		return "临时文件/未完成下载", "可删"
	case ".db", ".sqlite", ".sqlite3", ".ldb", ".leveldb", ".mdb":
		return "数据库（可能含账号/聊天/历史）", "不要删"
	case ".json", ".ini", ".yaml", ".yml", ".toml", ".config", ".xml":
		return "配置（删了丢设置）", "不要删"
	case ".exe", ".dll", ".sys", ".msi", ".bat", ".cmd", ".ps1":
		return "程序/系统文件", "不要删"
	case ".pak", ".dat", ".bin", ".cache", ".blob", ".asset":
		return "程序资源/数据块", "看应用"
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".heic":
		return "图片", "你的文件"
	case ".mp4", ".mkv", ".mov", ".avi", ".webm":
		return "视频", "你的文件"
	case ".mp3", ".wav", ".flac", ".m4a":
		return "音频", "你的文件"
	case ".zip", ".7z", ".rar", ".tar", ".gz":
		return "压缩包", "你的文件"
	case ".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx":
		return "文档", "你的文件"
	case ".pdb", ".dmp", ".mdmp":
		return "崩溃调试文件", "可删"
	}
	if ext == "" || ext == "." {
		return "无扩展名（看不出类型）", "不确定"
	}
	return "未知类型", "不确定"
}

// analyzePathGuide 有预算地分析目录内容：类型分布、最大文件、子目录
func analyzePathGuide(cfg Config, st *Store, path string, isDir bool, budget time.Duration) pathGuide {
	g := pathGuide{Kind: "文件"}
	deadline := time.Now().Add(budget)
	exts := map[string]*extStat{}
	var top []FileEntry
	addFile := func(p string, size uint64) {
		if strings.HasPrefix(strings.ToLower(p), strings.ToLower(cfg.DataDir)) {
			return
		}
		ext := strings.ToLower(filepath.Ext(p))
		s := exts[ext]
		if s == nil {
			s = &extStat{Ext: ext}
			exts[ext] = s
		}
		s.Bytes += size
		s.Count++
		top = append(top, FileEntry{Path: p, Bytes: size})
		if len(top) > 200 {
			sort.Slice(top, func(i, j int) bool { return top[i].Bytes > top[j].Bytes })
			top = top[:60]
		}
	}
	if !isDir {
		if info, err := os.Stat(path); err == nil {
			addFile(path, uint64(info.Size()))
		}
	} else {
		g.Kind = "文件夹"
		_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if time.Now().After(deadline) || g.Scanned > 40000 {
				g.Truncated = true
				return filepath.SkipAll
			}
			if err != nil || d.IsDir() {
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil {
				return nil
			}
			g.Scanned++
			addFile(p, uint64(info.Size()))
			return nil
		})
		sizes := childSizesFromScan(st, path)
		entries, _ := os.ReadDir(path)
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), "$") {
				continue
			}
			cp := filepath.Join(path, e.Name())
			size := sizes[strings.ToLower(cp)]
			if size == 0 {
				if s, ok := quickDirSize(cp, 350*time.Millisecond); ok {
					size = s
				}
			}
			cat := classify(strings.ToLower(cp + `\`))
			meta := cats[cat]
			nameLower := strings.ToLower(e.Name())
			looksCache := strings.Contains(nameLower, "cache") || strings.Contains(nameLower, "temp") ||
				strings.Contains(nameLower, "update") || strings.Contains(nameLower, "log") ||
				strings.Contains(nameLower, "dump") || strings.Contains(nameLower, "gpucache")
			g.Children = append(g.Children, childInfo{
				Path: cp, Name: e.Name(), Bytes: size, Cat: cat, Tier: meta.Tier,
				Clean: (meta.Tier == "clean" || looksCache) && !deniedForCleanup(cp) && !whitelisted(cfg, cp),
			})
		}
		sort.Slice(g.Children, func(i, j int) bool { return g.Children[i].Bytes > g.Children[j].Bytes })
		if len(g.Children) > 12 {
			g.Children = g.Children[:12]
		}
	}
	sort.Slice(top, func(i, j int) bool { return top[i].Bytes > top[j].Bytes })
	if len(top) > 5 {
		top = top[:5]
	}
	g.TopFiles = top
	for _, s := range exts {
		g.Exts = append(g.Exts, *s)
	}
	sort.Slice(g.Exts, func(i, j int) bool { return g.Exts[i].Bytes > g.Exts[j].Bytes })
	if len(g.Exts) > 8 {
		g.Exts = g.Exts[:8]
	}
	return g
}

func quickDirSize(root string, budget time.Duration) (uint64, bool) {
	deadline := time.Now().Add(budget)
	var bytes uint64
	n := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if time.Now().After(deadline) || n > 20000 {
			return filepath.SkipAll
		}
		if err != nil || d.IsDir() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			bytes += uint64(info.Size())
			n++
		}
		return nil
	})
	return bytes, n > 0
}

// childSizesFromScan 从扫描数据估算一级子目录大小
func childSizesFromScan(st *Store, parent string) map[string]uint64 {
	out := map[string]uint64{}
	sc := st.GetScan()
	if sc == nil {
		return out
	}
	base := strings.TrimRight(parent, `\`)
	pl := strings.ToLower(base) + `\`
	add := func(p string, b uint64) {
		lp := strings.ToLower(p)
		if !strings.HasPrefix(lp, pl) {
			return
		}
		rest := lp[len(pl):]
		if i := strings.IndexByte(rest, '\\'); i > 0 {
			rest = rest[:i]
		}
		out[strings.ToLower(base+`\`+rest)] += b
	}
	for _, d := range sc.Deep {
		add(d.Path, d.Bytes)
	}
	for _, d := range sc.Dirs3 {
		add(d.Path, d.Bytes)
	}
	return out
}

func whitelisted(cfg Config, p string) bool {
	low := strings.ToLower(p + `\`)
	for _, w := range cfg.Whitelist {
		if w != "" && strings.Contains(low, strings.ToLower(w)) {
			return true
		}
	}
	return false
}
