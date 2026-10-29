# 反复跑测试，找偶发失败 / 顺序依赖 / 状态残留。
#
# 做法：编译一次测试二进制，换不同的 shuffle 种子反复执行。
# 每轮顺序都不同——哪个用例偷了别人的状态、或依赖自己先跑，就会在某一轮现形。
#
# 注意：Go 的种子写法是 -test.shuffle=<数字>，没有 -test.shuffle.seed 这个 flag。
# 写错了会整个二进制退出码 2、一条用例都不跑（上一版就是这么误报的）。
$ErrorActionPreference = 'Continue'
$repo = 'C:\Users\lenovo\Documents\deepseek-harness\default-workspace\diskmon-go'
Set-Location $repo
$env:PATH = 'E:\toolchains\go\bin;' + $env:PATH
$tmp = Join-Path $repo '.gotmp'
New-Item -ItemType Directory -Force $tmp | Out-Null
$env:TMP = $tmp
$env:TEMP = $tmp
$env:GOTMPDIR = $tmp

$bin = Join-Path $tmp 'stress.test.exe'
Write-Output '=== 编译测试二进制 ==='
go test -c -o $bin .
if ($LASTEXITCODE -ne 0) { Write-Output '编译失败'; exit 1 }

$results = @()
$badDetail = @()

function RunOnce([string]$label, [string[]]$extra) {
    $t0 = Get-Date
    $out = & $bin @extra 2>&1
    $code = $LASTEXITCODE
    $secs = [math]::Round(((Get-Date) - $t0).TotalSeconds, 1)
    $pass = ($out | Select-String -Pattern '^\s*--- PASS' | Measure-Object).Count
    $fail = ($out | Select-String -Pattern '^\s*--- FAIL' | Measure-Object).Count
    $skip = ($out | Select-String -Pattern '^\s*--- SKIP' | Measure-Object).Count
    Write-Output ('  ' + $label.PadRight(20) + ' PASS ' + $pass.ToString().PadLeft(3) + '  FAIL ' + $fail + '  SKIP ' + $skip + '  ' + $secs + 's  exit ' + $code)
    if ($code -ne 0 -or $fail -gt 0) {
        $out | Select-String -Pattern '^\s*--- FAIL|panic:|DATA RACE|flag provided|invalid value' |
            Select-Object -First 5 | ForEach-Object { '      ' + $_.Line.Trim() }
        $script:badDetail += ($out | Out-String)
    }
    $script:results += [pscustomobject]@{
        Label = $label; Pass = $pass; Fail = $fail; Skip = $skip; Secs = $secs; Code = $code
    }
}

Write-Output ''
Write-Output '=== 六轮随机顺序 ==='
foreach ($seed in 1..6) {
    RunOnce "shuffle=$seed" @('-test.v=true', ('-test.shuffle=' + $seed))
}

Write-Output ''
Write-Output '=== 同进程连跑 3 次（抓状态残留：文件、全局变量、缓存没清）==='
RunOnce 'count=3' @('-test.v=true', '-test.count=3')

Write-Output ''
Write-Output '=== 连跑 3 次 + 洗牌 ==='
RunOnce 'count=3 shuffle=7' @('-test.v=true', '-test.count=3', '-test.shuffle=7')

Write-Output ''
Write-Output '=== 汇总 ==='
$totalPass = ($results | Measure-Object -Property Pass -Sum).Sum
$totalFail = ($results | Measure-Object -Property Fail -Sum).Sum
$badRuns = @($results | Where-Object { $_.Fail -gt 0 -or $_.Code -ne 0 })
Write-Output ('  轮次 ' + $results.Count + '，累计 PASS ' + $totalPass + '，累计 FAIL ' + $totalFail + '，异常轮次 ' + $badRuns.Count)
if ($badRuns.Count -eq 0) {
    Write-Output '  没有任何一轮出问题。'
} else {
    Write-Output ('  异常轮次：' + (($badRuns | ForEach-Object { $_.Label }) -join ', '))
}
