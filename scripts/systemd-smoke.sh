#!/bin/bash
# Installs GPanel with deploy/install.sh and checks it under real systemd.
# Destructive to the host's gpanel install; meant for throwaway CI runners.
#
#   sudo scripts/systemd-smoke.sh bin/gpanel
set -u

BIN="${1:?usage: sudo $0 <gpanel binary>}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
B="http://127.0.0.1:24444"
JAR="$(mktemp)"
FAKE=/usr/local/bin/gpanel-fake-server

pass=0; fail=0
check() {
  if eval "$2"; then echo "PASS  $1"; pass=$((pass + 1))
  else echo "FAIL  $1"; fail=$((fail + 1)); fi
}
c() { curl -s -b "$JAR" -c "$JAR" "$@"; }
wait_for() {
  local i
  for ((i = 0; i < $1 * 10; i++)); do eval "$2" && return 0; sleep 0.1; done
  return 1
}

OUT=$("$ROOT/deploy/install.sh" "$BIN" 2>&1); echo "$OUT"
check "安装脚本成功"            'echo "$OUT" | grep -q 已安装并启动'
check "unit 文件校验通过"      'systemd-analyze verify /etc/systemd/system/gpanel.service'
check "服务处于运行状态"        'systemctl is-active --quiet gpanel'
check "以 gpanel 用户运行"      '[ "$(ps -o user= -p "$(systemctl show -p MainPID --value gpanel)")" = gpanel ]'
check "数据目录归 gpanel 所有"  '[ "$(stat -c %U /var/lib/gpanel)" = gpanel ]'
check "面板可访问"              'wait_for 10 "curl -s -o /dev/null $B/"'
PW=$(echo "$OUT" | grep -o '初始密码 [0-9a-f]*' | awk '{print $2}')
check "安装输出初始密码"        '[ -n "$PW" ]'
check "登录成功"                'c -X POST $B/api/auth/login -d "{\"username\":\"admin\",\"password\":\"$PW\"}" | grep -q admin'

install -m 755 "$ROOT/scripts/fake-server.sh" "$FAKE"
ID=$(c -X POST $B/api/instances -d "{\"name\":\"svc\",\"command\":\"$FAKE\",\"stopCommand\":\"stop\",\"autoStart\":true}" | grep -o '"id":"[^"]*"' | cut -d'"' -f4)
check "实例启动"                'c -X POST $B/api/instances/$ID/start | grep -q running'
GPID=$(c $B/api/instances/$ID | grep -o '"pid":[0-9]*' | cut -d: -f2)
check "游戏进程以 gpanel 用户运行" '[ "$(ps -o user= -p "$GPID")" = gpanel ]'
check "游戏进程在服务 cgroup 内" 'grep -q gpanel.service /proc/$GPID/cgroup'

systemctl stop gpanel
check "停止服务后游戏进程已退出" '! kill -0 "$GPID" 2>/dev/null'
check "面板优雅关闭了实例"      'journalctl -u gpanel --no-pager -o cat | grep -q 正在关闭'
check "服务正常结束"            '[ "$(systemctl show -p Result --value gpanel)" = success ]'

OUT=$("$ROOT/deploy/install.sh" "$BIN" 2>&1)
check "再次运行安装脚本可启动"  'systemctl is-active --quiet gpanel'
check "升级不重复打印密码"      '! echo "$OUT" | grep -q 初始密码'
check "自动启动的实例已恢复"    'wait_for 10 "pgrep -u gpanel -f $FAKE >/dev/null"'
OUT=$("$ROOT/deploy/install.sh" "$BIN" 2>&1)
check "运行中再安装即升级重启"  'echo "$OUT" | grep -q 已升级并重启'

echo "通过 $pass，失败 $fail"
if [ "$fail" != 0 ]; then
  echo "--- journal"; journalctl -u gpanel --no-pager -n 100
  exit 1
fi
