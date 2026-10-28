#!/bin/bash
# End-to-end smoke test against a real panel binary.
#
#   scripts/smoke.sh [path/to/gpanel]          (default: bin/gpanel)
#
# Environment:
#   SMOKE_PORT=24445     port to listen on
#   SMOKE_NETWORK=0      skip checks that call the Adoptium API
#   SMOKE_JAVA=1         also install a real Temurin JRE and run it (~50 MB download)
#   SMOKE_JAVA_MAJOR=21  which Java major version to install
# Variables set here are read inside the quoted check expressions (eval).
# shellcheck disable=SC2034
set -u

BIN="$(cd "$(dirname "${1:-bin/gpanel}")" && pwd)/$(basename "${1:-bin/gpanel}")"
HERE="$(cd "$(dirname "$0")" && pwd)"
PORT="${SMOKE_PORT:-24445}"
# Load monitoring (dashboard) reads /proc; other systems report host info only.
LINUX=0
[ "$(uname -s)" = Linux ] && LINUX=1
B="http://127.0.0.1:$PORT"
WORK="$(mktemp -d)"
DATA="$WORK/data"; LOG="$WORK/panel.log"; JAR="$WORK/cookies"
PID=""

cleanup() {
  [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null && kill -KILL "$PID"
  rm -rf "$WORK"
}
trap cleanup EXIT

pass=0; fail=0
check() {
  if eval "$2"; then echo "PASS  $1"; pass=$((pass + 1))
  else echo "FAIL  $1"; fail=$((fail + 1)); fi
}
c() { curl -s -b "$JAR" -c "$JAR" "$@"; }
jfield() { grep -o "\"$1\":\"[^\"]*\"" | head -1 | cut -d'"' -f4; }
status() { c "$B/api/instances/$1"; }
# wait_for <seconds> <condition>
wait_for() {
  local i
  for ((i = 0; i < $1 * 10; i++)); do eval "$2" && return 0; sleep 0.1; done
  return 1
}

[ -x "$BIN" ] || { echo "panel binary not found: $BIN" >&2; exit 2; }

"$BIN" -addr "127.0.0.1:$PORT" -data "$DATA" >"$LOG" 2>&1 &
PID=$!
wait_for 10 'curl -s -o /dev/null "$B/"' || { echo "panel did not start"; cat "$LOG"; exit 1; }

# --- auth
check "首页可访问"           '[ "$(curl -s -o /dev/null -w %{http_code} $B/)" = 200 ]'
check "前端资源已内嵌"        'curl -s $B/vendor/xterm.js | head -c 20 | grep -q function'
check "站点图标"              'curl -s $B/favicon.svg | grep -q "<svg"'
check "未登录被拦截"          '[ "$(curl -s -o /dev/null -w %{http_code} $B/api/instances)" = 401 ]'
PW=$(grep -o '初始密码 [0-9a-f]*' "$LOG" | awk '{print $2}')
check "日志打印初始密码"      '[ -n "$PW" ]'
check "错误密码被拒"          'c -X POST $B/api/auth/login -d "{\"username\":\"admin\",\"password\":\"x\"}" | grep -q error'
check "登录成功"              'c -X POST $B/api/auth/login -d "{\"username\":\"admin\",\"password\":\"$PW\"}" | grep -q admin'
check "跨站请求被拒"          'c -X POST -H "Origin: http://evil.example" $B/api/instances -d "{}" | grep -q 跨站'

# --- instance lifecycle
ID=$(c -X POST $B/api/instances -d "{\"name\":\"smoke\",\"command\":\"bash $HERE/fake-server.sh\",\"stopCommand\":\"stop\",\"stopTimeout\":5}" | jfield id)
check "创建实例"              '[ -n "$ID" ]'
check "启动实例"              'c -X POST $B/api/instances/$ID/start | grep -q "\"state\":\"running\""'
check "发送命令"              'c -X POST $B/api/instances/$ID/command -d "{\"command\":\"ping\"}" | grep -q running'
check "概览统计实例资源"      '[ $LINUX = 0 ] || wait_for 5 "c $B/api/system | grep -q \"\\\"$ID\\\":{\\\"cpu\\\"\""'
c -X POST $B/api/instances/$ID/stop >/dev/null
check "优雅停止 (退出码 0)"   'wait_for 5 "status $ID | grep -q \"\\\"state\\\":\\\"stopped\\\".*\\\"exitCode\\\":0\""'

# Ctrl+C as the stop command (GSManager's "ctrl+c"); stopTimeout is long, so
# stopping quickly proves it was the Ctrl+C and not the SIGTERM fallback.
c -X PUT $B/api/instances/$ID -d "{\"name\":\"smoke\",\"command\":\"bash $HERE/fake-server.sh\",\"stopCommand\":\"^C\",\"stopTimeout\":30}" >/dev/null
c -X POST $B/api/instances/$ID/start >/dev/null
wait_for 5 "status $ID | grep -q \"\\\"state\\\":\\\"running\\\"\""
c -X POST $B/api/instances/$ID/stop >/dev/null
check "Ctrl+C 停止命令"        'wait_for 5 "status $ID | grep -q \"\\\"state\\\":\\\"stopped\\\"\""'

c -X POST $B/api/instances/$ID/start >/dev/null
c -X POST $B/api/instances/$ID/command -d '{"command":"crash"}' >/dev/null
check "崩溃被识别 (退出码 3)" 'wait_for 5 "status $ID | grep -q \"\\\"state\\\":\\\"crashed\\\".*\\\"exitCode\\\":3\""'

c -X PUT $B/api/instances/$ID -d "{\"name\":\"smoke\",\"command\":\"bash $HERE/fake-server.sh\",\"stopCommand\":\"stop\",\"stopTimeout\":5,\"autoRestart\":true}" >/dev/null
c -X POST $B/api/instances/$ID/start >/dev/null
PID1=$(status "$ID" | grep -o '"pid":[0-9]*' | cut -d: -f2)
c -X POST $B/api/instances/$ID/command -d '{"command":"crash"}' >/dev/null
check "崩溃后自动重启"        'wait_for 10 "status $ID | grep -q \"\\\"state\\\":\\\"running\\\"\" && ! status $ID | grep -q \"\\\"pid\\\":$PID1,\""'

# Stop-command ignored -> escalation to SIGTERM after stopTimeout.
c -X PUT $B/api/instances/$ID -d "{\"name\":\"smoke\",\"command\":\"bash $HERE/fake-server.sh\",\"stopCommand\":\"ignored\",\"stopTimeout\":1}" >/dev/null
c -X POST $B/api/instances/$ID/stop >/dev/null
check "停止超时升级为 SIGTERM" 'wait_for 15 "status $ID | grep -q \"\\\"state\\\":\\\"stopped\\\"\""'

check "未定义变量报错"        'c -X PUT $B/api/instances/$ID -d "{\"name\":\"smoke\",\"command\":\"{{nope}} x\"}" >/dev/null; c -X POST $B/api/instances/$ID/start | grep -q nope'

# --- files
F="$B/api/instances/$ID/files"
check "写入文件"              'c -X PUT "$F/content?path=/a.txt" --data-binary hello -H "Content-Type: text/plain" | grep -q ok'
check "读取文件"              '[ "$(c "$F/content?path=/a.txt")" = hello ]'
check "新建目录与上传"        'c -X POST $F/mkdir -d "{\"path\":\"/d\"}" >/dev/null; c -X POST -F "file=@$HERE/fake-server.sh" "$F/upload?path=/d" | grep -q fake-server'
check "压缩与解压"            'c -X POST $F/zip -d "{\"paths\":[\"/d\",\"/a.txt\"],\"dest\":\"/b.zip\"}" >/dev/null; c -X POST $F/unzip -d "{\"path\":\"/b.zip\",\"dest\":\"/out\"}" >/dev/null; [ "$(c "$F/content?path=/out/a.txt")" = hello ]'
check "复制（同名生成副本）"    'c -X POST $F/copy -d "{\"paths\":[\"/a.txt\"],\"dest\":\"/\"}" | grep -q "a - 副本.txt"'
check "移动到文件夹"          'c -X POST $F/move -d "{\"paths\":[\"/a - 副本.txt\"],\"dest\":\"/d\"}" | grep -q ok && [ "$(c "$F/content?path=/d/a%20-%20%E5%89%AF%E6%9C%AC.txt")" = hello ]'
check "不能移动到自身内部"    'c -X POST $F/move -d "{\"paths\":[\"/d\"],\"dest\":\"/d\"}" | grep -q error'
check "下载文件"              '[ "$(c "$F/download?path=/out/a.txt")" = hello ]'
check "路径越界被拒"          'c "$F/content?path=../../../etc/hosts" | grep -q error'

# Resumable upload: "hello world" in two chunks, with a wrong offset in between.
c -X POST $F/mkdir -d '{"path":"/up"}' >/dev/null
UP=$(c -X POST $F/uploads -d '{"path":"/up","name":"r.txt","size":11,"modified":1}')
UID_=$(echo "$UP" | jfield id)
UQ="path=%2Fup&name=r.txt&size=11&modified=1"
check "开始分片上传"          '[ -n "$UID_" ] && echo "$UP" | grep -q "\"offset\":0"'
check "上传第一片"            'printf "hello " | c -X PUT --data-binary @- "$F/uploads/$UID_?$UQ&offset=0" | grep -q "\"done\":false,\"offset\":6"'
check "偏移不符返回 409"      '[ "$(printf x | c -o /dev/null -w %{http_code} -X PUT --data-binary @- "$F/uploads/$UID_?$UQ&offset=3")" = 409 ]'
check "断点续传取回偏移"      'c -X POST $F/uploads -d "{\"path\":\"/up\",\"name\":\"r.txt\",\"size\":11,\"modified\":1}" | grep -q "\"offset\":6"'
check "上传完成并改名"        'printf world | c -X PUT --data-binary @- "$F/uploads/$UID_?$UQ&offset=6" | grep -q "\"done\":true" && [ "$(c "$F/content?path=/up/r.txt")" = "hello world" ]'
check "列表不含临时分片"      '! c "$F?path=/up" | grep -q gpanel-upload'

# Server logs (logs/ and crash-reports/).
c -X POST $F/mkdir -d '{"path":"/logs"}' >/dev/null
c -X PUT "$F/content?path=/logs/latest.log" --data-binary $'[main/INFO]: a\n[main/ERROR]: boom\n[main/INFO]: c\n' -H "Content-Type: text/plain" >/dev/null
check "日志文件列表"          'c $B/api/instances/$ID/logs | grep -q "\"path\":\"logs/latest.log\""'
check "日志按级别筛选"        'c "$B/api/instances/$ID/logs/view?file=logs/latest.log&level=error" | grep -q "\"total\":3,\"matched\":1"'
check "日志外文件被拒"        '[ "$(c -o /dev/null -w %{http_code} "$B/api/instances/$ID/logs/view?file=up/r.txt")" = 400 ]'
check "非 MC 实例无玩家状态"  'c $B/api/instances/$ID/status | grep -q "\"minecraft\":false" && c $B/api/status | grep -q "\"$ID\""'
WD=$(status "$ID" | jfield dir)
ln -s /etc "$WD/escape"
check "符号链接逃逸被拒"      'c "$F/content?path=/escape/hosts" | grep -q error'
check "不能删除根目录"        'c -X POST $F/delete -d "{\"paths\":[\"/\"]}" | grep -q error'

# --- runtimes
check "系统概览接口"          '[ $LINUX = 1 ] && want=true || want=false; c $B/api/system | grep -q "\"supported\":$want" && c $B/api/system | grep -q "\"hostname\""'
check "运行时接口"            'c $B/api/runtimes | grep -q "^\["'
if [ "${SMOKE_NETWORK:-1}" != 0 ]; then
  RELS=$(c "$B/api/runtimes/releases?kind=java")
  check "Adoptium 版本列表"   'echo "$RELS" | grep -q "\"imageType\":\"jre\""'
  check "伪造版本 ID 被拒"    'c -X POST $B/api/runtimes/install -d "{\"kind\":\"java\",\"releaseId\":\"../../x\"}" | grep -q error'

  if [ "${SMOKE_JAVA:-0}" = 1 ]; then
    MAJOR="${SMOKE_JAVA_MAJOR:-21}"
    REL=$(echo "$RELS" | grep -o "\"id\":\"jdk-${MAJOR}[^\"]*-jre\"" | head -1 | cut -d'"' -f4)
    check "找到 Java $MAJOR JRE" '[ -n "$REL" ]'
    c -X POST $B/api/runtimes/install -d "{\"kind\":\"java\",\"releaseId\":\"$REL\"}" >/dev/null
    check "下载、校验并解压 JRE" 'wait_for 300 "c $B/api/runtimes/jobs | grep -q \"\\\"state\\\":\\\"\\(done\\|failed\\)\\\"\""; c $B/api/runtimes/jobs | grep -q "\"state\":\"done\""'
    RT=$(c $B/api/runtimes/jobs | grep -o '"runtimeId":"[^"]*"' | head -1 | cut -d'"' -f4)
    check "安装后自动识别"      '[ -n "$RT" ] && c $B/api/runtimes | grep -q "\"id\":\"$RT\""'
    JID=$(c -X POST $B/api/instances -d "{\"name\":\"java\",\"command\":\"bash -c '{{java}} -version > version.txt 2>&1'\",\"runtime\":\"$RT\"}" | jfield id)
    c -X POST $B/api/instances/$JID/start >/dev/null
    check "{{java}} 启动真实 JVM" 'wait_for 30 "status $JID | grep -q \"\\\"exitCode\\\":0\""; c "$B/api/instances/$JID/files/content?path=/version.txt" | grep -q "Temurin-$MAJOR"'
    check "使用中的运行时不可卸载" 'c -X DELETE $B/api/runtimes/$RT | grep -q java'
    c -X DELETE "$B/api/instances/$JID?files=1" >/dev/null
    check "卸载运行时"          'c -X DELETE $B/api/runtimes/$RT | grep -q ok && ! c $B/api/runtimes | grep -q "\"id\":\"$RT\""'
  fi
fi

# --- accounts: a plain user sees and does only what was granted
check "管理员创建普通用户"    'c -X POST $B/api/users -d "{\"username\":\"smoke-user\",\"role\":\"user\",\"password\":\"smoke-user-1\",\"instances\":{\"$ID\":{\"console\":true}}}" | grep -q "\"mustChangePassword\":true"'
JU="$WORK/cookies-user"
u() { curl -s -b "$JU" -c "$JU" "$@"; }
u -X POST $B/api/auth/login -d '{"username":"smoke-user","password":"smoke-user-1"}' >/dev/null
check "新用户须先改密码"      'u $B/api/instances | grep -q mustChangePassword'
u -X POST $B/api/auth/password -d '{"oldPassword":"smoke-user-1","newPassword":"smoke-user-2"}' >/dev/null
check "只看到被分配的实例"    '[ "$(u $B/api/instances | grep -o "\"id\":\"[^\"]*\"" | sort -u | tr -d "\n")" = "\"id\":\"$ID\"" ]'
check "看不到环境变量"        '! u $B/api/instances/$ID | grep -q "\"env\":{"'
check "有控制台权限可操作"    '[ "$(u -o /dev/null -w %{http_code} -X POST $B/api/instances/$ID/stop)" != 403 ]'
check "无文件权限被拒"        '[ "$(u -o /dev/null -w %{http_code} "$F?path=/")" = 403 ]'
check "不能新建实例"          '[ "$(u -o /dev/null -w %{http_code} -X POST $B/api/instances -d "{}")" = 403 ]'
check "不能管理用户"          '[ "$(u -o /dev/null -w %{http_code} $B/api/users)" = 403 ]'
c -X PUT $B/api/users/smoke-user -d '{"disabled":true}' >/dev/null
check "停用后会话立即失效"    '[ "$(u -o /dev/null -w %{http_code} $B/api/auth/me)" = 401 ]'
check "删除用户"              'c -X DELETE $B/api/users/smoke-user | grep -q ok'

# --- HTTPS on the same port, and password changes
S="https://127.0.0.1:$PORT"
check "未启用时拒绝 HTTPS"    '! curl -sk -o /dev/null $S/'
check "生成自签名证书"        'c -X POST $B/api/system/tls/self-signed -d "{\"hosts\":[\"127.0.0.1\"],\"days\":30}" | grep -q "\"selfSigned\":true"'
check "启用 HTTPS"            'c -X PUT $B/api/system/tls -d "{\"enabled\":true}" | grep -q "\"active\":true"'
check "同端口 HTTPS 可用"     'curl -sk -b "$JAR" $S/api/instances | grep -q smoke'
check "HTTP 仍可用"           '[ "$(curl -s -o /dev/null -w %{http_code} $B/)" = 200 ]'
c -X PUT $B/api/system/tls -d '{"enabled":true,"forceHttps":true}' >/dev/null
check "强制 HTTPS 时跳转"     '[ "$(curl -s -o /dev/null -w %{http_code}%{redirect_url} $B/x)" = "307$S/x" ]'
check "通过 HTTPS 关闭"       'curl -sk -b "$JAR" -X PUT $S/api/system/tls -d "{\"enabled\":false}" | grep -q "\"active\":false"'
J2="$WORK/cookies2"
curl -s -c "$J2" -X POST $B/api/auth/login -d "{\"username\":\"admin\",\"password\":\"$PW\"}" >/dev/null
check "改密码退出其他会话"    'c -X POST $B/api/auth/password -d "{\"oldPassword\":\"$PW\",\"newPassword\":\"smoke-pass-123\"}" | grep -q "\"loggedOut\":1" && [ "$(curl -s -b "$J2" -o /dev/null -w %{http_code} $B/api/instances)" = 401 ]'
check "当前会话保持登录"      'c $B/api/instances | grep -q smoke'

# --- graceful shutdown
c -X PUT $B/api/instances/$ID -d "{\"name\":\"smoke\",\"command\":\"bash $HERE/fake-server.sh\",\"stopCommand\":\"stop\"}" >/dev/null
c -X POST $B/api/instances/$ID/start >/dev/null
kill -TERM "$PID"
check "SIGTERM 后面板退出"    'wait_for 15 "! kill -0 $PID 2>/dev/null"'
check "无残留子进程"          '! pgrep -f "$HERE/fake-server.sh" >/dev/null'
check "实例配置已持久化"      'grep -q smoke "$DATA/instances.json"'

echo "通过 $pass，失败 $fail"
if [ "$fail" != 0 ]; then
  echo "--- panel log"; cat "$LOG"
  exit 1
fi
