#!/usr/bin/env bash
# GPanel 一键安装与维护工具
#
#   安装:  bash <(curl -fsSL https://github.com/wangmancang520/gpanel/releases/latest/download/install.sh)
#   维护:  sudo gpanel-ctl            # 打开菜单
#          sudo gpanel-ctl update     # 也可以直接执行某个操作，见 gpanel-ctl help
#
# Installs the panel as a systemd service (user "gpanel", data in
# /var/lib/gpanel), and later updates it from GitHub releases: download,
# SHA-256 check, swap with a backup, restart, health check, automatic
# rollback if the new version doesn't come up.
set -Eeuo pipefail

REPO="${GPANEL_REPO:-wangmancang520/gpanel}"
RELEASES="${GPANEL_RELEASES:-https://github.com/$REPO/releases}" # override for tests/mirrors
API="https://api.github.com/repos/$REPO"
TOKEN="${GPANEL_GITHUB_TOKEN:-}" # only for private forks; the main repo is public

BIN=/usr/local/bin/gpanel
CTL=/usr/local/bin/gpanel-ctl
UNIT=/etc/systemd/system/gpanel.service
ETC=/etc/gpanel
ENV_FILE=$ETC/gpanel.env
CTL_CONF=$ETC/ctl.conf
DATA=/var/lib/gpanel
LIB=/usr/local/lib/gpanel # previous version, for rollback
SVC=gpanel
DEFAULT_PORT=24444
TUNA='https://mirrors.tuna.tsinghua.edu.cn/Adoptium/{major}/{image}/{arch}/{os}/{file}'

# Options (also settable as flags, see usage).
YES=0 PURGE=0 FORCE=0 LOCAL="" WANT_VERSION="" OPT_PORT="" OPT_BIND="" OPT_USER="" MIRROR=""

# ---------------------------------------------------------------- output

if [ -t 1 ] && [ "${NO_COLOR:-}" = "" ]; then
  B=$'\e[1m' D=$'\e[2m' R=$'\e[0m' RED=$'\e[31m' GRN=$'\e[32m' YLW=$'\e[33m' BLU=$'\e[34m' CYN=$'\e[36m'
else
  B="" D="" R="" RED="" GRN="" YLW="" BLU="" CYN=""
fi
info() { printf '  %s\n' "$*"; }
ok() { printf '  %s✓%s %s\n' "$GRN" "$R" "$*"; }
warn() { printf '  %s!%s %s\n' "$YLW" "$R" "$*"; }
err() { printf '  %s✗ %s%s\n' "$RED" "$*" "$R" >&2; }
die() { err "$*"; exit 1; }
step() { printf '  %s▸%s %s\n' "$BLU" "$R" "$*"; }
rule() { printf '  %s%s%s\n' "$D" "────────────────────────────────────────────────────" "$R"; }
title() { printf '\n  %s%s%s\n' "$B" "$*" "$R"; rule; }
have() { command -v "$1" >/dev/null 2>&1; }

TMPS=()
cleanup() { [ ${#TMPS[@]} -gt 0 ] && rm -rf "${TMPS[@]}"; return 0; }
trap cleanup EXIT
# work_dir: a scratch directory removed on exit. It lives under $LIB, not
# /tmp, because the downloaded program is run there and /tmp may be noexec.
work_dir() {
  install -d -m 755 "$LIB"
  local d
  d="$(mktemp -d "$LIB/tmp.XXXXXX")"
  TMPS+=("$d")
  echo "$d"
}

banner() {
  printf '\n'
  printf '  %s╭─────────╮%s\n' "$BLU" "$R"
  printf '  %s│  ▶ ▶ ▶  │%s  %sGPanel%s  游戏服务器管理面板\n' "$BLU" "$R" "$B" "$R"
  printf '  %s╰─────────╯%s  %s安装与维护工具 · github.com/%s%s\n' "$BLU" "$R" "$D" "$REPO" "$R"
}

# Prompts read from the terminal even when the script itself arrives on stdin
# (curl ... | bash). Without a terminal, defaults are used.
tty_ok() { [ -r /dev/tty ] && { : </dev/tty; } 2>/dev/null; }
ask() { # ask "提示" default -> echo answer
  local ans=""
  if [ "$YES" = 1 ] || ! tty_ok; then
    echo "$2"
    return
  fi
  printf '  %s?%s %s %s[%s]%s ' "$CYN" "$R" "$1" "$D" "$2" "$R" >/dev/tty
  read -r ans </dev/tty || true
  echo "${ans:-$2}"
}
confirm() { # confirm "问题" [y|n] -> status
  local def="${2:-n}" hint ans
  [ "$YES" = 1 ] && return 0
  if ! tty_ok; then
    [ "$def" = y ]
    return
  fi
  hint="y/N"
  [ "$def" = y ] && hint="Y/n"
  printf '  %s?%s %s %s[%s]%s ' "$CYN" "$R" "$1" "$D" "$hint" "$R" >/dev/tty
  read -r ans </dev/tty || true
  ans="${ans:-$def}"
  [[ "$ans" =~ ^[Yy] ]]
}
pause() { tty_ok && [ "$YES" = 0 ] && { printf '\n  %s按回车返回菜单…%s' "$D" "$R" >/dev/tty; read -r _ </dev/tty || true; }; return 0; }

# ---------------------------------------------------------------- checks

need_root() { [ "$(id -u)" = 0 ] || die "这个操作需要 root 权限，请用 sudo 运行"; }

check_system() {
  [ "$(uname -s)" = Linux ] || die "只支持 Linux"
  have systemctl && [ -d /run/systemd/system ] || die "需要 systemd"
  have curl || have wget || die "需要 curl 或 wget"
  have sha256sum || die "需要 sha256sum（coreutils）"
  ARCH="$(detect_arch)"
}

detect_arch() {
  case "$(uname -m)" in
    x86_64 | amd64) echo amd64 ;;
    aarch64 | arm64) echo arm64 ;;
    *) die "不支持的 CPU 架构：$(uname -m)（支持 x86_64 / arm64）" ;;
  esac
}

installed() { [ -x "$BIN" ] && [ -f "$UNIT" ]; }
current_version() { "$BIN" -version 2>/dev/null || echo "未知"; }
running() { systemctl is-active --quiet "$SVC"; }
# systemd refuses a unit started more than 5 times in 10s (several updates or
# rollbacks in a row, or a crash loop); an explicit start clears that first.
svc_start() { systemctl reset-failed "$SVC" >/dev/null 2>&1 || true; systemctl "$1" "$SVC"; }

# ---------------------------------------------------------------- config files

load_ctl_conf() {
  # shellcheck disable=SC1090
  [ -f "$CTL_CONF" ] && . "$CTL_CONF"
  MIRROR="${GPANEL_MIRROR:-${MIRROR:-}}"
}
save_ctl_conf() {
  install -d -m 755 "$ETC"
  printf '# gpanel-ctl 设置\n# GitHub 下载加速前缀，例如 https://example-proxy.com/ （留空直连）\nMIRROR=%q\n' "$MIRROR" >"$CTL_CONF"
}

# env_get KEY: value from the env file; empty if unset or unreadable (non-root).
env_get() { { sed -n "s/^$1=//p" "$ENV_FILE" | tail -1; } 2>/dev/null || true; }
env_set() { # env_set KEY VALUE  (empty VALUE comments the key out)
  local key=$1 val=$2 tmp
  tmp="$(mktemp)"
  TMPS+=("$tmp")
  if [ -n "$val" ]; then
    awk -v k="$key" -v v="$val" 'BEGIN{done=0} $0 ~ "^#?"k"=" {if(!done){print k"="v; done=1}; next} {print} END{if(!done) print k"="v}' "$ENV_FILE" >"$tmp"
  else
    awk -v k="$key" '$0 ~ "^"k"=" {print "#"$0; next} {print}' "$ENV_FILE" >"$tmp"
  fi
  cat "$tmp" >"$ENV_FILE" # keep owner and mode
}

addr() { local a; a="$(env_get GPANEL_ADDR)"; echo "${a:-127.0.0.1:$DEFAULT_PORT}"; }
port_of() { echo "${1##*:}"; }

# Global IPv4 addresses, skipping container/VM bridges (docker0, br-…, veth…)
# that nobody browses to; all of them if that leaves none.
host_ips() {
  local all real
  if have ip; then
    all="$(ip -4 -o addr show scope global 2>/dev/null | awk '{sub(/@.*/, "", $2); sub(/\/.*/, "", $4); print $2, $4}')"
    real="$(awk '$1 !~ /^(docker|br-|veth|virbr|cni|flannel|cali|vxlan|lxcbr|lxdbr|podman|kube-)/' <<<"$all")"
    awk '{print $2}' <<<"${real:-$all}"
  else
    hostname -I 2>/dev/null | tr ' ' '\n' | grep -v ':' || true
  fi
}

# URLs to show: the host's addresses when listening on all interfaces.
panel_urls() {
  local a host port scheme=http ip
  a="$(addr)" host="${a%:*}" port="$(port_of "$a")"
  [ -f "$DATA/tls/settings.json" ] && grep -q '"enabled": *true' "$DATA/tls/settings.json" && scheme="http(s)"
  if [ "$host" = "0.0.0.0" ] || [ -z "$host" ] || [ "$host" = "[::]" ]; then
    for ip in $(host_ips); do
      echo "$scheme://$ip:$port"
    done
  else
    echo "$scheme://$host:$port"
  fi
}

# ---------------------------------------------------------------- downloads

fetch() { # fetch URL DEST [quiet]
  local url=$1 dest=$2 quiet=${3:-} hdr=()
  if [ -n "$TOKEN" ] && [[ "$url" == https://api.github.com/* ]]; then
    hdr=(-H "Authorization: Bearer $TOKEN" -H "Accept: application/octet-stream")
  fi
  if have curl; then
    local prog=(--progress-bar)
    { [ -n "$quiet" ] || [ ! -t 2 ]; } && prog=(-s)
    curl -fL --retry 3 --connect-timeout 15 "${prog[@]}" "${hdr[@]}" -o "$dest" "$url"
  else
    local w=(--tries=3 --timeout=30)
    [ -n "$quiet" ] && w+=(-q)
    [ ${#hdr[@]} -gt 0 ] && w+=(--header="${hdr[1]}" --header="${hdr[3]}")
    wget "${w[@]}" -O "$dest" "$url"
  fi
}

latest_version() {
  local v=""
  if [ -n "$TOKEN" ]; then
    v="$(curl -fsS --connect-timeout 5 --max-time 10 -H "Authorization: Bearer $TOKEN" "$API/releases/latest" 2>/dev/null |
      sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)" || true
  else
    # The /latest page redirects to /tag/<version>; no API rate limit.
    v="$(curl -fsSIL -o /dev/null -w '%{url_effective}' --connect-timeout 5 --max-time 10 "$RELEASES/latest" 2>/dev/null |
      sed -n 's#.*/tag/\([^/?]*\).*#\1#p')" || true
  fi
  echo "$v"
}

# asset_url VERSION NAME: download URL of a release file.
asset_url() {
  local ver=$1 name=$2
  if [ -n "$TOKEN" ]; then
    # Private repositories: assets are only reachable through the API.
    curl -fsS -H "Authorization: Bearer $TOKEN" "$API/releases/tags/$ver" |
      awk -v n="$name" '/"url": *"https:\/\/api.github.com\/repos\/.*\/releases\/assets\// {gsub(/[",]/,"",$2); u=$2} $0 ~ "\"name\": *\""n"\"" {print u; exit}'
  else
    echo "${MIRROR}$RELEASES/download/$ver/$name"
  fi
}

# get_release VERSION DIR: downloads the binary (and install.sh) into DIR and
# verifies them against SHA256SUMS.
get_release() {
  local ver=$1 dir=$2 name="gpanel-linux-$ARCH" url
  step "下载 GPanel $ver（linux/$ARCH）"
  url="$(asset_url "$ver" "$name")"
  [ -n "$url" ] || die "版本 $ver 中没有 $name"
  fetch "$url" "$dir/gpanel" || die "下载失败：$url"
  fetch "$(asset_url "$ver" SHA256SUMS)" "$dir/SHA256SUMS" quiet || die "下载校验文件失败"
  fetch "$(asset_url "$ver" install.sh)" "$dir/install.sh" quiet || warn "未能下载该版本的维护脚本，继续使用当前脚本"
  local want got
  want="$(awk -v n="$name" '$2 == n || $2 == "*"n {print $1}' "$dir/SHA256SUMS")"
  got="$(sha256sum "$dir/gpanel" | awk '{print $1}')"
  [ -n "$want" ] && [ "$want" = "$got" ] || die "SHA-256 校验失败，文件可能损坏或被篡改（期望 ${want:-无}，实际 $got）"
  if [ -s "$dir/install.sh" ]; then
    want="$(awk '$2 == "install.sh" || $2 == "*install.sh" {print $1}' "$dir/SHA256SUMS")"
    [ "$want" = "$(sha256sum "$dir/install.sh" | awk '{print $1}')" ] || { warn "维护脚本校验失败，不更新脚本"; rm -f "$dir/install.sh"; }
  fi
  chmod +x "$dir/gpanel"
  ok "下载完成，SHA-256 校验通过"
}

# obtain_binary DIR: --local FILE, or the requested / latest release.
obtain_binary() {
  local dir=$1
  if [ -n "$LOCAL" ]; then
    [ -f "$LOCAL" ] || die "找不到文件：$LOCAL"
    install -m 755 "$LOCAL" "$dir/gpanel"
    step "使用本地文件 $LOCAL"
  else
    local ver="$WANT_VERSION"
    if [ -z "$ver" ]; then
      step "查询最新版本"
      ver="$(latest_version)"
      [ -n "$ver" ] || die "无法获取最新版本，请检查网络，或用 --version 指定版本"
    fi
    get_release "$ver" "$dir"
  fi
  "$dir/gpanel" -version >/dev/null 2>&1 || die "下载的程序无法在本机运行（架构或系统不匹配？）"
}

# install_ctl FILE: installs the maintenance script as gpanel-ctl.
install_ctl() {
  local src=$1
  [ -s "$src" ] || return 0
  bash -n "$src" 2>/dev/null || { warn "维护脚本语法检查未通过，保留当前版本"; return 0; }
  install -m 755 "$src" "$CTL.new" && mv -f "$CTL.new" "$CTL"
}

# The script's own file, if it is one (not when piped through bash).
self_file() {
  local f="${BASH_SOURCE[0]:-}"
  if [ -n "$f" ] && [ -f "$f" ] && [[ "$f" != /dev/* ]] && [[ "$f" != /proc/* ]]; then echo "$f"; fi
}

# ---------------------------------------------------------------- service

write_unit() { # from the binary itself, so it always matches the version
  "$BIN" -print-systemd-unit >"$UNIT.new" && mv -f "$UNIT.new" "$UNIT"
  chmod 644 "$UNIT"
  systemctl daemon-reload
}

# health: waits for the service to stay up and answer HTTP.
health() {
  local a host port i code
  a="$(addr)" host="${a%:*}" port="$(port_of "$a")"
  case "$host" in "" | 0.0.0.0 | "[::]") host=127.0.0.1 ;; esac
  for ((i = 0; i < 40; i++)); do
    if running; then
      code="$(curl -sk -o /dev/null -w '%{http_code}' --max-time 2 "http://$host:$port/" 2>/dev/null || true)"
      [ "$code" != "000" ] && [ -n "$code" ] && { sleep 1; running && return 0; }
    fi
    sleep 0.5
  done
  return 1
}

initial_password() {
  local i line=""
  for ((i = 0; i < 30; i++)); do
    line="$(journalctl -u "$SVC" --no-pager -o cat 2>/dev/null | grep -o '初始密码 [0-9a-f]*' | tail -1 || true)"
    [ -n "$line" ] && break
    sleep 0.5
  done
  echo "${line#初始密码 }"
}

restart_notice() {
  warn "重启面板会先优雅停止正在运行的游戏服务器；勾选了「自动启动」的实例会在面板启动后恢复。"
}

open_firewall() {
  local port=$1
  if have ufw && ufw status 2>/dev/null | grep -q '^Status: active'; then
    confirm "检测到 ufw 防火墙，放行 TCP $port 端口？" y && ufw allow "$port/tcp" >/dev/null && ok "ufw 已放行 $port/tcp"
  elif have firewall-cmd && firewall-cmd --state >/dev/null 2>&1; then
    confirm "检测到 firewalld 防火墙，放行 TCP $port 端口？" y &&
      firewall-cmd --permanent --add-port="$port/tcp" >/dev/null && firewall-cmd --reload >/dev/null && ok "firewalld 已放行 $port/tcp"
  fi
  return 0
}

port_busy() { have ss && ss -Hltn "sport = :$1" 2>/dev/null | grep -q .; }

# ---------------------------------------------------------------- commands

cmd_install() {
  need_root
  check_system
  if installed; then
    warn "GPanel 已安装（$(current_version)）。如需升级请选择「更新面板」。"
    return 0
  fi
  title "安装 GPanel"
  local port bind choice java tmp
  port="${OPT_PORT:-$(ask "面板端口" "$DEFAULT_PORT")}"
  [[ "$port" =~ ^[0-9]+$ ]] && [ "$port" -ge 1 ] && [ "$port" -le 65535 ] || die "端口不正确：$port"
  port_busy "$port" && die "端口 $port 已被占用，请换一个"
  if [ -n "$OPT_BIND" ]; then
    bind="$OPT_BIND"
  else
    info "访问方式："
    info "  1) 允许从外网访问（监听 0.0.0.0）"
    info "  2) 只允许本机访问（前面另有 Nginx / Caddy 反向代理时选这个）"
    choice="$(ask "请选择" 1)"
    bind=0.0.0.0
    [ "$choice" = 2 ] && bind=127.0.0.1
  fi
  java=n
  if [ "$(cat /etc/timezone 2>/dev/null || timedatectl show -p Timezone --value 2>/dev/null || true)" = Asia/Shanghai ]; then java=y; fi
  if [ "$YES" = 0 ] && confirm "从清华镜像下载 Java（中国大陆服务器推荐）？" "$java"; then java=y; else [ "$YES" = 1 ] || java=n; fi
  load_ctl_conf
  if [ -z "$LOCAL" ] && [ -z "$TOKEN" ] && [ "$YES" = 0 ] && [ -z "$MIRROR" ]; then
    MIRROR="$(ask "GitHub 下载加速前缀（可选，直接回车跳过）" "")"
  fi
  rule

  tmp="$(work_dir)"
  obtain_binary "$tmp"

  step "创建系统用户和目录"
  id gpanel >/dev/null 2>&1 || useradd --system --home-dir "$DATA" --shell /usr/sbin/nologin gpanel
  install -d -m 755 "$ETC" "$LIB"
  install -m 755 "$tmp/gpanel" "$BIN"
  write_unit
  if [ ! -f "$ENV_FILE" ]; then
    "$BIN" -print-env-template >"$ENV_FILE"
    chgrp gpanel "$ENV_FILE" && chmod 640 "$ENV_FILE"
  fi
  env_set GPANEL_ADDR "$bind:$port"
  [ "$java" = y ] && env_set GPANEL_JAVA_MIRROR "$TUNA"
  save_ctl_conf
  if [ -s "$tmp/install.sh" ]; then install_ctl "$tmp/install.sh"; elif [ -n "$(self_file)" ]; then install_ctl "$(self_file)"; fi
  [ -x "$CTL" ] || warn "未能安装 gpanel-ctl 维护命令（请从 Releases 下载 install.sh 后运行 sudo bash install.sh）"
  ok "已安装 $(current_version) 到 $BIN"

  step "启动服务"
  systemctl enable "$SVC" >/dev/null 2>&1
  svc_start start
  health || { journalctl -u "$SVC" --no-pager -n 20 >&2; die "面板没有正常启动，以上是最近的日志"; }
  ok "服务已启动"
  [ "$bind" = 0.0.0.0 ] && open_firewall "$port"

  local pw
  pw="$(initial_password)"
  printf '\n'
  rule
  printf '  %s🎉 安装完成%s\n\n' "$GRN$B" "$R"
  local u
  while read -r u; do [ -n "$u" ] && printf '  访问地址   %s%s%s\n' "$B" "$u" "$R"; done < <(panel_urls)
  printf '  用户名     %sadmin%s\n' "$B" "$R"
  printf '  初始密码   %s%s%s\n' "$B$YLW" "${pw:-（未能读取，请运行 sudo gpanel-ctl reset-password）}" "$R"
  printf '\n  %s登录后请在「安全」页修改密码；对公网开放时建议在同一页配置 HTTPS。%s\n' "$D" "$R"
  printf '  %s以后运行 %ssudo gpanel-ctl%s%s 打开维护菜单（更新、重置密码、卸载等）。%s\n' "$D" "$R$B" "$R" "$D" "$R"
  rule
}

cmd_update() {
  need_root
  check_system
  installed || die "GPanel 还没有安装"
  load_ctl_conf
  title "更新 GPanel"
  local cur target tmp
  cur="$(current_version)"
  if [ -z "$LOCAL" ]; then
    target="${WANT_VERSION:-}"
    if [ -z "$target" ]; then
      step "查询最新版本"
      target="$(latest_version)"
      [ -n "$target" ] || die "无法获取最新版本，请检查网络，或用 --version 指定版本"
    fi
    info "当前版本 $B$cur$R  →  目标版本 $B$target$R"
    if [ "$cur" = "$target" ] && [ "$FORCE" = 0 ]; then
      ok "已经是最新版本"
      update_ctl_only "$target"
      return 0
    fi
    WANT_VERSION="$target"
  fi
  restart_notice
  confirm "开始更新？" y || { info "已取消"; return 0; }

  tmp="$(work_dir)"
  obtain_binary "$tmp"
  local new
  new="$("$tmp/gpanel" -version)"

  # The running version is kept aside and only becomes "the previous
  # version" once the new one is up, so a failed update can't clobber it.
  step "备份当前版本"
  cp -f "$BIN" "$tmp/gpanel.cur"
  cp -f "$UNIT" "$tmp/gpanel.service.cur"

  step "替换程序并重启"
  install -m 755 "$tmp/gpanel" "$BIN.new" && mv -f "$BIN.new" "$BIN"
  write_unit
  svc_start restart
  if health; then
    install -m 755 "$tmp/gpanel.cur" "$LIB/gpanel.prev"
    install -m 644 "$tmp/gpanel.service.cur" "$LIB/gpanel.service.prev"
    ok "已更新到 $new"
    [ -s "$tmp/install.sh" ] && install_ctl "$tmp/install.sh" && ok "维护脚本已同步更新"
    info "如有问题，可以在菜单中选择「回滚到上一版本」。"
  else
    err "新版本没有正常启动，正在自动回滚到 $cur"
    journalctl -u "$SVC" --no-pager -n 15 >&2 || true
    restore_from "$tmp/gpanel.cur" "$tmp/gpanel.service.cur"
    health && ok "已回滚到 $(current_version)，面板正常运行" || die "回滚后面板仍未正常启动，请查看日志：journalctl -u gpanel -n 100"
    return 1
  fi
}

update_ctl_only() {
  [ -n "$LOCAL" ] && return 0
  local tmp
  tmp="$(work_dir)"
  if fetch "$(asset_url "$1" install.sh)" "$tmp/install.sh" quiet 2>/dev/null &&
    fetch "$(asset_url "$1" SHA256SUMS)" "$tmp/SHA256SUMS" quiet 2>/dev/null &&
    [ "$(awk '$2 == "install.sh" {print $1}' "$tmp/SHA256SUMS")" = "$(sha256sum "$tmp/install.sh" | awk '{print $1}')" ] &&
    ! cmp -s "$tmp/install.sh" "$CTL"; then
    install_ctl "$tmp/install.sh" && ok "维护脚本已更新到 $1 附带的版本"
  fi
  return 0
}

# restore_from BINARY UNIT: puts a saved version back in place and restarts.
restore_from() {
  install -m 755 "$1" "$BIN.new" && mv -f "$BIN.new" "$BIN"
  if [ -f "$2" ]; then install -m 644 "$2" "$UNIT"; fi
  systemctl daemon-reload
  svc_start restart
}

cmd_rollback() {
  need_root
  installed || die "GPanel 还没有安装"
  [ -f "$LIB/gpanel.prev" ] || die "没有可回滚的旧版本（只有更新过才会保留上一版本）"
  title "回滚到上一版本"
  info "当前 $B$(current_version)$R  →  上一版本 $B$("$LIB/gpanel.prev" -version 2>/dev/null || echo 未知)$R"
  restart_notice
  confirm "确定回滚？" n || { info "已取消"; return 0; }
  local tmp
  tmp="$(work_dir)"
  cp -f "$BIN" "$tmp/gpanel"
  cp -f "$UNIT" "$tmp/gpanel.service"
  restore_from "$LIB/gpanel.prev" "$LIB/gpanel.service.prev"
  # Swap, so rolling back again returns to the version just left.
  install -m 755 "$tmp/gpanel" "$LIB/gpanel.prev"
  install -m 644 "$tmp/gpanel.service" "$LIB/gpanel.service.prev"
  health && ok "已回滚到 $(current_version)" || die "回滚后面板没有正常启动，请查看日志：journalctl -u gpanel -n 100"
}

cmd_uninstall() {
  need_root
  installed || [ -d "$DATA" ] || die "GPanel 没有安装"
  title "卸载 GPanel"
  warn "卸载会停止面板和所有正在运行的游戏服务器。"
  confirm "确定卸载？" n || { info "已取消"; return 0; }
  local purge="$PURGE"
  if [ "$purge" = 0 ] && [ "$YES" = 0 ] && [ -d "$DATA" ]; then
    info "数据目录 $DATA 中有实例、世界存档、证书和账号（$(du -sh "$DATA" 2>/dev/null | awk '{print $1}')）。"
    if confirm "同时删除所有数据？此操作无法恢复" n; then
      [ "$(ask "请输入 DELETE 确认删除数据" "")" = DELETE ] && purge=1 || info "未确认，保留数据"
    fi
  fi
  step "停止并移除服务"
  systemctl disable --now "$SVC" >/dev/null 2>&1 || true
  rm -f "$UNIT" "$BIN" "$LIB/gpanel.prev" "$LIB/gpanel.service.prev"
  rmdir "$LIB" 2>/dev/null || true
  systemctl daemon-reload
  if [ "$purge" = 1 ]; then
    step "删除数据、配置和系统用户"
    rm -rf "$DATA" "$ETC"
    userdel gpanel 2>/dev/null || true
    ok "已彻底卸载"
  else
    ok "已卸载程序；数据保留在 $DATA，配置保留在 $ETC，重新安装后可继续使用"
  fi
  rm -f "$CTL"
}

cmd_reset_password() {
  need_root
  installed || die "GPanel 还没有安装"
  local user="${OPT_USER:-}" pw
  [ -n "$user" ] || user="$(ask "要重置密码的账号" admin)"
  pw="$(runuser -u gpanel -- "$BIN" -data "$DATA" -reset-password -user "$user" 2>&1)" || die "重置失败：$pw"
  printf '\n  %s新密码%s  %s%s%s   （账号 %s）\n' "$B" "$R" "$B$YLW" "$pw" "$R" "$user"
  info "${D}已生效并退出了所有登录会话，无需重启面板；登录后可在「安全」页修改。$R"
}

cmd_status() {
  installed || { warn "GPanel 没有安装"; return 0; }
  title "运行状态"
  local state since
  if running; then state="$GRN● 运行中$R"; else state="$RED● 未运行$R"; fi
  since="$(systemctl show -p ActiveEnterTimestamp --value "$SVC" 2>/dev/null || true)"
  printf '  %-8s %s %s\n' "状态" "$state" "${since:+$D（自 $since）$R}"
  printf '  %-8s %s\n' "版本" "$(current_version)"
  printf '  %-8s %s\n' "监听" "$(addr)"
  local u
  while read -r u; do [ -n "$u" ] && printf '  %-8s %s\n' "地址" "$u"; done < <(panel_urls)
  printf '  %-8s %s %s\n' "数据" "$DATA" "$D$(df -h "$DATA" 2>/dev/null | awk 'NR==2 {print "（磁盘剩余 "$4"）"}')$R"
  [ "$(env_get GPANEL_DISABLE_TLS)" = 1 ] && warn "HTTPS 已被临时关闭（救急模式），处理完后请在「修改配置」中恢复"
  printf '\n'
  systemctl status "$SVC" --no-pager -n 8 2>/dev/null | sed 's/^/  /' || true
}

cmd_logs() {
  installed || die "GPanel 还没有安装"
  info "${D}显示最近日志并持续跟随，按 Ctrl+C 返回$R"
  trap ':' INT
  journalctl -u "$SVC" -n 80 -f --no-pager -o short-iso || true
  trap - INT
}

cmd_service() { # start|stop|restart
  need_root
  installed || die "GPanel 还没有安装"
  case "$1" in
    start) svc_start start && health && ok "已启动" || die "启动失败，请查看日志" ;;
    stop)
      warn "停止面板会同时停止所有正在运行的游戏服务器。"
      confirm "确定停止？" n || return 0
      systemctl stop "$SVC" && ok "已停止"
      ;;
    restart)
      restart_notice
      confirm "确定重启？" y || return 0
      svc_start restart && health && ok "已重启" || die "重启后面板没有正常运行，请查看日志"
      ;;
  esac
}

apply_env_change() {
  ok "配置已保存到 $ENV_FILE"
  if running; then
    restart_notice
    if confirm "现在重启面板使配置生效？" y; then
      svc_start restart
      health && ok "已重启" || err "重启后面板没有正常运行，请查看日志"
    else
      info "稍后重启面板后生效"
    fi
  fi
  return 0
}

cmd_config() {
  need_root
  installed || die "GPanel 还没有安装"
  load_ctl_conf
  while true; do
    title "修改配置"
    local a mirror_on tls_off
    a="$(addr)"
    mirror_on="$([ -n "$(env_get GPANEL_JAVA_MIRROR)" ] && echo "清华镜像" || echo "官方源")"
    tls_off="$([ "$(env_get GPANEL_DISABLE_TLS)" = 1 ] && echo "${YLW}已临时关闭$R" || echo "正常")"
    printf '  %s1%s  监听地址与端口      %s%s%s\n' "$B" "$R" "$D" "$a" "$R"
    printf '  %s2%s  Java 下载源         %s%s%s\n' "$B" "$R" "$D" "$mirror_on" "$R"
    printf '  %s3%s  HTTPS 救急开关      %s\n' "$B" "$R" "$tls_off"
    printf '  %s4%s  GitHub 下载加速前缀 %s%s%s\n' "$B" "$R" "$D" "${MIRROR:-未设置}" "$R"
    printf '  %s0%s  返回\n' "$B" "$R"
    case "$(ask "请选择" 0)" in
      1)
        local port bind
        port="$(ask "端口" "$(port_of "$a")")"
        [[ "$port" =~ ^[0-9]+$ ]] && [ "$port" -ge 1 ] && [ "$port" -le 65535 ] || { err "端口不正确"; continue; }
        bind="$(ask "监听地址（0.0.0.0 = 允许外网，127.0.0.1 = 仅本机）" "${a%:*}")"
        [ "$bind:$port" = "$a" ] && continue
        [ "$port" != "$(port_of "$a")" ] && port_busy "$port" && { err "端口 $port 已被占用"; continue; }
        env_set GPANEL_ADDR "$bind:$port"
        [ "$bind" = 0.0.0.0 ] && open_firewall "$port"
        apply_env_change
        ;;
      2)
        if [ -n "$(env_get GPANEL_JAVA_MIRROR)" ]; then env_set GPANEL_JAVA_MIRROR ""; else env_set GPANEL_JAVA_MIRROR "$TUNA"; fi
        apply_env_change
        ;;
      3)
        if [ "$(env_get GPANEL_DISABLE_TLS)" = 1 ]; then
          env_set GPANEL_DISABLE_TLS ""
          info "将恢复面板里保存的 HTTPS 设置。"
        else
          info "临时只用 HTTP 访问面板（证书或「强制 HTTPS」导致打不开时使用），面板里的 HTTPS 设置会保留。"
          env_set GPANEL_DISABLE_TLS 1
        fi
        apply_env_change
        ;;
      4)
        MIRROR="$(ask "加速前缀（形如 https://xxx/，输入 - 清除）" "${MIRROR:-}")"
        [ "$MIRROR" = - ] && MIRROR=""
        save_ctl_conf && ok "已保存"
        ;;
      *) return 0 ;;
    esac
  done
}

cmd_backup() {
  need_root
  [ -d "$DATA" ] || die "没有找到数据目录 $DATA"
  title "备份数据"
  local out="${OPT_OUT:-/root/gpanel-backup-$(date +%Y%m%d-%H%M%S).tar.gz}"
  info "备份 $DATA 和 $ETC（不含可重新下载的 Java 运行时）到："
  info "$B$out$R"
  running && warn "面板运行中：正在写入的世界存档可能不完整，重要备份建议先在面板中停止实例。"
  confirm "开始备份？" y || return 0
  step "打包中…"
  tar -czf "$out" --exclude="${DATA#/}/runtimes" -C / "${DATA#/}" "${ETC#/}" 2>/dev/null || die "备份失败"
  chmod 600 "$out"
  ok "备份完成：$out（$(du -h "$out" | awk '{print $1}')）"
}

# ---------------------------------------------------------------- menu

LATEST=""
menu_header() {
  clear 2>/dev/null || true
  banner
  rule
  if installed; then
    local cur state u
    cur="$(current_version)"
    if running; then state="$GRN● 运行中$R"; else state="$RED● 未运行$R"; fi
    printf '  状态  %s    版本  %s%s%s' "$state" "$B" "$cur" "$R"
    if [ -n "$LATEST" ] && [ "$LATEST" != "$cur" ]; then printf '  %s（可更新到 %s）%s' "$YLW" "$LATEST" "$R"; fi
    printf '\n'
    u="$(panel_urls | head -1)"
    [ -n "$u" ] && printf '  地址  %s\n' "$u"
  else
    printf '  状态  %s未安装%s\n' "$D" "$R"
  fi
  rule
}

item() { printf '   %s%2s%s  %s\n' "$B" "$1" "$R" "$2"; }
group() { printf '\n  %s%s%s\n' "$D" "$1" "$R"; }

menu() {
  [ "$(id -u)" = 0 ] || NOTROOT=1
  if installed && have curl; then LATEST="$(latest_version)"; fi
  while true; do
    menu_header
    [ "${NOTROOT:-0}" = 1 ] && warn "当前不是 root，只能查看状态；修改操作请用 sudo 运行"
    if ! installed; then
      item 1 "安装面板"
      item 0 "退出"
      case "$(ask "请选择" 1)" in
        1) run_action cmd_install ;;
        *) printf '\n'; exit 0 ;;
      esac
      continue
    fi
    group "更新"
    item 1 "更新面板$([ -n "$LATEST" ] && [ "$LATEST" != "$(current_version)" ] && printf '  %s（新版本 %s）%s' "$YLW" "$LATEST" "$R")"
    item 2 "回滚到上一版本"
    group "运行"
    item 3 "启动 / 停止 / 重启"
    item 4 "查看状态"
    item 5 "查看日志"
    group "维护"
    item 6 "重置管理员密码"
    item 7 "修改配置（端口 / Java 下载源 / HTTPS 救急）"
    item 8 "备份数据"
    item 9 "卸载面板"
    item 0 "退出"
    case "$(ask "请选择" 0)" in
      1) run_action cmd_update ;;
      2) run_action cmd_rollback ;;
      3)
        title "启动 / 停止 / 重启"
        item 1 "启动"
        item 2 "停止"
        item 3 "重启"
        item 0 "返回"
        case "$(ask "请选择" 0)" in
          1) run_action cmd_service start ;;
          2) run_action cmd_service stop ;;
          3) run_action cmd_service restart ;;
        esac
        ;;
      4) run_action cmd_status ;;
      5) cmd_logs ;;
      6) run_action cmd_reset_password ;;
      7) run_action cmd_config ;;
      8) run_action cmd_backup ;;
      9) run_action cmd_uninstall && ! installed && { printf '\n'; exit 0; } ;;
      *) printf '\n'; exit 0 ;;
    esac
  done
}

# Runs an action in a subshell so a failure ("die", or any failing command)
# ends just that action and returns to the menu. errexit must stay on inside
# it, which is why the subshell isn't part of an "||" list.
run_action() {
  set +e
  (
    set -e
    "$@"
  )
  set -e
  pause
}

usage() {
  cat <<EOF

  ${B}用法${R}  sudo gpanel-ctl [命令] [选项]      不带命令时打开交互菜单

  ${B}命令${R}
    install            安装面板（默认安装最新版本）
    update             更新到最新版本（自动备份，启动失败自动回滚）
    rollback           回滚到更新前的版本
    uninstall          卸载（默认保留数据，加 --purge 删除数据）
    status             查看运行状态
    start|stop|restart 启动 / 停止 / 重启服务
    logs               查看并跟随日志
    reset-password     重置密码（--user 指定账号，默认 admin）
    config             修改端口、Java 下载源、HTTPS 救急开关
    backup             备份数据目录和配置
    version            显示已安装版本和最新版本

  ${B}选项${R}
    -y, --yes              跳过确认，全部使用默认值
    --version VERSION      安装 / 更新到指定版本（如 v0.3.0）
    --local FILE           使用本地的 gpanel 程序文件，不下载
    --port N               安装时的面板端口（默认 $DEFAULT_PORT）
    --bind ADDR            安装时的监听地址（0.0.0.0 或 127.0.0.1）
    --mirror PREFIX        GitHub 下载加速前缀
    --user NAME            reset-password 的账号
    --purge                uninstall 时同时删除所有数据
    --force                已是最新版本时也重新安装

  ${B}环境变量${R}
    GPANEL_GITHUB_TOKEN    从私有 fork 安装时用于下载的 GitHub Token
    GPANEL_MIRROR          同 --mirror

EOF
}

main() {
  local cmd="" args=()
  while [ $# -gt 0 ]; do
    case "$1" in
      -y | --yes) YES=1 ;;
      --purge) PURGE=1 ;;
      --force) FORCE=1 ;;
      --local) LOCAL="${2:?--local 需要文件路径}"; shift ;;
      --version) WANT_VERSION="${2:?--version 需要版本号}"; shift ;;
      --port) OPT_PORT="${2:?}"; shift ;;
      --bind) OPT_BIND="${2:?}"; shift ;;
      --mirror) MIRROR="${2:?}"; shift ;;
      --user) OPT_USER="${2:?}"; shift ;;
      --out) OPT_OUT="${2:?}"; shift ;;
      -h | --help | help) usage; exit 0 ;;
      -*) die "未知选项：$1（运行 gpanel-ctl help 查看用法）" ;;
      *) args+=("$1") ;;
    esac
    shift
  done
  cmd="${args[0]:-}"
  [ -n "$MIRROR" ] && [[ "$MIRROR" != */ ]] && MIRROR="$MIRROR/"
  GPANEL_MIRROR="${MIRROR:-${GPANEL_MIRROR:-}}"
  ARCH="${ARCH:-}"

  case "$cmd" in
    "")
      # First run through "curl | bash" with nothing installed: go straight to installing.
      if [ -z "$(self_file)" ] && ! installed && ! tty_ok; then banner; cmd_install; else menu; fi
      ;;
    install) banner; cmd_install ;;
    update) cmd_update ;;
    rollback) cmd_rollback ;;
    uninstall) cmd_uninstall ;;
    status) cmd_status ;;
    start | stop | restart) cmd_service "$cmd" ;;
    logs) cmd_logs ;;
    reset-password) cmd_reset_password ;;
    config) cmd_config ;;
    backup) cmd_backup ;;
    version)
      installed && echo "已安装：$(current_version)" || echo "未安装"
      echo "最新版本：$(latest_version || true)"
      ;;
    *) die "未知命令：$cmd（运行 gpanel-ctl help 查看用法）" ;;
  esac
}

main "$@"
