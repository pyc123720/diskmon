# UI 路由与链接审计

- 仓库：`diskmon-go`（Go，仅 Windows，零第三方依赖）
- 审计对象：所有会写进 HTML 的 URL（`href=`、`action=`、`fetch(...)`、`location=`、`sendBeacon(...)`、Go 侧拼出的路径）与服务端真实处理分支
- 审计方式：只读静态审计。未启动服务、未运行 `diskmon-go.exe`、未触碰 `dist\data` 与安装目录
- 结论先行：
  1. 今晚的 `/apps` 已经修掉了（`ui.go:539-540` 只剩一行说明注释），**全仓已无 `/apps` 链接**；
  2. 未发现第二条"页面里有、服务端没有"的链接：去重后 61 个路径（含 query 变体共 71 行）逐条都能落到具体分支；
  3. 但发现一个同级别的问题：`/rules` 的「编辑规则」表单漏发 `editing` 参数，导致**编辑已有规则后点保存必定失败**（见第四节 P1）。

## 审计方法的两个要害

1. **mux 只有一个注册点**：`newHTTPHandler` 里只有 `mux.HandleFunc("/", ...)`（`serve.go:676`），它是全 catch-all。其余路由全部靠 handler 内部 `r.URL.Path` 的 if/switch 分支，子路由拆在 `quarantineRoutes`（`serve.go:333`）、`rulesRoutes`（`serve.go:448`）、`appRoutes`（`serve.go:564`）、`planRoutes`（`serve.go:1108`）、`dupLeftRoutes`（`routes_dup_left.go:9`）。
   **所以"mux 注册了"不能证明任何具体路由存在**——任何没有命中分支的路径都会掉进 `default` 分支返回 404（`serve.go:1043-1050`）。本报告的每一条都去分支里找过。
2. 页面侧链接还有三种不写在 `href="/..."` 字面量里的来源，容易漏：
   - 导航表 `navItems`（`ui.go:63-71`）与 `navBar` 拼装（`ui.go:85`）；
   - 今日建议 `todayItem.Href`（字段定义 `today.go:20`，赋值在 `today.go:133/154/158/168/179/188/198/208/218`，输出在 `ui.go:263`）；
   - 详情页侧栏 `sidebarHTML`（`detail_pages.go:80`）。
   三种都已按"取值集合"逐个核对。

---

## 一、确认存在

表里"页面出处"给的是**产出该 URL 的代码行**，"服务端"给的是**真正处理它的分支**。同一路由多处出现时列全部出处（分号分隔）。

### 1. 全局导航 / 总览

| 路由 | 页面出处 | 服务端处理 | 核对 |
| --- | --- | --- | --- |
| `/` | `ui.go:64`（navItems）、`serve.go:363`、`serve.go:391`、`serve.go:618`、`serve.go:1045`、`serve.go:1047`、`apps.go:95`、`apps.go:107`、`op.go:162`、`plan.go:146`、`plan.go:153`、`plan.go:169`、`today.go` 无、`ui.go:492`、`settings.go:83`、`q_page.go:31`、`scan_progress.go:266` | `serve.go:1041`（`case "/"`） | ✓ |
| `/clean` | `ui.go:65`、`ui.go:390`、`ui.go:401`（`?cat=`）、`ui.go:486`、`ui.go:540`（`/clean#appgroups`）、`ui.go:299` 无、`detail_pages.go:114`、`serve.go:418`、`serve.go:669`、`serve.go:1119`、`serve.go:1133`、`lazy.go:564` | `serve.go:933` | ✓ 见下 |
| `/clean?cat=<分类键>` | `ui.go:401`（`urlQuery(e.k)`）、`ui.go:935` 传参 | `serve.go:935` `Get("cat")` → `cleanPage` `ui.go:408-414` 校验 `cats` 键 | ✓ 参数名一致；非法键被当"全部"处理（`ui.go:410-412`） |
| `/clean#appgroups` | `ui.go:540` | 同上 + 锚点 `id="appgroups"` 在 `ui.go:430` | ✓ 落地页确有该 id |
| `/trend` | `ui.go:66`、`ui.go:234`、`detail_pages.go:116`、`today.go:158`、`today.go:179`、`today.go:218` | `serve.go:1039` | ✓ |
| `/detail` | `ui.go:67`、`detail_pages.go:82`、`detail_pages.go:111`、`serve.go:834`、`ui.go:632`、`ui.go:492` 无 | `serve.go:839` | ✓ |
| `/detail#forecast` `/detail#daily` `/detail#diff` | `ui.go:632` | `serve.go:839` → `renderPage`；锚点 `id="forecast"` `render.go:172`、`id="daily"` `render.go:187`、`id="diff"` `render.go:296` | ✓ 三个锚点都在 `/detail` 的 HTML 里 |
| `/detail/<分区键>` | `detail_pages.go:80`（`sectionDefs` 6 个键：`capacity/trend/cats/files/clean/system`） | `serve.go:828` 前缀判断 + `serve.go:830` `sectionPage`；键表 `detail_pages.go:14-21` | ✓ 6 个键全部有 `IDs` 且对应 id 都在 `renderPage` 里（例：`system` → `engine/alerts/tools`，`render.go:500/503/515`） |
| `/detail/cats` | `inspect.go:192` | 同上（`cats` 是 `sectionDefs` 的键，`detail_pages.go:17`） | ✓ 特别注意：`legacySection`（`detail_pages.go:24-30`）还会把旧键（含 `apps`）映射到新组，所以 `/detail/apps` 也**不是** 404 |
| `/scan` | `ui.go:226`、`ui.go:632`、`ui.go:667`、`ui.go:676`、`render.go:128`、`today.go:133`、`today.go:168`、`lazy.go:522` | `serve.go:729` | ✓ |
| `/scan?full=1` | `ui.go:226` | `serve.go:730` `Get("full")` | ✓ |
| `/inspect`（GET 表单） | `ui.go:69`、`ui.go:270`（`<form action="/inspect">`，字段 `name="path"` `ui.go:271`）、`inspect.go:17`（同款表单）、`inspect.go:20`、`inspect.go:21`、`inspect.go:151`、`inspect.go:154`、`inspect.go:191` | `serve.go:887` + `serve.go:888` `Get("path")` | ✓ 表单字段名与读取一致 |
| `/settings` | `ui.go:70`、`serve.go:889`、`serve.go:901`、`serve.go:916`、`serve.go:909`（带 `notice` 重定向） | `serve.go:889` | ✓ |
| `/rules` | `ui.go:68`、`rules_ui.go:58`（`?edit=`）、`rules_ui.go:115`、`rules_ui.go:232`、`rules_ui.go:244`、`render.go:408`、`serve.go:464`、`serve.go:470`、`serve.go:480`、`serve.go:488`、`serve.go:500`、`serve.go:526`、`serve.go:549` | `serve.go:450` | ✓ |
| `/rules?edit=<名>` | `rules_ui.go:58` | `serve.go:452` `Get("edit")` → `findRule`（`rules_ui.go:71`） | ✓ 参数名一致 |
| `/export` | `ui.go:88`、`settings.go:80`、`serve.go:876`（空 `what` 时出页面） | `serve.go:876` | ✓ |
| `/export?what=<键>` | `export.go:32`（`exportDefs` 7 个键：`dirs/files/apps/dups/media/alerts/quarantine`）、`detail_pages.go:118`（`what=files`） | `serve.go:881` → `handleExport` `export.go:55-131` | ✓ 7 个键在 `handleExport` 的 switch 里都有分支 |
| `/lang?to=zh` `/lang?to=en` | `ui.go:90`、`ui.go:92` | `serve.go:860-867` `Get("to")` | ✓ |
| `/theme?mode=dark` `/theme?mode=light` | `ui.go:95`、`ui.go:97` | `serve.go:868-875` `Get("mode")` | ✓ |
| `/mute?mode=off` `/mute?mode=1h` | `mute.go:91` | `serve.go:841-851` `Get("mode")` | ✓ |
| `/favicon.ico` | `ui.go:156`、`render.go:111` | `serve.go:817` | ✓ 有分支（返回码另见第三节） |
| `/tool?name=<键>` | `viz.go:211`；键表 `viz.go:195-205`（`storagesense/cleanmgr/uninstall/diskmgmt/recyclebin`） | `serve.go:704-712` → `launchTool` `viz.go:218-231` | ✓ 5 个键与 switch 完全对齐 |
| `/api/status` | `render.go:128`（`target=_blank` 链接） | `serve.go:771` | ✓ |
| `/api/geom`（POST beacon） | `ui.go:119` `navigator.sendBeacon('/api/geom', ...)` | `serve.go:791`（GET/HEAD 返回 204，POST 落盘） | ✓ |
| `/api/actions`（fetch） | `ui.go:776`、`lazy.go:508` | `serve.go:749` | ✓ |
| `/api/scan/progress`（fetch） | `scan_progress.go:277` | `serve.go:743` | ✓ |
| `/api/health` | 无页面引用（外部探活） | `serve.go:699` | ✓ 存在，但页面不用它 |
| `/scan/cancel`（fetch） | `scan_progress.go:267` | `serve.go:738` | ✓ |

### 2. 清理中心 → 应用 / 方案 / 媒体 / 懒人

| 路由 | 页面出处 | 服务端处理 | 核对 |
| --- | --- | --- | --- |
| `/app?name=<应用名>` | `ui.go:432`、`render.go:423`、`serve.go:592` | `serve.go:566` `Get("name")` | ✓ 两处都用 `url.QueryEscape`（`ui.go:433`、`render.go:424`） |
| `/app/preview?name=` | `apps.go:106` | `serve.go:571` `Get("name")` | ✓ 用 `url.QueryEscape`（`apps.go:106`） |
| `/app/apply?n=<票据>` | `serve.go:590` | `serve.go:596` `Get("n")` → `takeTicketKind` | ✓ |
| `/plan/preview?key=<方案键>` | `plan.go:209`、`today.go:154`、`today.go:198`；键表 `plan.go:22-46`（`conservative/standard/aggressive`） | `serve.go:1110` `Get("key")` → `buildPlan` `plan.go:57` | ✓ `today.go` 用的 `conservative` 确在键表里（`plan.go:31`） |
| `/plan/apply?n=<票据>` | `plan.go:168` | `serve.go:1115` `Get("n")` | ✓ |
| `/media` | `ui.go:421`、`media.go:360`、`media.go:419`、`media.go:464`、`media.go:485`、`serve.go:959`、`serve.go:979`、`serve.go:995` | `serve.go:936` | ✓ |
| `/media?refresh=1` | `media.go:375` | `serve.go:937` `Get("refresh")` | ✓ |
| `/media/kind?kind=<类型键>` | `media.go:372` | `serve.go:944` `Get("kind")` | ✓ 参数名一致（值来自 `mediaBuckets`） |
| `/media/dir?dir=<目录>` | `media.go:402`、`media.go:439` | `serve.go:946` `Get("dir")`；`serve.go:948-956` 校验必须在本轮分类结果里 | ✓ 两处都用 `urlQuery` 编码（`media.go:402`、`media.go:440`） |
| `/media/apply?n=<票据>` | `media.go:461` | `serve.go:963` `Get("n")` | ✓ |
| `/media/batch?id=<批次>` | 无直接链接，由 `serve.go:982` 重定向进入 | `serve.go:984` `Get("id")` | ✓ |
| `/media/undo?id=<批次>` | `media.go:483` | `serve.go:986` `Get("id")` | ✓ |
| `/lazy` | `ui.go:299`、`ui.go:303`、`ui.go:313`、`ui.go:318`、`ui.go:422`、`ui.go:535`、`lazy.go:601`、`lazy.go:610`、`lazy.go:613`、`lazy.go:620`、`serve.go:1012`、`serve.go:1016`、`serve.go:1020`、`serve.go:1024` | `serve.go:1003` | ✓ |
| `/lazy/start` | `lazy.go:563` | `serve.go:1005` | ✓ |
| `/lazy/pause` | `lazy.go:444` | `serve.go:1010` | ✓ |
| `/lazy/resume` | `lazy.go:442` | `serve.go:1014` | ✓ |
| `/lazy/stop` | `lazy.go:446` | `serve.go:1018` | ✓ |
| `/lazy/undo` | `lazy.go:590` | `serve.go:1022` | ✓ |

### 3. 隔离区 / 操作进度 / 重复文件 / 卸载残留

| 路由 | 页面出处 | 服务端处理 | 核对 |
| --- | --- | --- | --- |
| `/q` | `q_page.go:31`、`q_page.go:48`、`serve.go:376`、`serve.go:425`、`serve.go:1029`、`op.go:162`、`today.go:188`、`lazy.go:1029` 无 | `serve.go:368` | ✓ |
| `/q/preview?path=<路径>` | `render.go:333`、`inspect.go:154`、`inspect.go:182`、`inspect.go:188`、`apps.go:102`、`pages_dup_left.go:118` | `serve.go:335` `Get("path")` → `rehearse` | ✓ 五处都用 `urlQuery`/`QueryEscape` 编码 |
| `/q/apply?n=<票据>` | `serve.go:362` | `serve.go:387` `Get("n")` | ✓ |
| `/q/restore?id=<项>` | `render.go:367`、`q_page.go:42` | `serve.go:421` `Get("id")` | ✓ |
| `/q/restore-batch`（GET 表单） | `q_page.go:35`；勾选框 `name="ids"` `q_page.go:42` | `serve.go:372-373`：`Get("all")` + `r.URL.Query()["ids"]` | ✓ 字段名一致（多值同名 checkbox → `ids=a&ids=b`） |
| `/q/restore-batch?all=1` | `q_page.go:48` | `serve.go:373` `Get("all") == "1"` | ✓ |
| `/q/rebuild` | `ui.go:454`、`q_page.go:49` | `serve.go:414` | ✓（响应头问题见第三节） |
| `/q/purge`（POST 表单） | `render.go:372`（`<form method="post" action="/q/purge">`） | `serve.go:436` + `methodGuard` `serve.go:662` | ✓ 存在；GET 访问得到 405 + 提示页，不是 404 |
| `/op` | `serve.go:381`、`serve.go:396`、`serve.go:430`、`serve.go:999`、`serve.go:1034`、`serve.go:1138`、`routes_dup_left.go:57` | `serve.go:402` | ✓ |
| `/api/op` | **无页面引用**（全仓仅 `serve.go:406` 一个分支） | `serve.go:406` | ✓ 存在，但属孤儿接口（见第四节 P2） |
| `/dup` | `pages_dup_left.go:37`、`pages_dup_left.go:154`、`routes_dup_left.go:29`、`routes_dup_left.go:52`、`today.go:208` | `routes_dup_left.go:11` | ✓ |
| `/dup/scan` | `pages_dup_left.go:40` | `routes_dup_left.go:15`（`min` 可选，`routes_dup_left.go:17`） | ✓ |
| `/dup/apply?n=<票据>` | `pages_dup_left.go:73` | `routes_dup_left.go:25` `Get("n")` | ✓ |
| `/leftovers` | `pages_dup_left.go:90`、`pages_dup_left.go:179` | `routes_dup_left.go:63` | ✓ |
| `/leftovers?refresh=1` | `pages_dup_left.go:92` | `routes_dup_left.go:64` `Get("refresh")` | ✓ |
| `/reveal?path=<路径>` | `inspect.go:180` | `serve.go:852` `Get("path")` | ✓ 见第三节（仅 `X:\` 形态会真的打开资源管理器） |
| `/inspect/result?path=<路径>`（fetch） | `inspect.go:41` | `serve.go:883` `Get("path")` | ✓ |

### 4. 规则 / 设置

| 路由 | 页面出处 | 服务端处理 | 核对 |
| --- | --- | --- | --- |
| `/rules/run?name=&mode=dry` `/rules/run?name=&mode=real` | `rules_ui.go:54`、`rules_ui.go:56`；确认页 `go=1` 表单 `serve.go:498` | `serve.go:472-474` `Get("name")`/`Get("mode")`/`Get("go")`（`serve.go:493`） | ✓ 三个参数名一致 |
| `/rules/delete?name=&go=1` | `rules_ui.go:59`、`serve.go:525` | `serve.go:519-521` `Get("name")`/`Get("go")` | ✓ |
| `/rules/toggle?name=&to=off|dry|on&go=1` | `rules_ui.go:61`、`rules_ui.go:63`、`rules_ui.go:66`、`serve.go:547` | `serve.go:538-541` `Get("name")`/`Get("to")`/`Get("go")` | ✓ |
| `/rules/save`（POST） | `rules_ui.go:95` | `serve.go:454`；非 POST → 405（`serve.go:455-458`） | ✓ 存在（表单字段问题见第四节 P1） |
| `/settings/save`（POST 表单） | `settings.go:22`；字段 `root/interval_sec/alert_min_delta_mb/auto_scan_hours/notify_min_delta_mb/lazy_auto_hour/lazy_auto/warn_free_gb/.../quarantine_days/quarantine_max_gb/whitelist/use_toast` | `serve.go:891` → `saveSettingsFromForm` `settings.go:103-192` | ✓ 13 个字段名逐一对上读取处（`settings.go:124-176`）；表单里没有端口/数据目录，保存时保留原值（`settings.go:178-181`），与页面文案一致 |
| `/settings/reset` | `settings.go:82` | `serve.go:911`（`go=1` 二次确认 `serve.go:915`） | ✓ |
| `/settings/reset?go=1` | `serve.go:915` | `serve.go:912` `Get("go")` | ✓ |
| `/settings?notice=<文本>` | `serve.go:909`、`serve.go:931`（重定向） | `serve.go:890` `Get("notice")` | ✓ |

---

## 二、确认不存在（页面里有、服务端没有）

**当前代码里 0 条会 404 的页面链接。** 这一节把"唯一一条历史事故"和"三个最像路由、其实不是路由的字符串"逐条摆出来，证明它们都被真的去 handler 里找过，而不是靠猜。

### 2.1 历史事故：`/apps`（已修复，现无链接）

| 项 | 证据 |
| --- | --- |
| 曾经的出处 | `ui.go:539` 的注释明确记录：这里原来指向 `/apps`，该路由不存在，点了就是 404 |
| 现在的写法 | `ui.go:540` → `href="/clean#appgroups"`；落点有 `id="appgroups"`（`ui.go:430`），服务端 `/clean` 分支在 `serve.go:933` → **不再 404** |
| 全仓复查 | `grep -n 'href="/apps\|href=\x27/apps\|/apps?'`：**无命中**（含 `data/report.html` 与 `dist/data/report.html` 两份静态报告快照，均无 `/apps` 链接） |
| 服务端复查 | `serve.go:838-1050` 的主 switch、`quarantineRoutes`（`serve.go:334`）、`rulesRoutes`（`serve.go:449`）、`appRoutes`（`serve.go:565`）、`planRoutes`（`serve.go:1109`）、`dupLeftRoutes`（`routes_dup_left.go:10`）**均无 `/apps` 分支**；真访问会落到 `serve.go:1043` 的 default → 404「页面不存在」 |
| 说明 | 现在 `/apps` 如果被手敲出来，仍然是 404；但已经没有任何页面链接指向它了 |

### 2.2 三个"看起来像路由、其实不是"的高危字符串（逐个排除）

| 字符串 | 位置 | 为什么不是路由 | 真正的入口 |
| --- | --- | --- | --- |
| `"apps"` | `export.go:21` | 这是 CSV 导出键，不是路径；它被拼成 `/export?what=apps`（`export.go:32`），由 `handleExport` 的 `case "apps"`（`export.go:79`）处理 | `/export?what=apps` ✓ |
| `id="apps"` | `render.go:411` | 页内锚点，只有 `#apps` 才用到；上方的标签栏 `render.go:120` 也是 `{"应用", "#apps"}` 这种纯 fragment，不带路径 | 在 `/detail` 页内滚动 ✓ |
| `"apps"` | `detail_pages.go:28` | `legacySection` 的旧键映射：`"apps": "clean"`，所以 `/detail/apps` 会被改写成 `/detail/clean`（`detail_pages.go:88-90`） | `/detail/apps` 与 `/detail/clean` 都 ✓ |

### 2.3 为什么可以下"0 条"这个结论

页面侧 URL 全集是从这五类来源机械枚举出来的，没有"凭印象"：

1. `href="..."` 双引号字面量 —— 159 行命中（含 `clean_page_test.go` 里 3 行断言，实际链接约 155 处）；
2. `href='...'` 单引号字面量 —— 3 处（`apps.go:102`、`pages_dup_left.go:73`、`pages_dup_left.go:118`）；
3. `action="..."` 表单 —— 7 处（`ui.go:270`、`inspect.go:17`、`q_page.go:35`、`rules_ui.go:95`、`settings.go:22`、`render.go:372`、`serve.go:498`）；
4. JS 侧 `fetch(...)` / `location=` / `sendBeacon(...)` —— `inspect.go:41`、`scan_progress.go:267`、`scan_progress.go:277`、`scan_progress.go:278`、`lazy.go:508`、`ui.go:776`、`ui.go:119`；
5. 变量拼装的三张表 —— `navItems`（`ui.go:63`）、`todayItem.Href`（`today.go`）、`sectionDefs`（`detail_pages.go:14`）+ `sidebarHTML`（`detail_pages.go:80`）。

去重后共 61 个路径（含 query 变体 71 行），全部在第一节点到了具体分支。**没有一条落到 `serve.go:1043` 的 default 404。**

---

## 三、无法判定

这些不是"路由不存在"，而是**静态看代码不足以断言用户实际会看到什么**，交付时需要运行时验证。

| # | 对象 | 现状 | 为什么无法静态判定 |
| --- | --- | --- | --- |
| 1 | `/favicon.ico` | 分支在 `serve.go:817-824`，读 `<exeDir>\icon.ico` 并写回 | 文件不存在时该分支**主动返回 404**（`serve.go:823`）。仓库里有没有随包发布的 `icon.ico`，取决于打包步骤，不是代码能回答的。这会让"浏览器控制台里有个 404"成为常态噪音 |
| 2 | `/q/rebuild` | 分支在 `serve.go:414-420`，成功页直接 `w.Write` | **没有设置 `Content-Type`**（对比 `serve.go:369`、`serve.go:403` 都设了 `text/html; charset=utf-8`）。Go 会嗅探成 `text/plain`，浏览器可能把这段 HTML 当纯文本显示。是不是真的显示成源码，必须跑起来看 |
| 3 | `/media/dir?dir=` | 分支在 `serve.go:946-962` | 参数值来自"本轮媒体分类缓存"。如果用户先拿到链接、期间分类被刷新且该目录落榜，会走 `serve.go:957-961` 的"该目录不在媒体分类结果里"提示页（HTTP 200）。属于语义失效，不是 404，但静态无法判断概率 |
| 4 | `/reveal?path=` | 分支在 `serve.go:852-858` | 只有 `len(p) > 3 && p[1] == ':'`（形如 `C:\...`）才真的打开资源管理器，否则静默跳过并 302 到 `/inspect`。传相对路径或 UNC 路径时"点了没反应"，代码看着有分支、实际不生效 |
| 5 | `/tool?name=` | 分支在 `serve.go:704-712` | 路由和键都对（5/5），但 `launchTool` 依赖系统里真有 `cleanmgr.exe`、`control.exe`、`diskmgmt.msc` 等（`viz.go:219-230`）。缺失时返回 **400 + 错误文本**，不是 404。无法静态判定目标机的系统组件完整性 |
| 6 | `/api/geom` | 分支在 `serve.go:791-816` | 只有 POST 才落盘，GET/HEAD 一律 204（`serve.go:792-795`）。`sendBeacon` 在部分浏览器/隐私模式下会降级为 GET，此时"保存窗口位置"静默失效——需要真机验证 |
| 7 | 各页面在"无数据"状态下的呈现 | 例如 `/dup`（`pages_dup_left.go:42-44`）、`/leftovers`（`pages_dup_left.go:89-100`）、`/trend`、`/media`（`media.go:418-420`） | 这些路由都有分支、都返回 200，但页面内容是"还没有结果"提示。是否算"可用"依赖业务判断，不属于路由存在性 |

---

## 四、附带发现（不属于路由存在性，但同属"页面与服务端的契约"）

### P1（高）`/rules` 编辑表单漏发 `editing`，编辑规则必定保存失败

- 服务端读取：`serve.go:460` `editing := r.PostForm.Get("editing")`，随后传给 `saveRuleFromValues(r.PostForm, editing)`（`serve.go:461`）。
- 页面从未发送：表单体在 `rules_ui.go:95-116`，字段只有 `name`、`path`、`max_size_mb`、`keep_days`、`interval_min`、`window_start`、`window_end`、`enabled`、`dry_run`——**没有 `editing`，也没有任何 hidden input**（`grep -n 'editing\|hidden' rules_ui.go` 只在 71/74/80/86/110/111/121/175/178 命中，全是读或校验，没有输出到 HTML）。
- 失败路径：`/rules?edit=foo` → 表单回填 `foo` → 提交 → `editing == ""` → `rules_ui.go:178` `if editing == "" && idx >= 0` 命中 → 返回 `fmt.Errorf("已存在同名规则：%s", name)` → `serve.go:461-467` 渲染「保存规则失败」页。
- 结论：**编辑规则这条路现在走不通**（新建规则不受影响，因为新建时本来就不该有同名）。
- 最小修法：在 `rules_ui.go:95` 的 `<form>` 里补一行 `<input type="hidden" name="editing" value="...">`，编辑态填 `editing.Name`，新建态填空串。顺带可让 `rules_ui.go:113` 那句"通过表单保存时永远是试运行"与实际行为对齐（那段文案本身是对的）。

### P2（低）`/api/op` 是孤儿接口

- 只有服务端分支（`serve.go:406-413`），全仓无任何页面 `fetch` 或链接指向它。
- 进度页 `opPage` 走的是 `/api/actions`（`ui.go:776`、`serve.go:749`），`/api/op` 的字段（`running/label/done/total/bytes/files/skipped/error`）与 `/api/actions` 的 `op` 子对象（`serve.go:754-757`）高度重复。
- 建议：要么让页面用起来，要么删掉，避免下一个人误以为它是活跃契约。

### P3（低）两个成功页缺 `Content-Type`

- `/q/rebuild`：`serve.go:414-420`，`w.Write` 前没设头（同函数内 `/q`、`/q/preview` 等都有设）。
- 其余带 HTML 输出的分支基本都显式设了 `text/html; charset=utf-8`，这两个是漏网。
- 影响：中文可能乱码或当纯文本显示。属渲染层，不是路由层。

### P4（提示）`pageShell` 页面没有导航栏

- `/q/*`、`/rules/run`、`/plan/*`、`/app/*`、`/dup/apply` 等用 `pageShell`（`serve.go:313-326`），只套 `appShell` 的标题与主题，导航由 `appShell` 提供；`pageShell` 内部确实调用 `appShell`（`serve.go:325`），所以导航是在的——**这条只是提醒**：`pageShell` 的 `switch title` 决定高亮项（`serve.go:315-324`），新加页面若标题没登记，导航会全部不高亮，看起来像"当前不在任何模块"。不是 bug，但容易在新增页面时踩到。

---

## 附：本次审计用到的检索式（可复现）

- 服务端分支：`HandleFunc|newHTTPHandler|r\.URL\.Path|switch .*Path`
- 页面链接：`href=\\?"/[^"]*|action=\\?"/[^"]*|fetch\(|window\.location`
- 单引号属性：`href='|action='|URL=|location=|location\.href|win\.open|\.assign\(`
- 路由字面量：`/apps|apps\b`、`api/op|/op\?|href="/op`
- 锚点与 id：`id=\\?"[a-zA-Z]+`（确认 `forecast/daily/diff/apps/appgroups/catnav` 都存在）
- 表单字段：`editing|hidden`（确认 `rules` 表单确实漏发）
