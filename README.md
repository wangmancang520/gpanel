# GPanel

极简游戏服务器托管面板：游戏实例管理 + Web 终端 + 文件管理。Go 单二进制，前端内嵌，无需 Node、数据库或外部 PTY 程序。

## 一键安装

适用于使用 systemd 的 Linux（Ubuntu / Debian / CentOS / Rocky / Arch 等），x86_64 或 arm64。以 root 运行：

```bash
bash <(curl -fsSL https://github.com/wangmancang520/gpanel/releases/latest/download/install.sh)
```

安装过程中会询问端口（默认 24444）、是否允许外网访问、是否使用清华镜像下载 Java，结束时显示访问地址和 `admin` 的初始密码。之后用维护菜单管理面板：

```bash
sudo gpanel-ctl
```

```
  ╭─────────╮
  │  ▶ ▶ ▶  │  GPanel  游戏服务器管理面板
  ╰─────────╯  安装与维护工具 · github.com/wangmancang520/gpanel
  ────────────────────────────────────────────────────
  状态  ● 运行中    版本  v0.1.0  （可更新到 v0.2.0）
  地址  http://203.0.113.10:24444
  ────────────────────────────────────────────────────

  更新
    1  更新面板  （新版本 v0.2.0）
    2  回滚到上一版本

  运行
    3  启动 / 停止 / 重启
    4  查看状态
    5  查看日志

  维护
    6  重置管理员密码
    7  修改配置（端口 / Java 下载源 / HTTPS 救急）
    8  备份数据
    9  卸载面板

  迁移
   10  从 GSManager 迁移实例
    0  退出
```

每一项也可以直接执行，方便写进脚本或定时任务，`sudo gpanel-ctl help` 查看全部命令和选项：

| 命令 | 作用 |
|---|---|
| `sudo gpanel-ctl update` | 更新到最新版本：下载并校验 SHA-256 → 备份当前版本 → 替换并重启 → 检查面板是否正常；**新版本起不来会自动回滚**。维护脚本本身也随之更新 |
| `sudo gpanel-ctl rollback` | 回到更新前的版本（再执行一次可以回来） |
| `sudo gpanel-ctl reset-password` | 忘记密码时用：生成新密码并立即生效，同时退出所有已登录的会话；**不需要重启面板，正在运行的游戏服务器不受影响** |
| `sudo gpanel-ctl config` | 修改端口和监听地址、切换 Java 下载源、HTTPS 救急开关（证书问题导致打不开面板时临时只用 HTTP） |
| `sudo gpanel-ctl backup` | 把数据目录和配置打包到 `/root/gpanel-backup-时间.tar.gz`（不含可重新下载的 Java） |
| `sudo gpanel-ctl uninstall` | 卸载，默认保留数据（重新安装后可继续使用）；加 `--purge` 彻底删除 |
| `sudo gpanel-ctl migrate-gsmanager` | 从常规安装的 GSManager 迁移游戏实例，见下文「从 GSManager 迁移」 |
| `sudo gpanel-ctl install -y --port 8080 --bind 0.0.0.0` | 无人值守安装；`--version v0.2.0` 指定版本，`--local ./gpanel` 用本地文件 |

说明：

- 文件位置：程序 `/usr/local/bin/gpanel`，配置 `/etc/gpanel/gpanel.env`，数据 `/var/lib/gpanel`，日志 `journalctl -u gpanel`。面板和游戏服务器都以系统用户 `gpanel` 运行，只额外拥有绑定 1024 以下端口的权限（用于 HTTP 方式申请证书）。
- **重启、更新面板会先优雅停止正在运行的游戏服务器**（发送停止命令，超时后强制结束），勾选了「自动启动」的实例会在面板启动后恢复；菜单在这类操作前会提示确认。
- 访问 GitHub 慢时，可以在安装时或「修改配置」中设置 GitHub 下载加速前缀（也可用 `--mirror` / `GPANEL_MIRROR`）；校验文件 `SHA256SUMS` 始终直接从 GitHub 获取，所以加速服务无法篡改程序；连不上 GitHub 时需要确认（或设置 `GPANEL_TRUST_MIRROR=1`）才会改用加速服务提供的校验文件。
- 对公网开放时，登录后请在「安全」页修改密码并配置 HTTPS（支持用域名免费申请 Let's Encrypt 证书）。

## 从 GSManager 迁移

已经在用 [GSManager 3](https://github.com/GSManagerXZ/GameServerManager)（常规安装，非 Docker）时，安装 GPanel 后运行：

```bash
sudo gpanel-ctl migrate-gsmanager      # 或菜单中的「从 GSManager 迁移实例」
```

它会先列出每个实例将如何迁移（不做任何修改），确认后：停止 GSManager 并关闭它的开机自启 → 停止 GPanel → 导入实例 → 把游戏目录的所有者改为 `gpanel` → 启动 GPanel。游戏文件**原地使用，不复制**。

| GSManager | GPanel |
|---|---|
| 名称、工作目录、自动启动 | 原样保留 |
| 启动命令（在 bash 中执行） | 需要 shell 的命令（`cd … &&`、管道、`./脚本`等）包成 `bash -c '…'`；简单命令保持不变 |
| 我的世界 Java 版 / 基岩版 | 按 GSManager 的规则识别 `start.sh`、`run.sh`、服务端 jar 或 `bedrock_server`，生成固定的启动命令 |
| 停止方式 stop / exit / quit / ctrl+c | 同名停止命令；ctrl+c 对应 `^C` |
| Java 版本（java8、java17…） | 优先用 GPanel 管理的同一主版本；否则复制 GSManager 当初使用的 JDK；都没有时才用系统中的同一主版本 |

- 位于 gpanel 用户进不去的目录（如 `/root` 下）的实例，会询问是否移动到 `/var/lib/gpanel/instances`（同一磁盘上是瞬间完成的改名）。
- 不迁移：GSManager 的账号、定时任务、RCON、备份、插件；以其他用户运行（终端用户）的设置；SteamCMD 一键更新（GPanel 暂不支持，游戏本身照常运行）。
- **迁移前请先在 GSManager 中停止正在运行的服务器**，让它们正常保存。GSManager 不会被卸载、配置也不会改动；可以重复运行，已迁移的实例会跳过。
- 安装目录不是默认的 `/opt/gsmanager3` 且没有 `gsm3` 服务时，用 `--gsm-dir` 指定。

## 从源码运行

```bash
go build -o bin/gpanel ./cmd/gpanel
./bin/gpanel -addr 127.0.0.1:24444 -data ./data
```

首次启动会在日志中打印 `admin` 的初始密码。对公网开放时需要 HTTPS：可以在面板「安全」页上传证书或生成自签名证书（见下文「HTTPS」），也可以在前面放 Caddy / Nginx 反代并只让反代访问面板。

| 参数 | 环境变量 | 默认 |
|---|---|---|
| `-addr` | `GPANEL_ADDR` | `127.0.0.1:24444` |
| `-data` | `GPANEL_DATA` | `data` |
| `-web-dir` | `GPANEL_WEB_DIR` | 空（使用内嵌前端）；开发时设为 `web/static` 免重编译 |
| `-java-mirror` | `GPANEL_JAVA_MIRROR` | 空（官方源）；见下文「Java 版本」 |
| `-disable-tls` | `GPANEL_DISABLE_TLS=1` | 关闭；临时只用 HTTP，忽略 HTTPS 设置（证书出问题时用来恢复） |

交叉编译：`CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/gpanel`

其他命令行用法：`gpanel -version` 显示版本；`gpanel -data DIR -reset-password [-user admin]` 离线重置密码（运行中的面板会自动读到新密码）。

用自己编译的程序安装为服务：`sudo ./install.sh install --local bin/gpanel`。systemd unit 内嵌在程序里（`gpanel -print-systemd-unit`），安装和更新时写入与程序版本匹配的 unit。unit 使用 `KillMode=mixed`，保证关机时由面板逐个优雅停止实例，而不是 systemd 同时给所有进程发 SIGTERM。

## 发布新版本

```bash
git tag v0.2.0 && git push origin v0.2.0
```

`.github/workflows/release.yml` 会测试、为 5 个平台编译（版本号写入程序）、生成 `SHA256SUMS`，并连同 `install.sh` 发布到 GitHub Releases。已安装的服务器运行 `sudo gpanel-ctl update` 即可升级，菜单顶部也会提示有新版本。

## 开发与测试

```bash
go vet ./... && go test ./...
go build -o bin/gpanel ./cmd/gpanel
scripts/smoke.sh bin/gpanel                  # 端到端冒烟测试（macOS / Linux）
SMOKE_JAVA=1 scripts/smoke.sh bin/gpanel     # 额外真实安装并运行一个 Temurin JRE
```

推送到任意分支都会触发 GitHub Actions（`.github/workflows/ci.yml`），在 Ubuntu 上执行：gofmt / vet / 带竞态检测的单元测试、含真实 Java 安装的冒烟测试、shellcheck、在真实 systemd 下用 `install.sh` 走完安装 → 重置密码 → 更新 → 坏版本自动回滚 → 手动回滚 → 从发布包下载并校验 → 备份 → 卸载的全流程（`scripts/systemd-smoke.sh`），以及 5 个平台的交叉编译（产物保留 7 天）。

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
  system/            主机配置与负载采样（概览页），按进程组统计实例 CPU / 内存
  certs/             面板自身的 HTTPS 证书管理；同一端口同时服务 HTTP 和 HTTPS
  gsmanager/         从常规安装的 GSManager 3 导入实例（gpanel -import-gsmanager）
web/static/          前端（Vue 3 + xterm.js，无构建步骤）
deploy/              systemd unit 与环境变量模板（内嵌进程序）
install.sh           一键安装与维护工具（安装后为 gpanel-ctl）
scripts/             冒烟测试（smoke.sh、systemd-smoke.sh）
.github/workflows/   CI
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

## HTTPS

在「安全」页管理，设置和证书存放在 `data/tls/`（私钥权限 0600）：

- **通过域名申请**：向 Let's Encrypt（或任意 ACME 服务）免费申请证书，签发后自动安装，到期前 30 天自动续期。三种验证方式：
  - **HTTP 验证**：申请时临时监听 80 端口。要求域名解析到本机、公网 80 端口可达。用 systemd 部署时 unit 已授予绑定低端口的权限（仅此一项）；有反向代理时也可以把 `/.well-known/acme-challenge/` 转发到面板端口。
  - **DNS 自动验证**：通过阿里云 DNS、腾讯云 DNSPod 或 Cloudflare 的 API 添加 TXT 记录，用完自动删除。无需开放端口，支持通配符证书。建议为面板单独创建只有 DNS 权限的子账号密钥；密钥保存在 `data/tls/acme.json`（0600），不会回传到浏览器。
  - **DNS 手动验证**：面板列出要添加的 TXT 记录，可先「检查解析」再开始验证。不能自动续期。
  - 调试时请选「Let's Encrypt 测试环境」，避免触发正式环境的频率限制。
- **上传证书**：PEM 格式的完整证书链（如 Let's Encrypt 的 `fullchain.pem`）和未加密的私钥。保存前会校验证书与私钥是否匹配、是否过期。
- **生成自签名证书**：ECDSA P-256，可填多个域名或 IP，适合内网；浏览器首次访问会提示不受信任。
- **启用 HTTPS**：同一端口同时接受 HTTP 和 HTTPS（按连接的第一个字节区分），开关和替换证书都即时生效，无需重启。
- **强制 HTTPS**：HTTP 请求 307 跳转到 HTTPS。只能在已通过 HTTPS 打开页面时开启，确保证书可用后才切换。
- **救急**：如果证书或强制跳转导致无法访问，用 `-disable-tls`（或 `GPANEL_DISABLE_TLS=1`）启动面板，会临时只用 HTTP，已保存的设置不变。

修改密码后，该账号在其他设备上的登录会话会被立即退出。

## 终端协议

`GET /api/instances/{id}/terminal`（WebSocket）

- 服务端 → 客户端：二进制帧为原始终端输出；文本帧 `{"type":"status","data":{...}}`
- 客户端 → 服务端：`{"type":"input","data":"..."}`、`{"type":"resize","cols":120,"rows":30}`

## 待办

- Windows ConPTY 实现
- 大文件分块/断点续传上传；解压大小限制
- 会话持久化（目前重启面板需重新登录）
- 多用户与按实例授权
