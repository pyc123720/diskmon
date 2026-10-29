package main

import (
	"strings"
	"testing"
)

const sampleWinapp2 = `
; 注释行应被忽略
// 这种注释也忽略

[Sample Cleaner]
LangSecRef=3021
Detect=HKCU\Software\Sample
Default=True
Warning=会清掉示例应用的缓存
FileKey1=%LocalAppData%\Sample\Cache|*.tmp|RECURSE
FileKey2=%LocalAppData%\Sample\Logs|*.log
ExcludeKey1=FILE|%LocalAppData%\Sample\Cache\|keep.tmp

[Registry Only]
LangSecRef=3021
RegKey1=HKCU\Software\RegistryOnly
RegKey2=HKCU\Software\RegistryOnly\Sub

[Dangerous]
FileKey1=%WinDir%\|*.*
FileKey2=C:\Windows
FileKey3=..\..\etc\|*.*

[Unknown Var]
FileKey1=%SomeThirdPartyVar%\Cache|*.tmp
`

func TestParseWinapp2Basics(t *testing.T) {
	res := ParseWinapp2(sampleWinapp2)
	if len(res.Entries) != 4 {
		t.Fatalf("段落数=%d，期望 4", len(res.Entries))
	}

	e := res.Entries[0]
	if e.Name != "Sample Cleaner" {
		t.Fatalf("段落名=%q", e.Name)
	}
	if !e.Default {
		t.Fatal("Default=True 没解析出来")
	}
	if e.Warning == "" {
		t.Fatal("Warning 没解析出来")
	}
	if e.LangSecRef != "3021" {
		t.Fatalf("LangSecRef=%q", e.LangSecRef)
	}
	if len(e.Files) != 2 {
		t.Fatalf("FileKey 数=%d，期望 2", len(e.Files))
	}

	fk := e.Files[0]
	if !fk.Resolved {
		t.Fatal("%LocalAppData% 应该能解析")
	}
	if !strings.HasSuffix(strings.ToLower(fk.Path), `\sample\cache`) {
		t.Fatalf("路径=%q", fk.Path)
	}
	if !fk.Recurse {
		t.Fatal("RECURSE 没解析出来")
	}
	if len(fk.Patterns) != 1 || fk.Patterns[0] != "*.tmp" {
		t.Fatalf("Patterns=%v", fk.Patterns)
	}

	if e.Files[1].Recurse {
		t.Fatal("FileKey2 没有 RECURSE，不该被标成递归")
	}
	if e.Excludes != 1 {
		t.Fatalf("Excludes=%d，期望 1（要能告知用户暂不支持排除项）", e.Excludes)
	}
}

// 只有注册表项的段落要跳过——我们不碰注册表，硬做就是另一类事故。
func TestParseWinapp2SkipsRegistryOnly(t *testing.T) {
	res := ParseWinapp2(sampleWinapp2)
	var reg *Winapp2Entry
	for i := range res.Entries {
		if res.Entries[i].Name == "Registry Only" {
			reg = &res.Entries[i]
		}
	}
	if reg == nil {
		t.Fatal("没找到 Registry Only 段落")
	}
	if !reg.Unsupported {
		t.Fatal("只有 RegKey 的段落应被标为 Unsupported")
	}
	if reg.RegistryKeys != 2 {
		t.Fatalf("RegistryKeys=%d，期望 2", reg.RegistryKeys)
	}
}

// 越界路径必须被识别出来：盘符根、系统根、带 .. 的相对路径。
func TestWinapp2UnsafePaths(t *testing.T) {
	cases := map[string]bool{
		`C:\`:                            true,
		`C:\Windows`:                     true,
		`C:\Program Files`:               true,
		`..\..\etc`:                      true,
		`relative\path`:                  true,
		`C:\Windows\Temp`:                false,
		`C:\Users\alice\AppData\Local\X`: false,
		`D:\Games\Cache`:                 false,
	}
	for p, want := range cases {
		if got := winapp2UnsafePath(p); got != want {
			t.Fatalf("winapp2UnsafePath(%q)=%v，期望 %v", p, got, want)
		}
	}
}

// 认不出的 %变量% 不许瞎猜——标 Resolved=false，让上层拒绝采纳。
func TestParseWinapp2UnknownVarStaysUnresolved(t *testing.T) {
	res := ParseWinapp2(sampleWinapp2)
	var ent *Winapp2Entry
	for i := range res.Entries {
		if res.Entries[i].Name == "Unknown Var" {
			ent = &res.Entries[i]
		}
	}
	if ent == nil {
		t.Fatal("没找到 Unknown Var 段落")
	}
	fk := ent.Files[0]
	if fk.Resolved {
		t.Fatal("未知变量不该被当成解析成功")
	}
	if !strings.Contains(fk.Path, "%SomeThirdPartyVar%") {
		t.Fatalf("原始占位符应保留便于排查，得到 %q", fk.Path)
	}
}

// 越界条目要产生警告，让操作者能看见，而不是悄悄丢掉。
func TestParseWinapp2WarnsOnUnsafe(t *testing.T) {
	res := ParseWinapp2(sampleWinapp2)
	if len(res.Warnings) == 0 {
		t.Fatal("应当为越界路径产生警告")
	}
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, "越界") {
		t.Fatalf("警告里应包含「越界」：%s", joined)
	}
}

// 段落前的杂行、非 key=value 行都要记为警告而不是崩掉。
func TestParseWinapp2MalformedLinesAreWarningsNotPanics(t *testing.T) {
	res := ParseWinapp2("这行在段落之前\n[X]\n没有等号的行\nFileKey1=%LocalAppData%\\X|*.*\n")
	if len(res.Entries) != 1 {
		t.Fatalf("段落数=%d", len(res.Entries))
	}
	if len(res.Entries[0].Files) != 1 {
		t.Fatalf("有效 FileKey 应被保留，得到 %v", res.Entries[0].Files)
	}
	if len(res.Warnings) != 2 {
		t.Fatalf("警告数=%d，期望 2（段落前一行 + 无等号一行）：%v", len(res.Warnings), res.Warnings)
	}
}

// 汇总计数要和实际条目对得上。
func TestWinapp2Summary(t *testing.T) {
	res := ParseWinapp2(sampleWinapp2)
	entries, withFiles, registryOnly, _, _ := Winapp2Summary(res)
	if entries != 4 {
		t.Fatalf("entries=%d，期望 4", entries)
	}
	if registryOnly != 1 {
		t.Fatalf("registryOnly=%d，期望 1", registryOnly)
	}
	if withFiles != 3 {
		t.Fatalf("withFiles=%d，期望 3", withFiles)
	}
}
