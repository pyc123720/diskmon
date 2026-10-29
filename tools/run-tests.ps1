# 在本机跑 diskmon-go 的测试。
#
# 为什么需要这个脚本：
#   本机装了火绒，它会拒绝执行 go 构建临时目录里的 exe。表现是
#     go test ./...
#     fork/exec ...\go-build1234\b001\diskmon.test.exe: Access is denied.
#   而同一份二进制只要编译到普通路径（比如 .gotmp\all.test.exe）就能正常跑。
#   所以这里一律先 `go test -c` 编到普通路径，再直接执行——不依赖任何杀软白名单。
#
#   顺带把 TMP/TEMP 指到工作区内：火绒/权限在某些时段也会拒 %TEMP% 下建目录，
#   而 t.TempDir() 用的是 os.TempDir()。指向工作区后这件事不再受环境影响。
#
# 用法：
#   pwsh -NoProfile -File tools\run-tests.ps1            # 全量
#   pwsh -NoProfile -File tools\run-tests.ps1 TestUsn    # 只跑匹配的用例

param(
    [string]$Run = '',
    [string]$GoExe = 'E:\toolchains\go\bin\go.exe'
)

$ErrorActionPreference = 'Continue'
$repo = Split-Path -Parent $PSScriptRoot
Set-Location $repo

if (Test-Path $GoExe) {
    $env:PATH = (Split-Path $GoExe -Parent) + ';' + $env:PATH
}

$tmp = Join-Path $repo '.gotmp'
New-Item -ItemType Directory -Force $tmp | Out-Null
# 必须分两行赋值：TMP=x TEMP="$TMP" 写一行时 $TMP 还是旧值，TEMP 会变成空串。
$env:TMP = $tmp
$env:TEMP = $tmp
$env:GOTMPDIR = $tmp

Write-Output '--- go build ---'
go build ./...
$buildCode = $LASTEXITCODE
Write-Output ("build: " + $buildCode)

Write-Output '--- go vet ---'
go vet ./...
$vetCode = $LASTEXITCODE
Write-Output ("vet: " + $vetCode)

$bin = Join-Path $tmp 'all.test.exe'
Write-Output '--- 编译测试二进制 ---'
go test -c -o $bin .
if ($LASTEXITCODE -ne 0 -or -not (Test-Path $bin)) {
    Write-Output '编译测试二进制失败'
    exit 1
}

Write-Output '--- 执行 ---'
$args2 = @('-test.v=true')
if ($Run) { $args2 += ('-test.run=' + $Run) }
$out = & $bin @args2 2>&1
$code = $LASTEXITCODE

$pass = ($out | Select-String -Pattern '^--- PASS' | Measure-Object).Count
$fail = ($out | Select-String -Pattern '^--- FAIL' | Measure-Object).Count
$skip = ($out | Select-String -Pattern '^--- SKIP' | Measure-Object).Count

Write-Output ''
Write-Output ("PASS: " + $pass + "  FAIL: " + $fail + "  SKIP: " + $skip + "  exit: " + $code)
if ($fail -gt 0) {
    ($out | Select-String -Pattern '^--- FAIL') | ForEach-Object { '  ' + $_.Line.Trim() }
}

if ($buildCode -ne 0 -or $vetCode -ne 0 -or $code -ne 0) { exit 1 }
exit 0
