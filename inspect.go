package main

import (
	"fmt"
	"time"
	"os"
	"strings"
)

// ---------- 路径体检 ----------

func inspectPage(cfg Config, st *Store, raw string) string {
	var b strings.Builder
	b.WriteString(`<h2 style="margin-top:0">路径体检</h2>`)
	b.WriteString(`<p class="muted">粘贴任意文件或文件夹路径，看看它占多大、属于哪类、能不能清、有没有被占用。不会删除任何东西。</p>`)

	// 输入框（GET 提交，方便收藏）
	b.WriteString(`<form method="get" action="/inspect" style="max-width:760px;margin-bottom:16px">
<input name="path" value="` + htmlEscape(raw) + `" placeholder="C:\Users\...\AppData\Local\Temp 或 %TEMP%" style="width:100%;padding:11px 12px;background:#1c212b;border:1px solid #2a3240;border-radius:10px;color:#e7ebf2;font-size:14px">
<div style="margin-top:10px"><button class="btn" type="submit" style="cursor:pointer;background:#1f6f4a;border-color:#33c179">体检</button>
<a class="btn" href="/inspect?path=%25TEMP%25">看 %TEMP%</a>
<a class="btn" href="/inspect?path=` + urlQuery(os.Getenv("USERPROFILE")+`\\Downloads`) + `">看 Downloads</a></div></form>`)

	if strings.TrimSpace(raw) == "" {
		return appShell("路径体检", "inspect", b.String())
	}

	path := expandPath(raw)
	if path == "" {
		b.WriteString(`<div class="warn">路径为空。</div>`)
		return appShell("路径体检", "inspect", b.String())
	}
	b.WriteString(`<div class="card"><h2><code>` + htmlEscape(path) + `</code></h2>`)

	stInfo, err := os.Stat(path)
	if err != nil {
		b.WriteString(`<div class="warn">路径不存在或不可读：` + htmlEscape(err.Error()) + `</div></div>`)
		b.WriteString(`<a class="btn" href="/inspect">再试一个</a>`)
		return appShell("路径体检", "inspect", b.String())
	}

	r := rehearseQuick(cfg, path, 3*time.Second)
	if r.Refuse != "" {
		b.WriteString(`<div class="warn"><b>不可清理：</b>` + htmlEscape(r.Refuse) + `</div>`)
	} else {
		meta := cats[r.Cat]
		color := tierColor[meta.Tier]
		kind := "文件夹"
		if !stInfo.IsDir() {
			kind = "文件"
		}
		b.WriteString(fmt.Sprintf(`<p>%s · <b>%s</b> · %d 个文件 · 修改于 %s</p>`,
			kind, human(r.Bytes), r.Files, stInfo.ModTime().Format("2006-01-02 15:04")))
		if r.Truncated {
			b.WriteString(`<div class="muted">目录较大，3 秒内统计了已遍历的部分，实际总量可能更大；隔离时会重新完整统计。</div>`)
		}
		b.WriteString(fmt.Sprintf(`<p>类别：<b>%s</b>　处置：<span style="color:%s">%s</span></p>`,
			htmlEscape(meta.Label), color, tierText[meta.Tier]))
		if meta.Hint != "" {
			b.WriteString(`<div class="muted">` + htmlEscape(meta.Hint) + `</div>`)
		}
		if len(r.Locked) > 0 {
			b.WriteString(fmt.Sprintf(`<div class="warn">%d 个文件正被占用，会被跳过。</div>`, len(r.Locked)))
		}
		switch meta.Tier {
		case "clean":
			b.WriteString(`<div class="ok" style="margin-top:10px">属于「可清」类：临时/开发缓存/更新缓存/日志，删了会自动重建。</div>`)
		case "confirm":
			b.WriteString(`<div class="warn" style="margin-top:10px">属于「需确认」类：可能含账号数据或要用的内容，建议先看里面有什么。</div>`)
		default:
			b.WriteString(`<div class="warn" style="margin-top:10px">不建议清理：这是用户文档/系统组件/程序安装目录。</div>`)
		}
		b.WriteString(`<div style="margin-top:12px"><a class="btn" href="/q/preview?path=` + urlQuery(path) + `">演练并隔离</a>`)
		b.WriteString(`<a class="btn" href="/detail/cats">看同类目录</a></div>`)
	}
	b.WriteString(`</div>`)
	b.WriteString(`<a class="btn" href="/inspect">再体检一个</a>`)
	return appShell("路径体检", "inspect", b.String())
}
