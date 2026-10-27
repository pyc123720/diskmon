package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---------- 查路径（向导版，异步加载） ----------

func inspectPage(cfg Config, st *Store, raw string) string {
	var b strings.Builder
	b.WriteString(`<h2 style="margin-top:0">查路径</h2>`)
	b.WriteString(`<p class="muted">粘贴任意文件或文件夹路径。它会先告诉你「这是什么、删了会怎样」，再给你可执行的下一步。不会删除任何东西。</p>`)
	b.WriteString(`<form method="get" action="/inspect" style="max-width:820px;margin-bottom:14px">
<input name="path" value="` + htmlEscape(raw) + `" placeholder="C:\Users\...\AppData\Local\Temp 或 %TEMP%" style="width:100%;padding:11px 12px;background:#1c212b;border:1px solid #2a3240;border-radius:10px;color:#e7ebf2;font-size:14px">
<div style="margin-top:10px"><button class="btn" type="submit" style="cursor:pointer;background:#1f6f4a;border-color:#33c179">查一下</button>
<a class="btn" href="/inspect?path=%25TEMP%25">看 %TEMP%</a>
<a class="btn" href="/inspect?path=` + urlQuery(os.Getenv("USERPROFILE")+`\\Downloads`) + `">看 Downloads</a></div></form>`)
	b.WriteString(`<div id="ires">`)
	if strings.TrimSpace(raw) == "" {
		b.WriteString(`<div class="muted">输入路径后回车，或点上面的示例。</div>`)
	} else {
		b.WriteString(`<div class="card" id="loading"><div style="display:flex;align-items:center;gap:10px"><span class="spin"></span><b>正在分析这个路径…</b></div>`)
		b.WriteString(`<div class="muted" style="margin-top:6px">最多几秒。正在统计大小、识别文件类型、拆分子目录。</div>`)
		b.WriteString(`<div style="height:6px;background:#1c212b;border-radius:4px;overflow:hidden;margin-top:12px"><div class="sweep"></div></div></div>`)
	}
	b.WriteString(`</div>`)
	b.WriteString(`<style>
.spin{width:14px;height:14px;border:2px solid #33c17933;border-top-color:#33c179;border-radius:50%;display:inline-block;animation:sp 0.8s linear infinite}
@keyframes sp{to{transform:rotate(360deg)}}
.sweep{height:100%;width:40%;background:linear-gradient(90deg,transparent,#33c179,transparent);animation:sw 1.2s ease-in-out infinite}
@keyframes sw{0%{margin-left:-40%}100%{margin-left:100%}}
</style>`)
	if strings.TrimSpace(raw) != "" {
		b.WriteString(`<script>
(function(){
  var raw = ` + jsQuote(raw) + `;
  fetch('/inspect/result?path=' + encodeURIComponent(raw)).then(function(r){return r.text()}).then(function(h){
    var box = document.getElementById('ires');
    if (box) box.innerHTML = h;
  }).catch(function(e){
    var box = document.getElementById('ires');
    if (box) box.innerHTML = '<div class="warn">分析失败：' + e + '</div>';
  });
})();
</script>`)
	}
	return appShell("查路径", "inspect", b.String())
}

// inspectBody 只渲染结果部分，供 /inspect/result 异步返回
func inspectBody(cfg Config, st *Store, raw string) string {
	var b strings.Builder
	path := expandPath(raw)
	if path == "" {
		return `<div class="warn">路径为空。</div>`
	}
	stInfo, err := os.Stat(path)
	if err != nil {
		b.WriteString(`<div class="card"><h2><code>` + htmlEscape(path) + `</code></h2>`)
		b.WriteString(`<div class="warn">路径不存在或不可读：` + htmlEscape(err.Error()) + `</div>`)
		b.WriteString(`<a class="btn" href="/inspect">再试一个</a></div>`)
		return b.String()
	}
	r := rehearseQuick(cfg, path, 3*time.Second)
	meta := cats[r.Cat]
	kind := "文件夹"
	if !stInfo.IsDir() {
		kind = "文件"
	}
	ident := identifyPath(path, r.Cat)
	guide := analyzePathGuide(cfg, st, path, stInfo.IsDir(), 1200*time.Millisecond)

	cacheDir := false
	if base := strings.ToLower(filepath.Base(path)); base != "" {
		for _, k := range []string{"cache", "temp", "tmp", "log", "logs", "dump", "dumps", "gpucache"} {
			if strings.Contains(base, k) {
				cacheDir = true
				break
			}
		}
	}

	verdict, vcolor, vicon := "", "#f5a524", "⚠"
	switch {
	case r.Refuse != "":
		verdict, vcolor, vicon = "不要动", "#f5486a", "⛔"
	case meta.Tier == "clean" || ident.Level == "clean" || (cacheDir && ident.Level != "required"):
		verdict, vcolor, vicon = "可以删（会自动重建）", "#33c179", "✓"
	case meta.Tier == "required" || ident.Level == "required":
		verdict, vcolor, vicon = "不要删", "#f5486a", "⛔"
	default:
		verdict = "不建议整体删"
	}
	b.WriteString(`<div class="card" style="border-color:` + vcolor + `55">`)
	b.WriteString(`<div style="display:flex;gap:14px;align-items:flex-start;flex-wrap:wrap">`)
	b.WriteString(fmt.Sprintf(`<div style="font-size:26px;line-height:1.2;color:%s">%s</div>`, vcolor, vicon))
	b.WriteString(`<div style="flex:1;min-width:260px">`)
	b.WriteString(`<h2 style="margin:0 0 4px">` + htmlEscape(verdict) + `</h2>`)
	b.WriteString(`<div><b>` + htmlEscape(ident.App) + `</b> · ` + htmlEscape(ident.What) + `</div>`)
	b.WriteString(`<div class="muted" style="margin-top:6px">` + htmlEscape(ident.IfDel) + `</div>`)
	if r.Refuse != "" {
		b.WriteString(`<div class="warn" style="margin-top:8px"><b>为什么不能碰：</b>` + htmlEscape(r.Refuse) + `</div>`)
	}
	b.WriteString(`</div></div>`)
	b.WriteString(fmt.Sprintf(`<div class="muted" style="margin-top:10px"><code>%s</code> · %s · <b>%s</b> · %d 个文件 · 修改于 %s</div>`,
		htmlEscape(path), kind, human(r.Bytes), r.Files, stInfo.ModTime().Format("2006-01-02 15:04")))
	if len(r.Locked) > 0 {
		b.WriteString(fmt.Sprintf(`<div class="warn" style="margin-top:6px">%d 个文件正被占用：清理时会被跳过，不影响其它文件。</div>`, len(r.Locked)))
	}
	b.WriteString(`</div>`)

	b.WriteString(`<div class="card"><h2>1 · 里面有什么</h2>`)
	if guide.Truncated || r.Truncated {
		b.WriteString(`<div class="muted">目录较大，这里只统计了前 1.2 秒遍历到的部分；总数以上面的完整统计为准。</div>`)
	}
	if len(guide.Exts) > 0 {
		b.WriteString(`<table><tr><th>文件类型</th><th style="text-align:right">大小</th><th style="text-align:right">数量</th><th>这类文件是什么</th><th>建议</th></tr>`)
		for _, e := range guide.Exts {
			what, ver := extMeaning(e.Ext)
			ext := e.Ext
			if ext == "" {
				ext = "(无扩展名)"
			}
			b.WriteString(fmt.Sprintf(`<tr><td><code>%s</code></td><td class="r">%s</td><td class="r">%d</td><td>%s</td><td class="muted">%s</td></tr>`,
				htmlEscape(ext), human(e.Bytes), e.Count, htmlEscape(what), htmlEscape(ver)))
		}
		b.WriteString(`</table>`)
	} else {
		b.WriteString(`<div class="muted">没有读到文件。</div>`)
	}
	if len(guide.TopFiles) > 0 {
		b.WriteString(`<div style="margin-top:12px"><b>最大的几个文件</b></div><table><tr><th>文件</th><th style="text-align:right">大小</th></tr>`)
		for _, f := range guide.TopFiles {
			b.WriteString(fmt.Sprintf(`<tr><td><code>%s</code></td><td class="r">%s</td></tr>`, htmlEscape(f.Path), human(f.Bytes)))
		}
		b.WriteString(`</table>`)
	}
	b.WriteString(`</div>`)

	if len(guide.Children) > 0 {
		b.WriteString(`<div class="card"><h2>2 · 子目录拆开来看看</h2>`)
		b.WriteString(`<div class="muted" style="margin-bottom:8px">很多时候整个目录不能删，但其中某个子目录可以。先看名字和分类，再点「单独查」。</div>`)
		b.WriteString(`<table><tr><th>子目录</th><th style="text-align:right">大小</th><th>分类</th><th>建议</th><th></th></tr>`)
		for _, c := range guide.Children {
			catLabel := cats[c.Cat].Label
			advice := tierText[c.Tier]
			act := `<a class="btn" href="/inspect?path=` + urlQuery(c.Path) + `">单独查</a>`
			if c.Clean {
				advice = "可清"
				act = `<a class="btn" href="/inspect?path=` + urlQuery(c.Path) + `">单独查</a> <a class="btn" style="border-color:#f5486a55;color:#ff8fa3" href="/q/preview?path=` + urlQuery(c.Path) + `">清这个</a>`
			}
			name := c.Name
			low := strings.ToLower(name)
			if strings.Contains(low, "cache") || strings.Contains(low, "temp") || strings.Contains(low, "log") || strings.Contains(low, "update") {
				name += " ← 名字像缓存，通常可清"
			}
			b.WriteString(fmt.Sprintf(`<tr><td><code>%s</code></td><td class="r">%s</td><td>%s</td><td class="muted">%s</td><td>%s</td></tr>`,
				htmlEscape(name), human(c.Bytes), htmlEscape(catLabel), htmlEscape(advice), act))
		}
		b.WriteString(`</table></div>`)
	}

	b.WriteString(`<div class="card"><h2>3 · 不放心？这样自己确认</h2><ol style="margin:6px 0 0 18px;padding:0">`)
	for _, h := range ident.How {
		b.WriteString(`<li>` + htmlEscape(h) + `</li>`)
	}
	b.WriteString(`<li>点「打开文件夹」，按大小排序看最大的几个文件，对照上表的「文件类型」判断。</li>`)
	b.WriteString(`<li>拿不准就先「演练并隔离」：它会先告诉你会移动多少、被占用几个，再让你确认。</li>`)
	b.WriteString(`</ol>`)
	if meta.Hint != "" {
		b.WriteString(`<div class="muted" style="margin-top:8px">` + htmlEscape(meta.Hint) + `</div>`)
	}
	b.WriteString(`</div>`)

	b.WriteString(`<div class="card"><h2>4 · 下一步</h2><div style="display:flex;gap:10px;flex-wrap:wrap">`)
	b.WriteString(`<a class="btn" href="/reveal?path=` + urlQuery(path) + `">打开文件夹</a>`)
	if r.Refuse == "" {
		b.WriteString(`<a class="btn" style="background:#1f6f4a;border-color:#33c179" href="/q/preview?path=` + urlQuery(path) + `">演练并隔离</a>`)
	}
	cleanCount := 0
	for _, c := range guide.Children {
		if c.Clean && cleanCount < 3 {
			cleanCount++
			b.WriteString(`<a class="btn" href="/q/preview?path=` + urlQuery(c.Path) + `">只清 ` + htmlEscape(c.Name) + `</a>`)
		}
	}
	b.WriteString(`<a class="btn" href="/inspect">查别的路径</a>`)
	b.WriteString(`<a class="btn" href="/detail/cats">看同类目录</a>`)
	b.WriteString(`</div>`)
	if st.GetScan() == nil {
		b.WriteString(`<div class="muted" style="margin-top:8px">提示：先扫描一次，子目录大小会更准。</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func jsQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "\r", "", "<", `\x3c`, ">", `\x3e`, "&", `\x26`)
	return "'" + r.Replace(s) + "'"
}
