package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// winapp2.ini 兼容解析器。
//
// 为什么做这个：BleachBit 用 XML、FluentCleaner 用 winapp2.ini、Dism++ 用 Data.xml——
// 三家的规则库都是外置的，而我们的 20 类是硬编码的。FluentCleaner 作者的原话是
// CCleaner 活下来靠的就是 winapp2.ini 这个生态。接一层解析器，社区规则库零成本继承。
//
// **安全边界**：这个解析器只产出「建议」，不改任何文件、不进清理流水线。
// 原因很直白：winapp2.ini 是社区维护的，里面既有写错路径的条目，也有整段删系统目录的
// 条目。让它直接驱动删除，等于把别人的失误变成我们的数据事故。
// 所以这里做三件事：解析、把路径展开成绝对路径、标出「越界」的条目。
// 真删不在这里。

// Winapp2FileKey 是一条 FileKeyN。
type Winapp2FileKey struct {
	Raw        string   // 原始值，便于排查
	Path       string   // 展开后的绝对路径（不含通配符部分）
	Patterns   []string // *.tmp 之类；空表示目录下全部
	Recurse    bool
	RemoveSelf bool
	Resolved   bool // %变量% 是否全部认得
}

// Winapp2Entry 是一个 [段落]。
type Winapp2Entry struct {
	Name         string
	LangSecRef   string
	Detect       []string
	Default      bool
	Warning      string
	Section      string
	Files        []Winapp2FileKey
	RegistryKeys int  // 我们不碰注册表，统计出来只用于告知
	Excludes     int  // 暂不支持 ExcludeKey，出现了就要提示
	Unsupported  bool // 只有注册表项 → 跳过
}

// Winapp2Result 是整份文件的解析结果。
type Winapp2Result struct {
	Entries  []Winapp2Entry
	Warnings []string
}

var winapp2VarRe = regexp.MustCompile(`%([A-Za-z0-9_()]+)%`)

// winapp2Vars 给出 %变量% 的展开表（键统一小写）。
func winapp2Vars() map[string]string {
	v := make(map[string]string, 24)
	for _, k := range []string{
		"LOCALAPPDATA", "APPDATA", "PROGRAMDATA", "PROGRAMFILES", "WINDIR",
		"SYSTEMROOT", "TEMP", "TMP", "SYSTEMDRIVE", "PUBLIC", "ALLUSERSPROFILE", "USERPROFILE",
	} {
		if val := os.Getenv(k); val != "" {
			v[strings.ToLower(k)] = val
		}
	}
	// ProgramFiles(x86) 在 winapp2 里常写成 %ProgramFiles(x86)%
	if val := os.Getenv("ProgramFiles(x86)"); val != "" {
		v["programfiles(x86)"] = val
	}
	if home, err := os.UserHomeDir(); err == nil {
		v["userprofile"] = home
		for name, sub := range map[string]string{
			"documents": "Documents", "desktop": "Desktop", "downloads": "Downloads",
			"music": "Music", "pictures": "Pictures", "videos": "Videos",
		} {
			v[name] = filepath.Join(home, sub)
		}
	}
	return v
}

// winapp2UnsafePath 判断一条解析出来的路径是不是越界了。
//
// 以前的规矩是「只拦等于某个系统根的情况」，理由是 %WinDir%\Temp 这类是合法目标。
// 但只拦相等，等于把最该拦的那些也放行了：`C:\Windows\System32`、
// `C:\Users\<你>\Documents` 都被判成安全——而工具自己的清理器对 Documents 是拒绝的，
// 两套语义就此分叉。
//
// 现在的规矩：系统根下面的子目录默认算越界，只放行明确列出的缓存/临时位置；
// Users 下面只放行 AppData\Local 与 AppData\LocalLow，其余（Documents、Desktop、
// Downloads、Pictures、Roaming……）一律算越界。
//
// 判不准就往「越界」那边靠：被标越界的条目只是不被采纳，误放行的条目会在将来
// 接线时变成真实风险。Roaming 里放的是浏览器配置、聊天记录这类用户数据，
// 所以它跟 AppData\Local 区别对待，不是笔误。
func winapp2UnsafePath(p string) bool {
	clean := filepath.Clean(p)
	if strings.Contains(clean, "..") || !filepath.IsAbs(clean) {
		return true
	}
	vol := filepath.VolumeName(clean)
	if vol == "" {
		return true
	}
	if strings.EqualFold(clean, vol) || strings.EqualFold(clean, vol+`\`) {
		return true
	}
	sep := string(os.PathSeparator)
	// under 判断 clean 是否落在 vol\rel 这一支下面（含自身）。
	// 必须隔着分隔符才算，否则 `%WinDir%\TempX` 会蹭 `%WinDir%\Temp` 的豁免。
	under := func(rel string) bool {
		prefix := vol + sep + rel
		if strings.EqualFold(clean, prefix) {
			return true
		}
		return len(clean) > len(prefix) &&
			strings.EqualFold(clean[:len(prefix)], prefix) &&
			clean[len(prefix)] == os.PathSeparator
	}

	// 系统根：默认越界，只放行这些子目录。
	for _, root := range []string{
		"Windows", "Program Files", "Program Files (x86)",
		"ProgramData", "System Volume Information",
	} {
		if !under(root) {
			continue
		}
		for _, ok := range []string{
			`Windows\Temp`,
			`Windows\Logs`,
			`Windows\Prefetch`,
			`Windows\Minidump`,
			`Windows\LiveKernelReports`,
			`Windows\SoftwareDistribution\Download`,
			`ProgramData\Microsoft\Windows\WER`,
		} {
			if under(ok) {
				return false
			}
		}
		return true
	}

	// Users：只放行 AppData 下的两个缓存分支，用户的数据目录全拦。
	if under("Users") {
		parts := strings.Split(strings.TrimPrefix(clean, vol+sep), sep)
		if len(parts) >= 4 && strings.EqualFold(parts[2], "AppData") &&
			(strings.EqualFold(parts[3], "Local") || strings.EqualFold(parts[3], "LocalLow")) {
			return false
		}
		return true
	}
	return false
}

// parseWinapp2FileKey 解析 `<path>|<pattern>|<FLAG>` 这种值。
func parseWinapp2FileKey(raw string, vars map[string]string) Winapp2FileKey {
	k := Winapp2FileKey{Raw: raw}
	parts := strings.Split(raw, "|")
	if len(parts) == 0 {
		return k
	}
	rawPath := strings.TrimSpace(parts[0])

	// 展开 %变量%；认不全就不下结论
	allKnown := true
	expanded := winapp2VarRe.ReplaceAllStringFunc(rawPath, func(m string) string {
		name := strings.ToLower(m[1 : len(m)-1])
		if val, hit := vars[name]; hit && val != "" {
			return val
		}
		allKnown = false
		return m
	})
	if allKnown {
		k.Path = filepath.Clean(expanded)
		k.Resolved = true
	} else {
		k.Path = expanded
	}

	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		switch strings.ToUpper(p) {
		case "RECURSE":
			k.Recurse = true
		case "REMOVESELF":
			k.RemoveSelf = true
		case "RECURSE=", "REMOVESELF=":
			// 形如 RECURSE=1 的写法少见，忽略
		default:
			k.Patterns = append(k.Patterns, p)
		}
	}
	return k
}

// ParseWinapp2 只解析、不执行。
func ParseWinapp2(text string) Winapp2Result {
	vars := winapp2Vars()
	res := Winapp2Result{}
	lines := strings.Split(text, "\n")

	var cur *Winapp2Entry
	flush := func() {
		if cur == nil {
			return
		}
		if len(cur.Files) == 0 && cur.RegistryKeys > 0 {
			cur.Unsupported = true
		}
		res.Entries = append(res.Entries, *cur)
		cur = nil
	}

	for i, line := range lines {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "//") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			flush()
			cur = &Winapp2Entry{Name: strings.TrimSpace(line[1 : len(line)-1])}
			continue
		}
		if cur == nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("第 %d 行：出现在任何段落之前，已忽略", i+1))
			continue
		}
		eq := strings.Index(line, "=")
		if eq <= 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf("第 %d 行：不是 key=value，已忽略（%q）", i+1, line))
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		lower := strings.ToLower(key)

		switch {
		case lower == "langsecref":
			cur.LangSecRef = val
		case lower == "detect":
			cur.Detect = append(cur.Detect, val)
		case lower == "default":
			cur.Default = strings.EqualFold(val, "true")
		case lower == "warning":
			cur.Warning = val
		case lower == "section":
			cur.Section = val
		case strings.HasPrefix(lower, "filekey"):
			if val == "" {
				continue
			}
			fk := parseWinapp2FileKey(val, vars)
			if fk.Resolved && winapp2UnsafePath(fk.Path) {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("%s：路径越界，已标记（%s）", cur.Name, fk.Path))
			}
			cur.Files = append(cur.Files, fk)
		case strings.HasPrefix(lower, "regkey"):
			cur.RegistryKeys++
		case strings.HasPrefix(lower, "excludekey"):
			cur.Excludes++
		default:
			// LangSecRef 之外还有很多我们不认识的键，静默跳过即可
		}
	}
	flush()
	return res
}

// Winapp2Summary 给 CLI 用：只报告，不清理。
func Winapp2Summary(res Winapp2Result) (entries, withFiles, registryOnly, unsafe, unresolved int) {
	entries = len(res.Entries)
	for _, e := range res.Entries {
		if e.Unsupported {
			registryOnly++
			continue
		}
		if len(e.Files) > 0 {
			withFiles++
		}
		for _, f := range e.Files {
			if !f.Resolved {
				unresolved++
				break
			}
			if winapp2UnsafePath(f.Path) {
				unsafe++
				break
			}
		}
	}
	return
}

// reportWinapp2 是 `diskmon-go.exe winapp2 <file>` 的实现：只读，不删任何东西。
func reportWinapp2(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("读不到 %s：%v\n", path, err)
		return
	}
	res := ParseWinapp2(string(b))
	total, withFiles, registryOnly, unsafe, unresolved := Winapp2Summary(res)

	fmt.Printf("解析 %s\n", path)
	fmt.Printf("  段落 %d 个：可产出文件规则 %d，仅注册表（跳过）%d\n", total, withFiles, registryOnly)
	if unsafe > 0 || unresolved > 0 {
		fmt.Printf("  ⚠ 越界 %d 个、变量未解析 %d 个——这些条目不会被采纳\n", unsafe, unresolved)
	}
	for _, e := range res.Entries {
		if e.Unsupported || len(e.Files) == 0 {
			continue
		}
		fmt.Printf("\n[%s]", e.Name)
		if e.Warning != "" {
			fmt.Printf("  ⚠ %s", e.Warning)
		}
		fmt.Println()
		for _, f := range e.Files {
			flag := ""
			if !f.Resolved {
				flag = "  (变量未解析)"
			} else if winapp2UnsafePath(f.Path) {
				flag = "  (越界，忽略)"
			} else if _, err := os.Stat(f.Path); err == nil {
				flag = "  (存在)"
			}
			if f.Recurse {
				flag += " [递归]"
			}
			pats := "*"
			if len(f.Patterns) > 0 {
				pats = strings.Join(f.Patterns, ",")
			}
			fmt.Printf("    %s  %s%s\n", f.Path, pats, flag)
		}
	}
	if len(res.Warnings) > 0 {
		fmt.Printf("\n解析警告 %d 条：\n", len(res.Warnings))
		for i, w := range res.Warnings {
			if i >= 10 {
				fmt.Printf("  … 还有 %d 条\n", len(res.Warnings)-10)
				break
			}
			fmt.Printf("  - %s\n", w)
		}
	}
	fmt.Println("\n以上只是解析结果，没有删除或移动任何文件。")
}
