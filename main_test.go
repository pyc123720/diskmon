package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain 负责一件事：保证 t.TempDir() 有个真能建目录的落脚点。
//
// t.TempDir() 用的是 os.TempDir()，也就是 TMP/TEMP 环境变量。本机有个反复出现
// 的情况：PowerShell 能往 %TEMP% 里写文件，但测试进程要想在里面 **建目录** 会被
// 拒（Access is denied）——杀软按进程拦未签名 exe 写 Temp 的那类行为。
// 结果就是整套用例因为「临时目录不能用」而红，跟被测代码一点关系都没有。
//
// 所以先探一次：能不能在 %TEMP% 里建目录。建不了就换到 exe 旁边——
// 测试进程都能在那儿被执行，就一定能在那儿写。
//
// 换法只用环境变量，不碰任何产品代码：os.TempDir() 每次调用都重读 TMP/TEMP，
// 在 os.Setenv 之后，后面所有 t.TempDir() 就都落到新位置了。
func TestMain(m *testing.M) {
	if !dirCreatable(os.TempDir()) {
		for _, cand := range tempFallbacks() {
			if !dirCreatable(cand) {
				continue
			}
			_ = os.Setenv("TMP", cand)
			_ = os.Setenv("TEMP", cand)
			break
		}
	}
	os.Exit(m.Run())
}

// tempFallbacks 按「越不像垃圾场越优先」的顺序给出备选落脚点。
func tempFallbacks() []string {
	var out []string
	if exe, err := os.Executable(); err == nil {
		// 正常情况下这是 go 的临时构建目录，测试跑完 go 自己会清。
		out = append(out, filepath.Join(filepath.Dir(exe), ".temptest"))
	}
	if wd, err := os.Getwd(); err == nil {
		out = append(out, filepath.Join(wd, ".temptest"))
	}
	return out
}

// dirCreatable 试建再删，问的是「能不能建目录」，不是「能不能写文件」。
// 这两个在本机上答案不一样，混着判会得出错误结论。
//
// 注意先 MkdirAll：os.MkdirTemp 要求传入的 dir **已经存在**，否则报
// ENOENT 而不是权限错。少了这一步，所有候选都会被判成「不可用」，
// 后备逻辑静默失效——第一版就是这么写的，53 个用例照红不误。
func dirCreatable(dir string) bool {
	if dir == "" {
		return false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	probe, err := os.MkdirTemp(dir, "probe-*")
	if err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}
