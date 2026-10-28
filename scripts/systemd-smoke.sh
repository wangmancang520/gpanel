#!/bin/bash
# Exercises install.sh (gpanel-ctl) under real systemd: install, password
# reset, update, automatic rollback of a broken update, manual rollback,
# release download with checksum verification, backup and uninstall.
# Destructive to the host's gpanel install; meant for throwaway CI runners.
#
#   sudo scripts/systemd-smoke.sh DIR   # DIR holds gpanel-v1..v3 built with -X main.version=v0.0.N
# Variables set here are read inside the quoted check expressions (eval).
# shellcheck disable=SC2034
set -u

DIR="$(cd "${1:?usage: sudo $0 <dir with gpanel-v1, gpanel-v2, gpanel-v3>}" && pwd)"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$ROOT/install.sh"
CTL=/usr/local/bin/gpanel-ctl
B="http://127.0.0.1:24444"
WORK="$(mktemp -d)"
JAR="$WORK/cookies"
FAKE=/usr/local/bin/gpanel-fake-server
export NO_COLOR=1

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
ver() { /usr/local/bin/gpanel -version 2>/dev/null; }
pid() { systemctl show -p MainPID --value gpanel; }
login() { rm -f "$JAR"; c -X POST $B/api/auth/login -d "{\"username\":\"admin\",\"password\":\"$1\"}" | grep -q admin; }

# --- install
OUT=$("$SCRIPT" install -y --local "$DIR/gpanel-v1" --port 24444 --bind 127.0.0.1 2>&1); echo "$OUT"
check "安装成功"                  'echo "$OUT" | grep -q 安装完成'
check "安装了 gpanel-ctl"         '[ -x $CTL ] && cmp -s $CTL "$SCRIPT"'
check "unit 由程序自身生成"       'cmp -s /etc/systemd/system/gpanel.service <("$DIR/gpanel-v1" -print-systemd-unit)'
check "unit 文件校验通过"         'systemd-analyze verify /etc/systemd/system/gpanel.service'
check "监听地址已写入配置"        'grep -q "^GPANEL_ADDR=127.0.0.1:24444$" /etc/gpanel/gpanel.env'
check "配置文件 root:gpanel 640"  '[ "$(stat -c %U:%G:%a /etc/gpanel/gpanel.env)" = root:gpanel:640 ]'
check "服务处于运行状态"          'systemctl is-active --quiet gpanel'
check "以 gpanel 用户运行"        '[ "$(ps -o user= -p "$(pid)")" = gpanel ]'
check "可绑定 80 端口（仅此权限）" 'grep -q "^CapAmb:[[:space:]]*0000000000000400$" /proc/$(pid)/status'
check "数据目录归 gpanel 所有"    '[ "$(stat -c %U /var/lib/gpanel)" = gpanel ]'
PW=$(echo "$OUT" | grep -o '初始密码 *[0-9a-f]\{16\}' | awk '{print $2}')
check "安装输出初始密码"          '[ -n "$PW" ]'
check "登录成功"                  'login "$PW"'
check "再次安装提示已安装"        '"$SCRIPT" install -y --local "$DIR/gpanel-v1" 2>&1 | grep -q 已安装'

install -m 755 "$ROOT/scripts/fake-server.sh" "$FAKE"
ID=$(c -X POST $B/api/instances -d "{\"name\":\"svc\",\"command\":\"$FAKE\",\"stopCommand\":\"stop\",\"autoStart\":true}" | grep -o '"id":"[^"]*"' | cut -d'"' -f4)
check "实例启动"                  'c -X POST $B/api/instances/$ID/start | grep -q running'
GPID=$(c $B/api/instances/$ID | grep -o '"pid":[0-9]*' | cut -d: -f2)
check "游戏进程以 gpanel 用户运行" '[ "$(ps -o user= -p "$GPID")" = gpanel ]'
check "游戏进程在服务 cgroup 内"  'grep -q gpanel.service /proc/$GPID/cgroup'

# --- password reset: takes effect without restarting the panel
PANEL=$(pid)
OUT=$($CTL reset-password -y 2>&1); echo "$OUT"
NEWPW=$(echo "$OUT" | grep -o '新密码 *[0-9a-f]\{16\}' | awk '{print $2}')
check "重置密码输出新密码"        '[ -n "$NEWPW" ] && [ "$NEWPW" != "$PW" ]'
check "旧会话已失效"              '[ "$(c -o /dev/null -w %{http_code} $B/api/instances)" = 401 ]'
check "新密码可登录"              'login "$NEWPW"'
check "重置未重启面板和游戏服务器" '[ "$(pid)" = "$PANEL" ] && kill -0 "$GPID"'
check "未知账号报错"              '! $CTL reset-password -y --user nobody >/dev/null 2>&1'
check "status 显示版本"           '$CTL status | grep -q v0.0.1'

# --- update from a local file, keeping the previous version
OUT=$($CTL update -y --local "$DIR/gpanel-v2" 2>&1); echo "$OUT"
check "更新到 v0.0.2"             '[ "$(ver)" = v0.0.2 ]'
check "保留了上一版本"            '[ "$(/usr/local/lib/gpanel/gpanel.prev -version)" = v0.0.1 ]'
check "更新后自动启动实例已恢复"  'wait_for 15 "pgrep -u gpanel -f $FAKE >/dev/null"'
check "更新后密码不变"            'login "$NEWPW"'

# A build that won't start must be rolled back automatically.
cat >"$WORK/broken" <<EOF
#!/bin/sh
case "\$1" in
  -version) echo v0.0.9 ;;
  -print-systemd-unit) exec "$DIR/gpanel-v2" -print-systemd-unit ;;
  *) exit 1 ;;
esac
EOF
chmod +x "$WORK/broken"
OUT=$($CTL update -y --local "$WORK/broken" 2>&1); RC=$?; echo "$OUT"
check "坏版本更新报告失败"        '[ $RC != 0 ] && echo "$OUT" | grep -q 自动回滚'
check "自动回滚到 v0.0.2"         '[ "$(ver)" = v0.0.2 ] && systemctl is-active --quiet gpanel'

OUT=$($CTL rollback -y 2>&1); echo "$OUT"
check "手动回滚到 v0.0.1"         '[ "$(ver)" = v0.0.1 ] && systemctl is-active --quiet gpanel'
$CTL rollback -y >/dev/null 2>&1
check "再次回滚回到 v0.0.2"       '[ "$(ver)" = v0.0.2 ]'

# --- release download: files and SHA256SUMS served like GitHub releases
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
REL="$WORK/releases/download"
mkdir -p "$REL/v0.0.3" "$REL/v0.0.4"
cp "$DIR/gpanel-v3" "$REL/v0.0.3/gpanel-linux-$ARCH"
{ cat "$SCRIPT"; echo "# smoke-marker v0.0.3"; } >"$REL/v0.0.3/install.sh"
(cd "$REL/v0.0.3" && sha256sum * >SHA256SUMS)
cp "$DIR/gpanel-v1" "$REL/v0.0.4/gpanel-linux-$ARCH"
cp "$SCRIPT" "$REL/v0.0.4/install.sh"
(cd "$REL/v0.0.4" && sha256sum * >SHA256SUMS && echo tampered >>"gpanel-linux-$ARCH")
(cd "$WORK/releases" && exec python3 -m http.server 8765 --bind 127.0.0.1 >/dev/null 2>&1) &
HTTP=$!
wait_for 5 'curl -s -o /dev/null http://127.0.0.1:8765/'
export GPANEL_RELEASES=http://127.0.0.1:8765
OUT=$($CTL update -y --version v0.0.3 2>&1); echo "$OUT"
check "下载、校验并更新到 v0.0.3" '[ "$(ver)" = v0.0.3 ] && echo "$OUT" | grep -q 校验通过'
check "维护脚本随版本更新"        'grep -q "smoke-marker v0.0.3" $CTL'
check "已是该版本时不重复更新"    '$CTL update -y --version v0.0.3 2>&1 | grep -q 已经是最新版本'
OUT=$($CTL update -y --version v0.0.4 2>&1); RC=$?; echo "$OUT"
check "校验失败拒绝更新"          '[ $RC != 0 ] && echo "$OUT" | grep -q 校验失败 && [ "$(ver)" = v0.0.3 ]'

# A download mirror ("GitHub 加速前缀") only supplies files: SHA256SUMS still
# comes from the release itself, so a mirror can't vouch for its own tampering.
mkdir -p "$REL/v0.0.5"
cp "$DIR/gpanel-v2" "$REL/v0.0.5/gpanel-linux-$ARCH"
cp "$SCRIPT" "$REL/v0.0.5/install.sh"
(cd "$REL/v0.0.5" && sha256sum * >SHA256SUMS)
MIR="$WORK/mirror"
mkdir -p "$MIR"
{ cat "$DIR/gpanel-v2"; echo tampered; } >"$MIR/gpanel-linux-$ARCH"
cp "$SCRIPT" "$MIR/install.sh"
(cd "$MIR" && sha256sum "gpanel-linux-$ARCH" install.sh >SHA256SUMS) # consistent with the tampered build
cat >"$WORK/mirror.py" <<'EOF'
# Serves files by basename, like a prefix proxy in front of GitHub would.
import http.server, os, sys
root = sys.argv[1]
class H(http.server.SimpleHTTPRequestHandler):
    def translate_path(self, path):
        return os.path.join(root, os.path.basename(path.split("?")[0]))
    def log_message(self, *args):
        pass
http.server.ThreadingHTTPServer(("127.0.0.1", 8766), H).serve_forever()
EOF
python3 "$WORK/mirror.py" "$MIR" &
MIRP=$!
wait_for 5 'curl -s -o /dev/null http://127.0.0.1:8766/x'
M=http://127.0.0.1:8766/
OUT=$($CTL update -y --version v0.0.5 --mirror $M 2>&1); RC=$?; echo "$OUT"
check "镜像篡改的程序被拒绝"      '[ $RC != 0 ] && echo "$OUT" | grep -q 校验失败 && [ "$(ver)" = v0.0.3 ]'
cp "$REL/v0.0.5/gpanel-linux-$ARCH" "$MIR/"
(cd "$MIR" && sha256sum "gpanel-linux-$ARCH" install.sh >SHA256SUMS)
OUT=$($CTL update -y --version v0.0.5 --mirror $M 2>&1); echo "$OUT"
check "镜像提供正版文件时可更新"  '[ "$(ver)" = v0.0.2 ] && echo "$OUT" | grep -q 校验通过'

# GitHub unreachable: the mirror's own checksums are refused unless trusted.
export GPANEL_RELEASES=http://127.0.0.1:9
OUT=$($CTL update -y --version v0.0.6 --mirror $M 2>&1); RC=$?; echo "$OUT"
check "无法直连时不信任镜像校验"  '[ $RC != 0 ] && echo "$OUT" | grep -q GPANEL_TRUST_MIRROR && [ "$(ver)" = v0.0.2 ]'
OUT=$(GPANEL_TRUST_MIRROR=1 $CTL update -y --version v0.0.6 --mirror $M 2>&1); RC=$?; echo "$OUT"
check "明确信任后可用镜像校验"    '[ $RC = 0 ] && echo "$OUT" | grep -q 校验通过'
kill $MIRP $HTTP; unset GPANEL_RELEASES

# --- backup, stop/start
check "备份"                      '$CTL backup -y --out $WORK/b.tgz >/dev/null 2>&1 && tar -tzf $WORK/b.tgz | grep -q "var/lib/gpanel/users.json"'
check "备份文件仅 root 可读"      '[ "$(stat -c %a $WORK/b.tgz)" = 600 ]'
$CTL stop -y >/dev/null 2>&1
check "停止服务后游戏进程已退出"  '! systemctl is-active --quiet gpanel && ! pgrep -u gpanel -f $FAKE >/dev/null'
check "面板优雅关闭了实例"        'journalctl -u gpanel --no-pager -o cat | grep -q 正在关闭'
check "启动"                      '$CTL start -y >/dev/null 2>&1 && systemctl is-active --quiet gpanel'

# --- uninstall keeps data; reinstall picks it up; purge removes everything
$CTL uninstall -y >/dev/null 2>&1
check "卸载移除程序和服务"        '[ ! -e /usr/local/bin/gpanel ] && [ ! -e /etc/systemd/system/gpanel.service ] && [ ! -e $CTL ]'
check "卸载默认保留数据"          '[ -f /var/lib/gpanel/users.json ]'
OUT=$("$SCRIPT" install -y --local "$DIR/gpanel-v1" --port 24444 --bind 127.0.0.1 2>&1); echo "$OUT"
check "重新安装沿用原有数据"      'wait_for 10 "login \"$NEWPW\""'
# The journal still holds the first install's "初始密码" line; it's stale.
check "重新安装不显示旧初始密码"  '! echo "$OUT" | grep -q "$PW" && echo "$OUT" | grep -q 沿用'
$CTL uninstall -y --purge >/dev/null 2>&1
check "彻底卸载删除数据和用户"    '[ ! -e /var/lib/gpanel ] && [ ! -e /etc/gpanel ] && ! id gpanel >/dev/null 2>&1'

rm -rf "$WORK" "$FAKE"
echo "通过 $pass，失败 $fail"
if [ "$fail" != 0 ]; then
  echo "--- journal"; journalctl -u gpanel --no-pager -n 100
  exit 1
fi
