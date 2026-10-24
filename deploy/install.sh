#!/bin/bash
# Install or upgrade GPanel as a systemd service.
#
#   sudo deploy/install.sh ./gpanel-linux-amd64
#
# Creates the "gpanel" system user, installs the binary to /usr/local/bin,
# the unit to /etc/systemd/system and (first time only) /etc/gpanel/gpanel.env.
# Data lives in /var/lib/gpanel. Upgrading restarts the panel, which stops
# running game servers; instances with auto-start come back up.
set -euo pipefail

BIN="${1:?用法: sudo $0 <gpanel 二进制>}"
HERE="$(cd "$(dirname "$0")" && pwd)"

[ "$(id -u)" = 0 ] || { echo "请用 root 运行（sudo）" >&2; exit 1; }
[ -f "$BIN" ] || { echo "找不到二进制: $BIN" >&2; exit 1; }
command -v systemctl >/dev/null || { echo "需要 systemd" >&2; exit 1; }

if ! id gpanel >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/gpanel --shell /usr/sbin/nologin gpanel
  echo "已创建系统用户 gpanel"
fi

install -m 755 "$BIN" /usr/local/bin/gpanel
install -m 644 "$HERE/gpanel.service" /etc/systemd/system/gpanel.service
if [ ! -f /etc/gpanel/gpanel.env ]; then
  install -d -m 755 /etc/gpanel
  install -m 640 -g gpanel "$HERE/gpanel.env" /etc/gpanel/gpanel.env
  echo "已写入 /etc/gpanel/gpanel.env"
fi
systemctl daemon-reload

first_run=0
[ -f /var/lib/gpanel/users.json ] || first_run=1

if systemctl is-active --quiet gpanel; then
  systemctl restart gpanel
  echo "已升级并重启 gpanel"
else
  systemctl enable --now gpanel
  echo "已安装并启动 gpanel"
fi

if [ "$first_run" = 1 ]; then
  # The admin account is created on first start; show its password.
  for _ in $(seq 1 20); do
    line=$(journalctl -u gpanel --no-pager -o cat 2>/dev/null | grep '初始密码' | tail -1 || true)
    [ -n "$line" ] && break
    sleep 0.5
  done
  [ -n "${line:-}" ] && echo "$line"
fi
echo "状态: sudo systemctl status gpanel    日志: sudo journalctl -u gpanel -f"
