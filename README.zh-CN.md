# diskmon（磁盘监测）

一个常驻托盘的 Windows 小工具，回答三个问题：

- **C 盘的空间去哪了？** —— 目录占用、20 类分类、Treemap
- **刚刚是什么在涨？** —— NTFS USN 日志，秒级定位到文件、目录、进程
- **哪些能安全删？** —— 所有清理先进隔离区，7 天内可撤销

监测默认**只读**。任何删除都要你点确认；除了明确标注的 L2 自动规则，其它动作都可回滚，
而 L2 规则需要三道确认。

> **数据安全提示**：本工具会把文件移入隔离区，L2 规则可永久删除文件。
> 启用规则前请先读 [SECURITY.md](SECURITY.md)。

[English](README.md)

## 功能

| 模块 | 内容 |
|---|---|
| 监测 | 剩余空间、三级阈值、每日自动扫描、写满预测（带区间） |
| 归因 | USN 秒级、按进程写入量（近似）、跳变 vs 泄漏判定 |
| 分析 | 20 类分类、Treemap、快照对比（上次/一周前）、按应用聚合 |
| 媒体拆分 | 图片 / 视频 / 文档 / 音频 / 压缩包 / 程序，按目录统计 |
| 重复文件 | 两阶段哈希、排除硬链接、不碰安装目录 |
| 卸载残留 | 注册表孤立 + 数据残留，分置信度 |
| 清理 | 懒人清理（可设每天/每周自动）、三档方案、按应用、按目录、按媒体类型、L2 规则 |
| 体检 | 粘贴任意路径，查看大小/类别/能否清理/是否被占用（3 秒预算） |
| 安全 | 隔离区（7 天、20 GB 上限）、白名单/黑名单、先试运行、可撤销 |
| 导出 | 目录/大文件/应用/重复/媒体/告警/隔离区，CSV（Excel 友好） |
| 工程 | 单文件 8 MB、托盘、`/api/health`、看门狗、签名构建 |

## 快速开始

### 方式 A：安装版（推荐）

1. 从 Releases 下载 `diskmon-setup.exe`
2. SmartScreen 可能提示（自签名构建）：**更多信息 → 仍要运行**
3. 通过 UAC，按提示选择是否加 Defender 排除 / 是否信任自签名证书
4. 托盘出现图标，桌面生成「磁盘监测」快捷方式

### 方式 B：绿色版

把 `diskmon-go.exe` 放到任意目录双击即可（托盘运行）。
用 `diskmon-go.exe open` 或托盘菜单打开看板。

### 首次使用（三步）

1. 打开看板 → 点 **扫描**（首次约 1–2 分钟）
2. 看「今日建议」
3. 点 **懒人清理**（只清临时/开发/更新缓存，全部可撤销）

## 从源码构建

要求：Windows 10/11、Go 1.27+、无第三方依赖。

```powershell
./build.ps1                 # vet + test + 构建 -> dist/diskmon-go.exe
./build.ps1 -Installer      # 同时构建安装器（需要 payload，脚本会自动准备）
```

安装器内嵌 app exe，因此需要先生成 `installer/payload/`；`build.ps1 -Installer` 会自动处理
（自签名证书可选）。

CI：`.github/workflows/build.yml` 在 `windows-latest` 上跑 `go vet`、`go test` 并构建 exe。

## 命令行

```
diskmon-go.exe tray          # 托盘模式
diskmon-go.exe open          # 打开独立窗口
diskmon-go.exe scan          # 扫描并打印摘要
diskmon-go.exe report        # 生成 HTML 报告
diskmon-go.exe serve         # 只启动看板服务
diskmon-go.exe status        # 一行状态
diskmon-go.exe health        # 版本/运行时长/goroutine/计数器
diskmon-go.exe dup C:\ 4     # 重复文件扫描（最小 MB）
diskmon-go.exe leftovers     # 卸载残留扫描
diskmon-go.exe plans         # 预览三档清理方案
diskmon-go.exe rules         # 列出 L2 规则
```

## 界面

`http://127.0.0.1:8766/`（端口可配），只监听本机回环地址。

| 页面 | 用途 |
|---|---|
| `/` | 容量、今日建议、懒人清理、入口 |
| `/clean` | 三档方案、按应用、重复、残留、隔离区、记录 |
| `/trend` | 预测、每日增长、性质判定、对比 |
| `/detail` 与 `/detail/<分区>` | 全量数据 + 20 个分区页 |
| `/media` | 图片/视频/文档/音频/压缩包，按目录 |
| `/rules` | 规则增删改、试运行、立即执行 |
| `/settings` | 阈值、白名单、隔离区、通知规则 |

## 清理为什么安全

1. 监测过程不写被扫描的路径
2. 所有清理都移到 `<数据目录>/quarantine`，7 天内可还原，上限 20 GB
3. 只有 `clean` 层（临时/开发缓存/更新缓存/日志）能进自动规则
4. 规则默认试运行，永久删除要三道确认
5. 硬黑名单（`AppData\Local\Packages`、`Microsoft\Windows`、`WindowsApps` 等）永不触碰

## 限制

- **一次只监测一个盘**：默认 `C:\`，可在设置里改「扫描根目录」（USN 会跟随）。
  多盘同时扫描是计划中的功能，尚未实现。
- 仅 Windows；USN、进程 I/O 需要管理员权限
- 进程归因是近似值：只回答"谁写了多少"，不回答"谁写了哪个文件"
- 自签名构建，其它机器上 SmartScreen 仍会提示（除非导入随包证书）

## 隐私

无遥测；除签名时访问时间戳服务器外不联网。数据都在 `%LOCALAPPDATA%\diskmon-go`
（或配置的数据目录）。

## 贡献

欢迎 issue 和 PR。请保持现有安全模型：只读监测、隔离优先、先试运行再删除。

## 许可证

MIT © 2026 pyc123720 —— 见 [LICENSE](LICENSE)。
