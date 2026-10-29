package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// winapp2 的接线。
//
// 解析器当初故意不接清理流水线（见 winapp2.go 顶部），理由是社区规则里既有写错路径的，
// 也有整段删系统目录的。那个理由现在依然成立，所以接线不是「照规则删」，而是三道闸：
//
//	1. 规则层：只采纳变量解析成功、且没越界（winapp2UnsafePath）的条目；
//	2. 分类层：每条还要过 guardPath，档位必须是 clean 才进候选；
//	3. 执行层：用户点下去走的是隔离区（q.apply），不是 removePath。
//
// 任何一道不通过，该条目只被列出来给用户看原因，不能被处理。
// 这三道不是重复劳动——它们拒绝的成因完全不同：规则写错、路径太广、分类认为该留着。

// Winapp2Group 是一段 winapp2 规则在这台机器上的实际命中。
type Winapp2Group struct {
	Name    string   // 段落名，比如 "Adobe Flash Player"
	Paths   []string // 可隔离的路径（已过越界与档位两道检查）
	Bytes   uint64   // 命中文件的总字节
	Files   int      // 命中文件数
	Skipped []string // 被挡下的路径与原因，照原样展示，不隐藏
}

// winapp2FilePath 给出规则文件位置：配置里指了就用它，否则 <数据目录>\winapp2.ini。
//
// 走 expandVars 而不是 os.ExpandEnv：用户会写成 %USERPROFILE%\winapp2.ini，
// 而 os.ExpandEnv 不认 %VAR% 这种 Windows 写法（rules_ui 那边踩过同一个坑）。
func winapp2FilePath(cfg Config) string {
	if p := strings.TrimSpace(cfg.Winapp2Path); p != "" {
		return expandPath(p)
	}
	return filepath.Join(cfg.DataDir, "winapp2.ini")
}

// winapp2MatchFiles 列出某个 FileKey 实际会命中哪些文件，并求和。
//
// 通配符按基名匹配；Recurse 决定是只扫一层还是走到底。
// 路径本身是单个文件时，只有模式为空或模式命中它才采纳。
func winapp2MatchFiles(fk Winapp2FileKey) (files []string, bytes uint64) {
	match := func(name string) bool {
		if len(fk.Patterns) == 0 {
			return true
		}
		for _, pat := range fk.Patterns {
			if ok, err := filepath.Match(strings.ToLower(pat), strings.ToLower(name)); err == nil && ok {
				return true
			}
		}
		return false
	}

	info, err := os.Stat(fk.Path)
	if err != nil {
		return nil, 0
	}
	if !info.IsDir() {
		if match(info.Name()) {
			return []string{fk.Path}, uint64(info.Size())
		}
		return nil, 0
	}
	if fk.Recurse {
		_ = filepath.Walk(fk.Path, func(p string, fi os.FileInfo, werr error) error {
			if werr != nil || fi == nil || fi.IsDir() {
				return nil
			}
			if match(fi.Name()) {
				files = append(files, p)
				bytes += uint64(fi.Size())
			}
			return nil
		})
		return files, bytes
	}
	ents, rerr := os.ReadDir(fk.Path)
	if rerr != nil {
		return nil, 0
	}
	for _, e := range ents {
		if e.IsDir() || !match(e.Name()) {
			continue
		}
		if fi, ferr := e.Info(); ferr == nil {
			files = append(files, filepath.Join(fk.Path, e.Name()))
			bytes += uint64(fi.Size())
		}
	}
	return files, bytes
}

// winapp2PathAllowed 是第一道闸：这条路径准不准进候选。
//
// 单独抽成变量是为了测试。窗口位置类的用例需要「写得了 + 不算越界」的目录，
// 而这台机器上它不存在：Users\Documents 被越界规则正确拦掉，AppData\Local 又建不了目录。
// 与其为了测试放松真实规则，不如把判断本身当成可替换的一层——
// 越界规则自身由 TestWinapp2UnsafePathCoversUserData 直接钉住，不走这个接缝。
var winapp2PathAllowed = func(p string) bool { return !winapp2UnsafePath(p) }

// winapp2Scan 读规则文件并算出候选项。没放规则文件不是错误，返回空即可。
//
// 返回值里保留「一条路都没通过」的段落——用户有权知道自己装的规则为什么没生效，
// 而不是页面上空得像是规则不存在。
func winapp2Scan(cfg Config) ([]Winapp2Group, []string) {
	b, err := os.ReadFile(winapp2FilePath(cfg))
	if err != nil {
		return nil, nil
	}
	res := ParseWinapp2(string(b))

	var groups []Winapp2Group
	for _, e := range res.Entries {
		if e.Unsupported || len(e.Files) == 0 {
			continue
		}
		g := Winapp2Group{Name: e.Name}
		// ExcludeKey 是「这些别动」。我们还不支持排除语义，那就不能只照 FileKey 动手——
		// 否则规则特意留下的文件会被一起挪走。整段不采纳，比猜一个近似行为安全。
		if e.Excludes > 0 {
			g.Skipped = append(g.Skipped, fmt.Sprintf(
				"这一段声明了 %d 条 ExcludeKey（排除项），我们还不支持排除，所以整段不采纳", e.Excludes))
			groups = append(groups, g)
			continue
		}
		seen := make(map[string]bool, len(e.Files))
		for _, fk := range e.Files {
			if !fk.Resolved {
				g.Skipped = append(g.Skipped, fk.Raw+"：变量没能解析成真实路径")
				continue
			}
			if !winapp2PathAllowed(fk.Path) {
				g.Skipped = append(g.Skipped, fk.Path+"：路径越界，规则不采纳")
				continue
			}
			// guardPath 是清理器自己的门，不因为「规则这么写」就绕过它。
			_, tier, refuse := guardPath(cfg, fk.Path)
			if tier != "clean" || refuse != "" {
				reason := refuse
				if reason == "" {
					reason = "分类档位是 " + tier + "，这里只处理 clean 档"
				}
				g.Skipped = append(g.Skipped, fk.Path+"："+reason)
				continue
			}
			if seen[fk.Path] {
				continue
			}
			files, n := winapp2MatchFiles(fk)
			if len(files) == 0 {
				continue // 路径不存在或没命中，不占版面
			}
			seen[fk.Path] = true
			g.Paths = append(g.Paths, fk.Path)
			g.Bytes += n
			g.Files += len(files)
		}
		if len(g.Paths) > 0 || len(g.Skipped) > 0 {
			groups = append(groups, g)
		}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Bytes != groups[j].Bytes {
			return groups[i].Bytes > groups[j].Bytes
		}
		return groups[i].Name < groups[j].Name
	})
	return groups, res.Warnings
}

// Winapp2ApplyResult 是一次「隔离这一段规则」的结论。
type Winapp2ApplyResult struct {
	Name    string
	Moved   int
	Bytes   uint64
	Refused []string
}

// winapp2Apply 把一段规则命中的路径逐条移进隔离区。
//
// 重新扫一遍而不是收下前端传来的路径：页面上的候选可能已经过期，
// 而且不能让 HTTP 参数直接决定要动哪些文件。
func winapp2Apply(cfg Config, name string) (Winapp2ApplyResult, error) {
	out := Winapp2ApplyResult{Name: name}
	groups, _ := winapp2Scan(cfg)
	var target *Winapp2Group
	for i := range groups {
		if groups[i].Name == name {
			target = &groups[i]
			break
		}
	}
	if target == nil {
		return out, fmt.Errorf("没有这段规则，或者它没有任何可处理的路径：%s", name)
	}
	// 「一条路都没通过」不是成功。移了 0 个文件却回一句「已完成」，
	// 比明确说「这一组动不了，原因在此」更糟——用户会以为清理过了。
	if len(target.Paths) == 0 {
		reason := "没有可处理的路径"
		if len(target.Skipped) > 0 {
			reason = target.Skipped[0]
		}
		return out, fmt.Errorf("「%s」这一组动不了：%s", name, reason)
	}
	q := openQuarantine(cfg)
	for _, p := range target.Paths {
		e, err := q.apply(cfg, p, "winapp2: "+name)
		if err != nil {
			out.Refused = append(out.Refused, p+"："+err.Error())
			continue
		}
		out.Moved++
		out.Bytes += e.Bytes
	}
	return out, nil
}

// winapp2CardHTML 是清理中心里的那张卡。
func winapp2CardHTML(cfg Config) string {
	groups, _ := winapp2Scan(cfg)
	var b strings.Builder
	b.WriteString(`<div class="card" id="winapp2"><h2>社区规则（winapp2.ini）</h2>`)
	b.WriteString(fmt.Sprintf(`<p class="muted">把 winapp2.ini 放到 <code>%s</code>，这里会按它的段落列出可以清的东西。规则只负责提议：越界的路径、非 clean 档的路径一律只展示原因、不给按钮。</p>`,
		htmlEscape(winapp2FilePath(cfg))))

	if len(groups) == 0 {
		b.WriteString(`<div class="empty">还没有读到规则文件。放一份 winapp2.ini 进去，刷新这一页即可。</div></div>`)
		return b.String()
	}

	var totalBytes uint64
	for _, g := range groups {
		totalBytes += g.Bytes
	}
	b.WriteString(fmt.Sprintf(`<div class="muted" style="margin-bottom:8px">%d 段有命中 · 合计 %s</div>`, len(groups), human(totalBytes)))
	b.WriteString(`<table><tr><th>规则</th><th style="text-align:right">命中</th><th style="text-align:right">体积</th><th></th></tr>`)
	for _, g := range groups {
		action := `<span class="muted">无可处理路径</span>`
		if len(g.Paths) > 0 {
			action = fmt.Sprintf(`<form method="post" action="/winapp2/apply" style="display:inline" onsubmit="return confirm('把「%s」命中的文件移进隔离区？7 天内可还原。')"><input type="hidden" name="name" value="%s"><button class="act" type="submit">隔离这一组</button></form>`,
				htmlEscape(g.Name), htmlEscape(g.Name))
		}
		b.WriteString(fmt.Sprintf(`<tr><td>%s</td><td class="r">%d 个文件</td><td class="r">%s</td><td>%s</td></tr>`,
			htmlEscape(g.Name), g.Files, human(g.Bytes), action))
	}
	b.WriteString(`</table>`)

	// 被挡下的原因必须能看见——否则用户只会觉得「规则装了却没反应」。
	var skipped []string
	for _, g := range groups {
		for _, s := range g.Skipped {
			skipped = append(skipped, "["+g.Name+"] "+s)
		}
	}
	if len(skipped) > 0 {
		if len(skipped) > 12 {
			skipped = skipped[:12]
		}
		b.WriteString(fmt.Sprintf(`<details style="margin-top:8px"><summary class="muted">%d 条路径被挡下（点开看原因）</summary><ul class="muted">`, len(skipped)))
		for _, s := range skipped {
			b.WriteString("<li>" + htmlEscape(s) + "</li>")
		}
		b.WriteString(`</ul></details>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}
