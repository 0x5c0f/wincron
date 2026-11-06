# WinCron

[![Release](https://img.shields.io/github/v/release/0x5c0f/wincron?include_prereleases&display_name=tag)](https://github.com/0x5c0f/wincron/releases)
[![Build](https://github.com/0x5c0f/wincron/actions/workflows/release.yml/badge.svg)](https://github.com/0x5c0f/wincron/actions/workflows/release.yml)
[![Go](https://img.shields.io/badge/Go-1.20%2B-00ADD8)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

用 Go 编写的 **Windows 后台定时任务服务**，用来替代「必须登录桌面才能运行」的系统计划任务。

它把 Linux `crond` 的工作方式搬到 Windows：程序本身注册为 **Windows 服务**，开机自动运行、与用户登录状态无关；由它拉起的 `bat` / `cmd` / PowerShell 脚本在 **Session 0** 静默执行，不弹控制台窗口、不需要交互式桌面。管理方式同时提供命令行和 Web 界面。

> 项目地址：<https://github.com/0x5c0f/wincron.git>

---

## 目录

- [一、为什么需要它](#一为什么需要它)
- [二、主要特性](#二主要特性)
- [三、系统要求与下载哪个包](#三系统要求与下载哪个包)
- [四、快速开始](#四快速开始)
- [五、命令行](#五命令行)
- [六、配置文件](#六配置文件)
- [七、Cron 表达式](#七cron-表达式)
- [八、Web 管理界面](#八web-管理界面)
- [九、编写 bat / PowerShell 脚本](#九编写-bat--powershell-脚本)
- [十、通知与日志](#十通知与日志)
- [十一、故障排查](#十一故障排查)
- [十二、从源码构建](#十二从源码构建)
- [十三、版本发布流程](#十三版本发布流程)
- [十四、安全须知](#十四安全须知)
- [十五、常见问题](#十五常见问题)
- [十六、项目结构与许可证](#十六项目结构与许可证)

---

## 一、为什么需要它

### Windows 计划任务的痛点

Windows 任务计划程序在服务器场景下常遇到两类问题：

- 配置任务时提示 `0x800710E0`（「操作员或系统管理员拒绝了请求」）等权限错误；
- 任务勾选「只在用户登录时运行」才正常，改成「不管用户是否登录都要运行」后，脚本不执行、或执行了却读不到网络盘 / 用户环境变量 / `HKCU`。

对需要长期无人值守运行的服务器来说，「必须有人登录」是不可接受的前提。

### Session 0 隔离

从 Windows Vista 起，服务运行在 **Session 0**，而用户登录的桌面运行在 **Session 1+**。两者隔离：Session 0 没有交互桌面，也不该向桌面弹窗口。WinCron 正是利用这一点：

```
Windows 启动
  └─ SCM 启动 WinCron 服务 (Session 0, 无需任何用户登录)
       └─ cron 调度器按表达式触发
            └─ cmd /c + CREATE_NO_WINDOW 拉起 backup.bat (仍在 Session 0)
                 └─ 输出被捕获 → logs/<任务ID>.log → 可选 notify 多渠道通知
```

### 两层「后台」

| 层次 | 含义 | WinCron 的做法 |
|---|---|---|
| 服务自身 | 进程不依赖终端、开机自启 | `kardianos/service` 注册为 Windows 服务，SCM 管理生命周期 |
| 被调度的任务 | 双击会弹 cmd 黑窗的脚本，定时跑时也不能弹窗、不能要桌面 | `HideWindow` + `CREATE_NO_WINDOW` + `CREATE_NEW_PROCESS_GROUP`，无控制台窗口 |

> 注意：`wincron.exe run`（前台模式）**不产生 Session 0**，它仍在当前登录会话里，只能用于调试。要验证「锁屏 / 注销 / 重启未登录仍执行」，必须 `install` 成服务。

---

## 二、主要特性

- **真后台**：Windows 服务 + Session 0 无窗口执行，不依赖登录、不弹窗。
- **cron 语法**：5 字段（Linux crontab 习惯）、6 字段（秒级）、`@daily` / `@every 1m` 等描述符自动兼容。
- **两套管理方式**：完整 CLI 与带独立登录页的 Web 界面，功能对等。
- **配置热加载**：`crontab.json` 每 10 秒检测修改时间与大小，改动后自动重载；`wincron reload` 可立即触发。
- **配置健壮性**：UTF-8 BOM / GBK 编码自动识别，原子写入（`.tmp` + rename），启动期非法 JSON fail-fast，运行期改坏则保留上一份有效配置继续跑。
- **任务控制**：超时终止整棵子进程树、失败重试、环境变量、工作目录、手动执行、启停开关。
- **可观测性**：每个任务独立日志、日志保留天数自动清理、Web 执行日志页、`doctor` 运行环境诊断。
- **多渠道通知**：通知层全部基于 [nikoksr/notify](https://github.com/nikoksr/notify)，内置 11 类渠道（企业微信、飞书、钉钉、Slack、Telegram、Bark、邮件、Teams、Discord、Pushover、通用 Webhook），按名称复用、按任务选择，支持 `${ENV:NAME}` 引用凭据、测试发送与失败可观测。
- **中文友好**：自动把 `cmd` 的 GBK 输出转成 UTF-8，避免 Web / 日志乱码。
- **单文件**：一个 exe 内嵌全部 Web 页面，无外部运行时依赖（`CGO_ENABLED=0`）。

---

## 三、系统要求与下载哪个包

每个 Release 提供 **两个未压缩的 amd64 二进制**，区别只在编译工具链：

| 产物 | 编译工具链 | 适用系统 | 建议 |
|---|---|---|---|
| `wincron-<tag>-windows-amd64-legacy.exe` | **Go 1.20.14** | Windows Server 2012 / 2012 R2、Windows 8.1、Windows 10/11、Server 2016 及更高 | Server 2012 / 2012 R2 **必须用这个** |
| `wincron-<tag>-windows-amd64.exe` | Go 1.22.x | Windows 10 / 11、Windows Server 2016 及更高 | 其它现代系统用它 |

原因是 Go 官方从 **1.21 起把最低系统要求提高到 Windows 10 / Server 2016**，Go 1.20 是最后一个能在 Server 2012 上运行的版本。因此 `go.mod` 固定为 `go 1.20`，两套工具链都能编译同一份源码。

两个产物均为 `CGO_ENABLED=0` 静态编译，不依赖 VC++ 运行库；架构只提供 `amd64`。Release 另附 `SHA256SUMS`，校验方式：

```powershell
# 在 exe 与 SHA256SUMS 所在目录
Get-FileHash .\wincron-v1.0.0-windows-amd64.exe -Algorithm SHA256
# 与 SHA256SUMS 中对应行比对
```

> Release 里只放 exe 与校验和。配置模板 `crontab.example.json`、示例脚本 `examples/`、本文档都在仓库里。

---

## 四、快速开始

### 1. 放置程序

新建一个目录（例如 `C:\wincron`），把下载的 exe 重命名为 `wincron.exe` 放进去。**配置文件路径默认以 exe 所在目录为基准**，因为服务由 SCM 启动时工作目录是 `System32`。

### 2. 生成配置文件

```cmd
cd /d C:\wincron
wincron.exe list
```

第一次运行任意管理命令（`list` / `validate` / `add` / `install` 等）都会自动生成 `crontab.json`；已存在时不会覆盖。也可以把仓库里的 `crontab.example.json` 复制过来改名使用。

生成后**第一件事是改密码**：

```json
"web_username": "admin",
"web_password": "CHANGE_ME_BEFORE_USE"
```

> 这是程序内置的占位口令，不是可用的强密码。`web_username` 与 `web_password` 必须同时为空（关闭认证）或同时非空，只填一个会被判定为配置错误并拒绝登录与所有管理 API。

### 3. 校验并添加任务

```cmd
wincron.exe validate
wincron.exe add
```

`add` 是交互式的，整行读取输入，cron 和命令都可以含空格、重定向：

```
任务ID（回车自动生成）:
任务名称: 每日备份
Cron表达式（5或6字段，例: * * * * *）: 0 2 * * *
执行命令（可含空格/重定向）: C:\scripts\backup.bat
工作目录（可选，回车跳过）:
超时时间秒（0=不超时，回车默认0）: 600
最大重试次数（0=不重试，回车默认0）: 1
是否启用（y/n，回车默认y）: y
```

### 4. 安装为服务

**以管理员身份**打开 `cmd` 或 PowerShell：

```cmd
wincron.exe install
wincron.exe start
wincron.exe status
```

`install` 注册为开机自启服务（默认服务名 `WinCron`）。卸载：

```cmd
wincron.exe stop
wincron.exe uninstall
```

### 5. 打开 Web 界面

浏览器访问 <http://localhost:9090>，未登录会跳转到独立登录页 `/login`。服务启动后也可直接看 `wincron.exe status` 输出的实际端口。

### 6.（可选）配置通知渠道

在 `crontab.json` 的 `app.notify_channels` 里定义渠道，再在任务的 `notify.channels` 中按名称引用；`wincron.exe notify types` 列出支持的渠道，`notify list` / `notify test <渠道名>` 用来核验。完整说明见[第十节](#十通知与日志)。

---

## 五、命令行

用法：`wincron.exe <命令> [选项] [参数]`

### 服务管理（install / uninstall / start / stop 需要管理员）

| 命令 | 说明 |
|---|---|
| `install` | 注册为 Windows 服务（开机自启） |
| `uninstall` | 卸载服务 |
| `start` / `stop` / `restart` | 启动 / 停止 / 重启服务 |
| `status` | 查看服务名称、状态、Web 端口、配置文件绝对路径 |

### 运行与诊断

| 命令 | 说明 |
|---|---|
| `run`、`foreground` | 前台运行（调试用，**不是服务**） |
| `validate` | 校验配置文件；报告非法 cron、重复 ID、非法任务 ID、端口越界、认证配置不完整等 |
| `reload` | 更新配置文件时间戳，立即触发服务热加载（不改内容） |
| `doctor` | 输出当前 Session、账户、环境变量、驱动器、风险清单 |
| `doctor -cmd "C:\scripts\backup.bat"` | 用与服务完全相同的执行方式试跑一条命令，验证脚本能否在这种环境下工作 |
| `notify` / `notify list` | 列出已配置通知渠道及其就绪状态、构造失败原因 |
| `notify types` | 列出支持的渠道类型与各自必填字段 |
| `notify test <渠道名>` | 立即向该渠道发送一条测试通知，验证 token / 机器人是否可用 |
| `version` | 显示版本（Release 产物显示对应 tag 版本） |
| `help` | 显示帮助 |

### 任务管理

| 命令 | 说明 |
|---|---|
| `list`、`ls` | 列出全部任务，并附带配置问题提示 |
| `add` | 交互式添加任务 |
| `remove <ID>`、`rm <ID>` | 删除任务 |
| `enable <ID>` / `disable <ID>` | 启用 / 禁用任务 |
| `logs` | 列出日志文件 |
| `logs <ID>` | 查看某任务的执行日志 |

### 全局选项

| 选项 | 说明 |
|---|---|
| `-config <路径>` | 指定配置文件，默认 `exe 同目录/crontab.json`；相对路径会以 exe 目录为基准解析 |
| `-cmd <命令>` | 仅 `doctor` 使用，试跑指定命令 |

选项可以写在命令前后，例如 `wincron.exe remove job-1 -config D:\crontab.json` 同样有效。

---

## 六、配置文件

默认文件名 `crontab.json`，UTF-8 编码（带 BOM 或 GBK/GB18030 也能自动识别）。整体结构：

```json
{
  "app": { "...": "应用级配置" },
  "jobs": [ { "...": "任务" } ]
}
```

### `app` 字段

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `service_name` | string | `WinCron` | Windows 服务名称 |
| `display_name` | string | `WinCron 定时任务服务` | 服务显示名称 |
| `description` | string | — | 服务描述 |
| `service_user` | string | 空 | 服务运行账户，留空 = `LocalSystem`。填写域/本机账户可继承该用户的 `HKCU`、网络凭据与环境变量 |
| `service_password` | string | 空 | 上述账户密码 |
| `web_enabled` | bool | `true` | 是否启用 Web 管理 |
| `web_port` | int | `9090` | Web 端口，越界会自动回退并报告问题 |
| `web_username` | string | `admin` | Web 登录用户名 |
| `web_password` | string | `CHANGE_ME_BEFORE_USE` | Web 登录密码，**必须修改** |
| `log_dir` | string | `logs` | 日志目录，相对路径以 exe 目录为基准 |
| `log_retention_days` | int | `30` | 日志保留天数，过期自动清理 |
| `log_max_entries` | int | `500` | 内存中保留的执行日志条数上限（也是 Web 分页的总量）；有效范围 `10`-`100000`，越界回退默认并在 `validate` 中报告 |
| `log_max_file_mb` | int | `10` | 单个任务 `.log` 文件的体积上限（MB）；接近上限时保留最近半份内容并在文件头写入裁剪说明，有效范围 `1`-`1024` |
| `enable_seconds` | bool | `true` | 兼容保留；5 / 6 字段 cron 现在自动识别，此开关不再影响解析 |
| `notify_channels` | array | `[]` | 全局**命名通知渠道**定义，见[第十节](#十通知与日志)；凭据可写 `${ENV:NAME}` |
| `notify_timeout` | int | `10` | **全局**通知发送超时秒数，有效范围 `1`-`300`；渠道内 `timeout` 为 `0` 时使用该值，非 `0` 则单独覆盖。Web「通知渠道」页可直接修改 |

> 通知渠道定义集中在 `app.notify_channels`，任务只按 `name` 引用。旧版的 `app.smtp_*`、`notify.webhooks`、`notify.emails` 已删除；首次加载旧配置时会自动迁移并备份，见第十节。

### `jobs[]` 字段

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | string | **唯一** ID，用于日志文件名与手动执行；禁止 `..`、路径分隔符及 Windows 非法文件名字符 |
| `name` | string | 显示名称 |
| `cron` | string | cron 表达式，5 / 6 字段或描述符 |
| `command` | string | 要执行的命令，可含空格、管道、重定向 |
| `work_dir` | string | 工作目录（可选），相对路径以 exe 目录为基准 |
| `enabled` | bool | 是否启用 |
| `timeout` | int | 超时秒数，`0` = 不限；超时会终止整棵子进程树 |
| `max_retry` | int | 失败重试次数 |
| `notify` | object | `{ "on_success": bool, "on_failure": bool, "channels": ["渠道名"] }` |
| `env` | object | 追加给该任务的环境变量，如 `{ "BACKUP_ROOT": "D:\\bak" }` |

完整任务示例：

```json
{
  "id": "daily-backup",
  "name": "每日数据库备份",
  "cron": "0 2 * * *",
  "command": "C:\\scripts\\backup.bat >> C:\\logs\\backup.out.log 2>&1",
  "work_dir": "C:\\scripts",
  "enabled": true,
  "timeout": 600,
  "max_retry": 1,
  "notify": {
    "on_success": false,
    "on_failure": true,
    "channels": ["ops-wecom", "ops-slack"]
  },
  "env": {}
}
```

> `channels` 里的名称必须在 `app.notify_channels` 中存在；`validate` 会报告引用了不存在的渠道或重复引用。
>
> JSON 里的 Windows 路径反斜杠必须写成 `\\`。配置文件采用临时文件 + `os.Rename` 原子写入，服务不会读到写了一半的文件。

---

## 七、Cron 表达式

| 写法 | 示例 | 含义 |
|---|---|---|
| **5 字段**（分 时 日 月 周） | `* * * * *` | 每分钟 |
| **5 字段** | `0 2 * * *` | 每天 02:00 |
| **6 字段**（秒 分 时 日 月 周） | `*/10 * * * * *` | 每 10 秒 |
| **描述符** | `@daily`、`@hourly`、`@weekly`、`@monthly`、`@every 1m` | 常用间隔 |

5 字段会自动在前面补 `0 ` 变成 6 字段，因此 `* * * * *` 表示「每分钟的第 0 秒」，与 Linux crontab 语义一致；不需要任何开关。字段数不是 5 或 6 会给出明确中文错误。非法表达式在 `add`、Web 保存、`validate` 三处都会被拦下，不会写进配置。

---

## 八、Web 管理界面

- 地址：`http://localhost:<web_port>`，默认 `9090`。
- **独立登录页** `/login`：纯页面表单，不是浏览器原生认证弹窗。会话由服务端 HMAC 签名的 HttpOnly Cookie 维护，支持退出登录（服务端吊销令牌）、登录失败限流。登录页不显示账号来自哪个配置文件或哪些配置字段；详细排障信息只由 CLI `validate` 提供。
- 页面：任务列表（增删改、启停、立即执行、cron 前置校验）、执行日志、运行环境诊断、**通知渠道**、服务状态。
- **页面位置记忆**：切换页签会写入 URL hash（`#jobs` / `#logs` / `#doctor` / `#notify`），刷新后仍停留在当前页；浏览器前进/后退也能正常切换。
- **自定义下拉**：日志任务筛选、任务启用状态、渠道类型、每页条数都用统一的自定义组件，不使用浏览器原生 `<select>`；支持鼠标、上下方向键、Enter、Esc、Tab 与点击外部关闭，菜单以浮层渲染，不会被弹窗的滚动区域裁剪。
- **执行日志分页**：默认每页 20 条，可切到 50 / 100 条，提供上一页 / 下一页与总数、当前区间、内存保留上限提示。日志总量本身受 `app.log_max_entries` 约束，不会无限增长。
- **运行环境页**：关键环境变量改为自适应网格卡片，长值折行并可「展开完整值」，卡片不再出现横向或纵向滚动条。
- **通知渠道页**：可直接新增、编辑、删除渠道并调整发送超时，保存后立即重建通知器生效，无需等待 10 秒热加载。表单按渠道类型动态渲染必填字段，凭据字段默认掩码显示、可切换明文、支持 `${ENV:NAME}`。渠道名称保存后不可修改（任务按名称引用）；被任务引用的渠道禁止删除。任务编辑弹窗里按复选框选择渠道，成功 / 失败时机分别可勾选。
- **凭据隔离**：渠道列表接口只返回名称 / 类型 / 必填字段和就绪状态，**不回传任何凭据**；完整定义仅由单渠道详情接口按需返回，并带 `Cache-Control: no-store`。
- 右上角显示当前用户并提供「退出登录」；会话过期会自动跳回登录页。

### HTTP API

公开端点：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/login` | 登录页 |
| POST | `/api/login` | 登录，签发会话 Cookie |
| POST | `/api/logout` | 登出并吊销令牌 |
| GET | `/api/auth/status` | 查询登录态、是否免认证、认证配置错误 |

需要登录的接口（`Cookie` 会话，或兼容脚本的 HTTP Basic Auth）：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/status` | 服务状态与配置问题 |
| GET / POST | `/api/jobs` | 列出 / 新增任务 |
| GET / PUT / DELETE | `/api/jobs/:id` | 查询 / 修改 / 删除任务 |
| POST | `/api/jobs/:id/run` | 立即执行一次 |
| POST | `/api/jobs/:id/toggle` | 启用 / 禁用 |
| GET | `/api/logs`、`/api/logs/:jobId` | 分页日志列表 / 指定任务分页日志；查询参数 `offset`（默认 0）、`limit`（默认 20，上限 100）。响应含 `logs`、`count`、`total`、`offset`、`limit`、`has_more`、`max_entries` |
| GET | `/api/schedule/info` | 当前调度表 |
| POST | `/api/schedule/reload` | 触发重载 |
| GET | `/api/doctor` | 运行环境诊断 |
| GET | `/api/validate` | 配置校验结果（含渠道与引用问题） |
| GET | `/api/notify/channels` | 渠道定义（仅 name/type/required）、就绪状态、支持类型、`required_map`、`notify_timeout`、构造错误；**不含凭据** |
| GET | `/api/notify/channels/:name` | 单个渠道完整定义（含凭据），响应带 `Cache-Control: no-store`，仅编辑弹窗按需读取 |
| POST | `/api/notify/channels` | 新增渠道，走与直接编辑配置文件相同的校验并原子写回 |
| PUT | `/api/notify/channels/:name` | 整体替换渠道；名称不可修改，改动立即重建通知器 |
| DELETE | `/api/notify/channels/:name` | 删除渠道；被任务引用时返回 `400` 并指出引用任务 |
| PUT | `/api/notify/settings` | 修改 `notify_timeout`（1-300 秒） |
| POST | `/api/notify/channels/:name/test` | 向指定渠道发送一条测试通知；渠道不可用返回 `502` |

脚本示例：

```bash
curl -u admin:YOUR_PASSWORD http://localhost:9090/api/jobs
curl -u admin:YOUR_PASSWORD -X POST http://localhost:9090/api/jobs/daily-backup/run
```

---

## 九、编写 bat / PowerShell 脚本

Session 0 与「双击运行」不是同一个环境。下面是**最常见的踩坑点**：

| 现象 / 资源 | 双击（用户会话） | WinCron 服务（Session 0） | 建议 |
|---|---|---|---|
| 控制台窗口 | 弹出 cmd 黑窗 | **不弹窗** | 不要依赖窗口，输出写日志 |
| `pause` / `choice` / `read-host` | 等人按键 | 无人应答，任务卡到超时 | 全部去掉 |
| 相对路径 | 相对脚本所在目录 | 服务 cwd 可能是 `System32` | 一律用绝对路径，或配置 `work_dir` |
| 映射盘 `Z:\` | 可见 | **不可见**（驱动器映射属于用户登录会话） | 用 UNC `\\server\share`，并给服务账户授权 |
| `HKCU` / `%USERPROFILE%` / `%APPDATA%` | 当前用户 | `LocalSystem` 的配置单元 | 避免依赖；必要时设置 `service_user` |
| 用户 `PATH`、第三方命令 | 可用 | 可能不同 | 用绝对路径调用外部程序；注意 Git Bash 等终端可能污染 PATH |
| GUI 程序、弹窗 | 可见 | 不可见且可能挂住 | 不要调用 GUI |
| 域网络凭据 | 当前用户凭据 | 服务账户凭据 | 配置 `service_user`，或改用 UNC 显式授权 |
| 中文输出 | 正常 | `cmd` 默认 GBK，WinCron 自动转 UTF-8 | `.bat` 用 GBK + CRLF，或 UTF-8 + `chcp 65001 >nul` |
| PowerShell 执行策略 | 可能因策略被拦 | 同样会拦 | 调用时带 `-ExecutionPolicy Bypass -File` |

`.bat` 模板（保存为 **GBK + CRLF**）：

```bat
@echo off
chcp 936 >nul
rem 所有路径写全，不要 pause，不要依赖映射盘
"C:\Windows\System32\robocopy.exe" "D:\data" "\\nas\backup\data" /MIR /R:2 /W:5
if %ERRORLEVEL% GEQ 8 exit /b 1
echo backup ok
exit /b 0
```

> `robocopy` 退出码 `1` 表示「已复制文件」属于成功，用 `>= 8` 判失败，不要写 `if errorlevel 1`。
>
> 上线前用 `wincron.exe doctor -cmd "C:\scripts\backup.bat"` 以与服务完全一致的执行方式试跑一次，能提前发现绝大多数环境问题。

`examples/` 里的 `backup.bat` 是可被调度的任务脚本模板；`install.bat` / `uninstall.bat` 是给管理员**双击**使用的辅助脚本，其中的 `pause` 只用于人工查看结果，不会被 WinCron 调度，因此不受上面「不要 pause」的约束。

---

## 十、通知与日志

### 日志

`logs/<任务ID>.log` 记录开始/结束时间、退出码、耗时、标准输出与错误输出。日志有**三层上限**，避免高频任务把内存和磁盘撑满：

| 层面 | 配置 | 默认 | 行为 |
|---|---|---|---|
| 内存条数 | `log_max_entries` | `500` | 只保留最近 N 条，也是 Web 分页能看到的总量；调小上限会立即裁剪已有记录 |
| 单文件体积 | `log_max_file_mb` | `10 MB` | 写入前检查体积，接近上限时保留最近半份内容，并在文件头写入裁剪说明；并发写同一任务日志时串行化裁剪与追加 |
| 文件天数 | `log_retention_days` | `30` 天 | 后台定时清理过期 `.log`，始终保留 `environment.log` |

Web「执行日志」页按 `offset` / `limit` 分页读取内存日志，每页最多 100 条；接口同时回传 `total`、`has_more` 与 `max_entries`，前端据此展示页码与保留上限。这三个字段都支持配置热加载，改完 `crontab.json` 无需重启服务。

### 通知层设计：全部基于 nikoksr/notify

项目内**不再保留任何自研的 Webhook / SMTP 发送代码**。所有渠道都由 [`github.com/nikoksr/notify`](https://github.com/nikoksr/notify) 的 `service/*` 子包实现，`internal/notify` 只负责「配置 → notify service」的构造、按任务选择渠道、超时与错误日志。

- **命名渠道**：渠道定义在 `app.notify_channels`，任务只写 `notify.channels: ["渠道名"]`。凭据集中一处，不散落在每个任务里。
- **互相隔离**：发送时为每个渠道构造独立的 `notify.Notify` 实例，避免不同任务的 `receivers` 串台。
- **触发点唯一**：任务执行结束后触发（`cron` 定时执行与 Web「立即执行」都会走）。服务启动/停止、配置变更、错过执行等系统事件**不发通知**。
- **可观测**：发送是异步的，单渠道独立超时；失败原因写入服务日志（Session 0 下写 `service.Logger`，前台模式打印到控制台），不会静默丢通知。服务停止时会等在途通知发完再退出。
- **消息内容**：标题为 `[WinCron] 任务成功/失败: <任务名>`，正文含 ID、状态、命令、起止时间、耗时、退出码、重试次数以及标准输出 / 错误输出；输出与错误各截断到 2000 字节，企业微信 `text.content` 截断到 2048 字节。

> 依赖固定为 **notify v0.38.1**（`go 1.19` directive）。v1.x 起要求 Go 1.22+ / Go 1.25+，会把 `go.mod` 顶到高版本，导致 Server 2012 所需的 Go 1.20.14 legacy 产物无法编译。

### 支持的渠道

| `type` | 必填字段 | 说明 |
|---|---|---|
| `http` | `urls`（也接受单个 `url`） | 通用 Webhook，POST `{"subject": "...", "message": "..."}` |
| `wecom` | `url` | **企业微信群机器人**。notify 没有专用企微 service，这里用 `service/http` 的自定义 `BuildPayload` 生成 `msgtype/text` schema；可选 `mentioned_list`、`mentioned_mobile_list` |
| `lark` | `url` | 飞书群机器人（`NewWebhookService`） |
| `dingding` | `token`、`secret` | 钉钉自定义机器人（加签） |
| `slack` | `token`、`receivers` | Bot token + 频道 ID |
| `telegram` | `token`、`chat_ids` | 构造时会联网校验 token，带 5 秒超时 |
| `bark` | `token` | device key；可选 `servers` 指定自建服务器 |
| `mail` | `smtp_host`、`from`、`receivers` | `service/mail`，`host:port` + 可选 `username` / `password` |
| `msteams` | `receivers` | Teams Incoming Webhook URL 列表 |
| `discord` | `token`、`receivers` | Bot token + 频道 ID |
| `pushover` | `token`、`receivers` | 应用 token + 用户 key |

随时可用 `wincron.exe notify types` 查看该清单与必填字段。渠道级 `timeout` 可覆盖 `app.notify_timeout`。

> notify 仓库共 29 个 `service/*` 子包，这里只接入 11 个：`syslog` 在 Windows 上无法编译；FCM、Amazon SES/SNS、Mailgun、SendGrid、Twilio 等需要各自独立的云 SDK / 凭据模型；另有一些需要 OAuth 交互式授权。它们无法用本项目统一的「命名渠道 + 收件目标」字段描述，硬接会破坏配置模型与 Go 1.20 基线。另外 notify 的 `service/wechat` 是**微信公众号 API**，不是企业微信群机器人，两者不要混用。

### 配置示例

```json
{
  "app": {
    "notify_timeout": 10,
    "log_max_entries": 500,
    "log_max_file_mb": 10,
    "notify_channels": [
      {
        "name": "ops-wecom",
        "type": "wecom",
        "url": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=${ENV:WINCRON_WECOM_KEY}",
        "mentioned_mobile_list": ["@all"]
      },
      {
        "name": "ops-slack",
        "type": "slack",
        "token": "${ENV:WINCRON_SLACK_TOKEN}",
        "receivers": ["#ops-alerts"]
      },
      {
        "name": "ops-mail",
        "type": "mail",
        "smtp_host": "smtp.example.com:587",
        "from": "wincron@example.com",
        "username": "${ENV:WINCRON_SMTP_USER}",
        "password": "${ENV:WINCRON_SMTP_PASS}",
        "receivers": ["ops@example.com"]
      }
    ]
  },
  "jobs": [
    {
      "id": "daily-backup",
      "notify": { "on_success": false, "on_failure": true,
                  "channels": ["ops-wecom", "ops-slack"] }
    }
  ]
}
```

仓库里的 [`crontab.example.json`](crontab.example.json) 是可直接复制的完整模板。

### 凭据：`${ENV:NAME}`

所有字符串字段（`url` / `urls` / `token` / `secret` / `username` / `password` / `receivers` / `servers` / `smtp_host` / `from` / `mentioned_*`）都支持 `${ENV:变量名}`，避免 token 和邮箱密码明文落盘。

- 变量缺失时**只有该渠道被标记为不可用**并写入构造错误，服务照常启动，其它渠道不受影响。
- `validate` 与 `list` 不会要求变量在**当前终端**里存在：交互式终端和 Session 0 服务的环境变量往往不同，在这里报错只会产生假阳性；真正的展开与缺失报错发生在构造渠道时。
- Windows 服务由 SCM 启动，读取的是**系统环境变量**。设置后需要重启服务（或重启系统）才生效；只设在当前用户下，`LocalSystem` 是看不到的。

### 测试与排障

```cmd
wincron.exe notify types                :: 支持哪些类型、各自必填什么
wincron.exe notify list                 :: 渠道是否可用、构造失败原因
wincron.exe notify test ops-wecom       :: 真实发一条测试通知
```

Web「通知渠道」页可以直接新增 / 编辑 / 删除渠道、修改发送超时，并对可用渠道点「测试发送」；保存时与 `wincron notify` 一样走完整校验、原子写回配置文件，并立即重建通知器。`GET /api/notify/channels` 可脚本化查询（不含凭据），`GET /api/notify/channels/:name` 才会返回编辑所需的完整定义。渠道发送失败、5xx、网络不可达都会写进服务日志。

### 旧配置自动迁移

旧版的 `app.smtp_*`、`notify.webhooks`、`notify.emails` 已彻底删除，但升级不会让现有任务静默丢通知：

1. 首次 `Load()` 检测到旧字段时，按任务生成 `legacy-<任务ID>-http` 与 `legacy-<任务ID>-mail` 两个命名渠道（`smtp_host` + `smtp_port` 合成 `host:port`，`smtp_user` 同时作为 `from` 与 `username`），任务改成按名称引用。
2. 写回前把原文件**逐字节**备份为 `crontab.json.pre-notify.bak`；该备份已存在时不覆盖，保证最原始的那份不被热加载反复改写。
3. 写回后旧字段消失；迁移是幂等的，二次加载不会追加重复渠道。写回失败时内存里已是新格式，下次加载会重试。

### 已知边界

- 没有重试、限流、去重、通知历史与自定义消息模板；失败只记日志，不补偿。
- notify 各 service 对 `context` 的支持程度不一致：`service/http` 会真正响应取消，而 `service/mail` 只在发送前检查一次 ctx。因此 `notify_timeout` 主要兜住 HTTP 类渠道，SMTP 类还依赖服务端超时设置。
- `service/mail` 走 STARTTLS（常见 587）；**不支持 465 隐式 TLS**。
- `telegram.New` 会联网校验 token，本项目给它套了 5 秒初始化超时；网络不通时该渠道不可用，不影响其它渠道。
- 通用 `http` 渠道发的是 notify 默认 schema，不能直接把企业微信 / 钉钉 / 飞书机器人地址填进去；这些平台请用对应的 `wecom` / `dingding` / `lark` 类型。

---

## 十一、故障排查

| 症状 | 排查顺序 |
|---|---|
| `install` 报 `Access is denied` | 必须**以管理员身份**运行终端 |
| 「双击脚本能跑，定时不跑」 | ① `wincron doctor -cmd "<命令>"` ② 检查绝对路径 / 映射盘 / `HKCU` / PATH ③ 看 `logs/environment.log` 或 Web「运行环境」页 |
| 服务启动了但没有任务执行 | `wincron status` → `wincron list`（看 `enabled`）→ `wincron validate`（看 cron 是否合法）→ 看 Web `/api/schedule/info` |
| Web 打不开 | 确认 `web_enabled=true`、端口未被占用、防火墙放行；`status` 会打印实际端口 |
| 登录一直失败 | `web_username` 与 `web_password` 必须同时非空；`GET /api/auth/status` 会返回 `config_error`；连续失败 10 次会触发 30 秒限流 |
| 改了配置没生效 | 等 10 秒热加载，或 `wincron reload`；`list` 会列出配置问题 |
| 配置改坏了服务退出 | 启动期非法 JSON 会 fail-fast 并打印中文错误；修好后重启即可。运行期改坏不会中断服务，会保留上一份有效配置 |
| 日志中文乱码 | 确认程序为当前版本；脚本自身编码见第九节 |
| 任务超时后仍有残留进程 | 超时会终止整棵子进程树；若脚本又拉起了脱离作业的新进程，请检查脚本逻辑 |
| 通知完全没收到 | ① `notify list` 看渠道是否「可用」 ② 检查 `on_success` / `on_failure` 与 `notify.channels` 是否填了 ③ `notify test <渠道名>` 单独验证 ④ 看服务日志里的「通知发送失败」 |
| 渠道显示「环境变量未设置」 | 确认变量是**系统**环境变量、重启过服务；`LocalSystem` 看不到当前用户的用户级变量。`validate` 不报错是正常设计 |
| 企业微信 / 飞书机器人收不到 | 必须用 `wecom` / `lark` 类型；通用 `http` 渠道发的是 `{"subject","message"}`，不符合这些平台的 schema |
| 邮件发不出去 | `service/mail` 只支持 STARTTLS（587），不支持 465 隐式 TLS；`smtp_host` 要写 `host:port` |

---

## 十二、从源码构建

```bash
git clone https://github.com/0x5c0f/wincron.git
cd wincron
```

`go.mod` 固定为 `go 1.20`，因此 Go 1.20.14 及以上工具链都能编译。通知依赖固定为 `github.com/nikoksr/notify v0.38.1`（更高的 v1.x 要求 Go 1.22+ / 1.25+，会破坏 legacy 产物）；如果要升级 notify，必须同步复核 Go 1.20.14 工具链能否编译。

```bash
# 标准检验流程
go mod download
go mod verify
gofmt -l .          # 无输出即通过
go vet ./...
go test ./... -count=1

# 现代系统产物
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -o wincron.exe ./cmd/wincron

# Server 2012 / 2012 R2 产物：必须换用 Go 1.20.14 工具链
# 方式一：本机已装 Go 1.20.14，直接用它执行
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -o wincron-legacy.exe ./cmd/wincron

# 方式二：本机是 Go 1.21+，用 GOTOOLCHAIN 自动切换到 1.20.14
GOTOOLCHAIN=go1.20.14 CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -o wincron-legacy.exe ./cmd/wincron
```

> `GOTOOLCHAIN` 需要 Go 1.21+ 才能识别；Go 1.20.14 本身就是目标工具链，直接用它执行即可。

版本信息通过链接期注入：

```bash
go build -ldflags "-s -w -X main.version=1.0.0" -o wincron.exe ./cmd/wincron
wincron.exe version   # WinCron v1.0.0
```

---

## 十三、版本发布流程

发布完全由 **tag 推送**触发，工作流为 `.github/workflows/release.yml`。

### 标签规范

| 标签 | 是否打包 | Release 类型 | `prerelease` |
|---|---|---|---|
| `v1.0.0`、`v1.2.3` | ✅ | 正式版 | `false` |
| `v1.0.0-rc.1`、`v1.0.0-rc.2` | ✅ | 对应版本的预发布版 | `true` |
| `v1.0.0-pre`、`v1.0.0-beta`、`v1.0` 等其它 `v*` 标签 | ❌ | 静默跳过，工作流不报错 | — |

只有严格匹配 `^v[0-9]+\.[0-9]+\.[0-9]+$` 或 `^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$` 的标签才会构建发布。Release 的 `tag_name` 就是触发它的原始标签，因此 `v1.0.0-rc.1` 与 `v1.0.0` 可以共存、互不覆盖。

> 文档顶部的 Release badge 带 `include_prereleases` 参数：shields.io 默认只统计正式 Release，仓库尚处于预发布阶段时会显示「no releases」；加上该参数后会显示最新版本（含预发布）。

### 构建矩阵

每个 Release 由两套工具链各编一个产物（见[第三节](#三系统要求与下载哪个包)）：`Go 1.20.14` → `-legacy`，`Go 1.22.x` → 默认包。每一套都会完整跑一遍标准 Go 检验流程：

```
go mod download → go mod verify → gofmt -l → go vet ./... → go test ./... -count=1 → go build → 冒烟 version → sha256sum
```

### 发布产物

```
wincron-v1.0.0-rc.1-windows-amd64-legacy.exe
wincron-v1.0.0-rc.1-windows-amd64.exe
SHA256SUMS
```

工作流在发布前会校验 exe 数量并用 `sha256sum -c` 复核校验和；产物不压缩，直接下载 exe 即可。

发布示例：

```bash
git tag v1.0.0
git push origin main --tags
# 或预发布
git tag v1.0.0-rc.1
git push origin v1.0.0-rc.1
```

---

## 十四、安全须知

- **必须修改 `web_password`**：内置值 `CHANGE_ME_BEFORE_USE` 只是占位符，任何能访问 Web 端口的人都能用它登录并增删任务。
- 配置文件里包含 Web 密码、通知渠道 token / 邮箱密码、可选的服务账户密码，请限制该文件的 ACL，不要提交到公开仓库（本仓库 `.gitignore` 已忽略运行时的 `crontab.json`）。通知凭据强烈建议写成 `${ENV:NAME}` 并设为系统环境变量；Web 的 `/api/notify/channels` 只返回名称与状态，不回传凭据。只有在已登录会话里打开编辑弹窗时，`/api/notify/channels/:name` 才会返回单个渠道的完整定义，该响应带 `Cache-Control: no-store`；配置文件本身仍是敏感文件。
- 默认 Web 监听 `9090`，建议仅在本机/内网访问，必要时用防火墙限制来源。
- 任务是**以服务账户权限执行任意命令**的，等同于给配置文件写入者该权限；不要给不可信来源开放配置修改能力。
- 会话密钥由进程随机生成、不落盘；修改用户名/密码、重启进程、会话过期、主动登出都会使旧会话失效。

---

## 十五、常见问题

**Q：锁屏、注销、远程桌面断开后任务还会跑吗？**
A：装成服务后由 SCM 在 Session 0 管理，与交互登录无关，会继续跑。`wincron.exe run` 前台模式仍在登录会话，**不能**用来验证这一点。

**Q：脚本双击会弹 cmd 窗口，定时执行会弹吗？**
A：不会。服务使用 `HideWindow` + `CREATE_NO_WINDOW` 启动 `cmd /c`，既没有可见控制台，也不需要桌面。

**Q：`* * * * *` 会被识别成一个 `*` 吗？**
A：不会。CLI 交互式输入按整行读取，含空格的 cron 和 `echo aaa >> c:\b.txt` 这类命令都能完整写入；`add` 结束前会回显实际落盘内容。

**Q：配置文件在哪？能不能换位置？**
A：默认是 exe 同目录的 `crontab.json`；用 `-config D:\path\crontab.json` 指定其它位置，但**安装服务时也要传同样的 `-config`**，否则服务读的是默认路径。

**Q：怎么让任务以某个域用户身份运行（访问共享 / HKCU）？**
A：设置 `app.service_user` 与 `app.service_password` 后重新安装/重启服务。留空则是 `LocalSystem`。

**Q：为什么要出两个 exe？**
A：Go 1.21 起官方不再支持 Windows Server 2012 / 2012 R2，只有 Go 1.20 编译的 legacy 包能覆盖这些系统；现代系统则用较新工具链的产物。这也是 notify 依赖必须锁在 v0.38.1 的原因。

**Q：能不能直接把企业微信 / 钉钉机器人地址填进 `http` 渠道？**
A：不能。通用 `http` 渠道发的是 notify 默认的 `{"subject","message"}`。请分别使用 `wecom` / `dingding` / `lark` 类型，它们会生成各平台要求的 JSON schema。

**Q：升级后旧的 `webhooks` / `emails` / `smtp_*` 还能用吗？**
A：能，第一次加载会自动迁移成命名渠道并改写 `crontab.json`，原文件逐字节备份在 `crontab.json.pre-notify.bak`。迁移后建议把凭据改成 `${ENV:NAME}`，并用 `notify list` / `notify test` 复核。

**Q：通知渠道的凭据会被 Web 页面看到吗？**
A：`/api/notify/channels` 只返回 `name` / `type` / `required` 和就绪状态，不返回 `url` / `token` / `password`；只有 `/api/notify/channels/:name` 会在已登录会话中返回完整定义并禁止缓存。但 `crontab.json` 本身仍含这些值（或环境变量名），请限制文件 ACL。

**Q：服务启动、配置变更、任务超时被杀会发通知吗？**
A：不会。当前只有「任务执行结束」一个触发点，且按 `on_success` / `on_failure` 决定；失败原因可从任务日志和服务日志查看。

---

## 十六、项目结构与许可证

```
cmd/wincron/          CLI 入口与命令分发
internal/config/      配置读写、校验、深拷贝、原子写入、热加载
internal/cronexpr/    cron 表达式归一化与校验
internal/scheduler/   任务调度与重载
internal/executor/    无窗口执行、输出转码、超时杀进程树、日志
internal/service/     Windows 服务生命周期与环境快照
internal/web/         Gin Web 服务、会话认证、内嵌 HTML 页面
internal/notify/      notify service 构造、渠道路由、超时与失败日志
internal/config/notify.go  渠道模型、${ENV:NAME} 展开、旧配置迁移与校验
internal/doctor/      运行环境诊断
internal/paths/       路径基准（exe 所在目录）
examples/             示例脚本
```

依赖：[gin](https://github.com/gin-gonic/gin)、[cron/v3](https://github.com/robfig/cron)、[service](https://github.com/kardianos/service)、[notify](https://github.com/nikoksr/notify)、`golang.org/x/sys`、`golang.org/x/text`。

本项目采用 [MIT License](LICENSE) · Copyright (c) 2026 0x5c0f。
