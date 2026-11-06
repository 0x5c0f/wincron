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
                 └─ 输出被捕获 → logs/<任务ID>.log → 可选 Webhook / 邮件通知
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
- **通知**：成功/失败可选 Webhook 与 SMTP 邮件。
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
| `enable_seconds` | bool | `true` | 兼容保留；5 / 6 字段 cron 现在自动识别，此开关不再影响解析 |
| `smtp_host` | string | 空 | SMTP 服务器，留空则不发邮件 |
| `smtp_port` | string | `587` | SMTP 端口 |
| `smtp_user` | string | 空 | SMTP 账号，同时作为发件人 |
| `smtp_pass` | string | 空 | SMTP 密码 / 授权码 |

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
| `notify` | object | `{ "on_success": bool, "on_failure": bool, "webhooks": [], "emails": [] }` |
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
    "webhooks": ["https://example.com/hook"],
    "emails": ["ops@example.com"]
  },
  "env": {}
}
```

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
- **独立登录页** `/login`：纯页面表单，不是浏览器原生认证弹窗。会话由服务端 HMAC 签名的 HttpOnly Cookie 维护，支持退出登录（服务端吊销令牌）、登录失败限流。
- 页面：任务列表（增删改、启停、立即执行、cron 前置校验）、执行日志、运行环境诊断、服务状态。
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
| GET | `/api/logs`、`/api/logs/:jobId` | 日志列表 / 指定任务日志 |
| GET | `/api/schedule/info` | 当前调度表 |
| POST | `/api/schedule/reload` | 触发重载 |
| GET | `/api/doctor` | 运行环境诊断 |
| GET | `/api/validate` | 配置校验结果 |

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

- **日志**：`logs/<任务ID>.log`，记录开始/结束时间、退出码、耗时、标准输出与错误输出；`log_retention_days` 控制自动清理。
- **Webhook**：`notify.webhooks` 中的 URL 会收到任务结果的 JSON POST，由 `on_success` / `on_failure` 决定是否触发。
- **邮件**：配置 `app.smtp_*` 后，`notify.emails` 中的收件人会收到结果邮件；`smtp_host` 留空则跳过邮件发送。

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

---

## 十二、从源码构建

```bash
git clone https://github.com/0x5c0f/wincron.git
cd wincron
```

`go.mod` 固定为 `go 1.20`，因此 Go 1.20.14 及以上工具链都能编译。

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
- 配置文件里包含 Web 密码、SMTP 密码、可选的服务账户密码，请限制该文件的 ACL，不要提交到公开仓库（本仓库 `.gitignore` 已忽略运行时的 `crontab.json`）。
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
A：Go 1.21 起官方不再支持 Windows Server 2012 / 2012 R2，只有 Go 1.20 编译的 legacy 包能覆盖这些系统；现代系统则用较新工具链的产物。

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
internal/notify/      Webhook / SMTP 通知
internal/doctor/      运行环境诊断
internal/paths/       路径基准（exe 所在目录）
examples/             示例脚本
```

依赖：[gin](https://github.com/gin-gonic/gin)、[cron/v3](https://github.com/robfig/cron)、[service](https://github.com/kardianos/service)、`golang.org/x/sys`、`golang.org/x/text`。

本项目采用 [MIT License](LICENSE) · Copyright (c) 2026 0x5c0f。
