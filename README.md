# GPanel

极简游戏服务器托管面板：游戏实例管理 + Web 终端 + 文件管理。Go 单二进制，前端内嵌，无需 Node、数据库或外部 PTY 程序。

## 运行

```bash
go build -o bin/gpanel ./cmd/gpanel
./bin/gpanel -addr 127.0.0.1:24444 -data ./data
```

首次启动会在日志中打印 `admin` 的初始密码。对公网开放时请在前面放 Caddy / Nginx 提供 HTTPS，并用 `-addr 0.0.0.0:24444` 或只让反代访问。

| 参数 | 环境变量 | 默认 |
|---|---|---|
| `-addr` | `GPANEL_ADDR` | `127.0.0.1:24444` |
| `-data` | `GPANEL_DATA` | `data` |
| `-web-dir` | `GPANEL_WEB_DIR` | 空（使用内嵌前端）；开发时设为 `web/static` 免重编译 |

交叉编译：`CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/gpanel`

## 目录结构

```
cmd/gpanel/          入口：组装各模块
internal/
  config/            启动参数
  store/             JSON 文件持久化（接口很窄，可换 SQLite）
  events/            进程内事件总线（实例状态变化等）
  auth/              登录、会话、CSRF 校验
  pty/               伪终端抽象；pty_unix.go 实现，pty_windows.go 待实现 (ConPTY)
  instance/          实例配置、生命周期、崩溃重启、终端 WebSocket
  files/             基于 os.Root 的沙箱文件管理
  runtimes/          运行时检测（Java），通过 LaunchHook 注入启动参数
web/static/          前端（Vue 3 + xterm.js，无构建步骤）
data/                运行时数据：users.json、instances.json、instances/<id>/、runtimes/java/
```

## 扩展点

加功能时尽量只新增包，不改核心：

1. **新 HTTP 模块**：实现 `httpx.Module`（`Register(mux)`），在 `cmd/gpanel/main.go` 的 `modules` 列表加一行。
2. **影响实例启动**：`instances.AddLaunchHook(func(cfg, spec) error)`，可写入命令变量 `spec.Vars`（命令中的 `{{name}}`）、环境变量 `spec.Env`、`spec.PathPrepend`。Java 运行时就是这样接入的。
3. **实例的附加配置**：存进 `Config.Extensions["模块名"]`，无需改动 `instance.Config`。
4. **响应实例事件**（定时任务、通知、备份）：`bus.Subscribe(func(events.Event))`。
5. **新运行时类型**（Node、.NET…）：实现 `runtimes.Provider`，传给 `runtimes.NewRegistry`。要支持在线安装，再实现 `runtimes.Installer`（只需列出版本和安装目录，下载、校验、解压、任务进度都是通用的）。
6. **另一类文件根目录**（如备份目录）：再挂一个 `files.NewHandler(prefix, rootFunc)`。
7. **前端页面**：`app.js` 的 `nav` 加一项；实例详情页的标签在 `views/instance.js` 的 `tabs`。

## Java 版本

- **在线安装**：「运行时」页列出 Eclipse Temurin（Adoptium API）各主版本最新的 JDK / JRE，一键安装到 `data/runtimes/java/<版本>/`。下载后校验 SHA-256，安全解压（拒绝越界路径和符号链接），可取消；未被实例使用的面板安装版本可卸载。
- **国内镜像**：`-java-mirror` / `GPANEL_JAVA_MIRROR` 设置下载地址模板，例如清华 TUNA：
  ```
  https://mirrors.tuna.tsinghua.edu.cn/Adoptium/{major}/{image}/{arch}/{os}/{file}
  ```
  镜像常比官方晚几天，缺少最新构建时自动回退到官方源（GitHub）；无论来源都用官方 API 给出的 SHA-256 校验。
- 自动检测 `JAVA_HOME`、PATH、系统常见目录（`/usr/lib/jvm`、`/Library/Java/JavaVirtualMachines`、`C:\Program Files\Java` 等）。也可以手动把 JDK 解压到 `data/runtimes/java/<任意名>/` 后重新扫描。
- 实例设置里选择运行时；启动命令用 `{{java}}` 引用，例如 `{{java}} -Xmx4G -jar server.jar nogui`。同时会设置 `JAVA_HOME` 并把其 `bin` 放到 PATH 最前。

## 终端协议

`GET /api/instances/{id}/terminal`（WebSocket）

- 服务端 → 客户端：二进制帧为原始终端输出；文本帧 `{"type":"status","data":{...}}`
- 客户端 → 服务端：`{"type":"input","data":"..."}`、`{"type":"resize","cols":120,"rows":30}`

## 待办

- Windows ConPTY 实现
- 大文件分块/断点续传上传；解压大小限制
- 会话持久化（目前重启面板需重新登录）
- 多用户与按实例授权
