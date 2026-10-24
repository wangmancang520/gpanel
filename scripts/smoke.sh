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
set -u

BIN="$(cd "$(dirname "${1:-bin/gpanel}")" && pwd)/$(basename "${1:-bin/gpanel}")"
HERE="$(cd "$(dirname "$0")" && pwd)"
PORT="${SMOKE_PORT:-24445}"
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
c -X POST $B/api/instances/$ID/stop >/dev/null
check "优雅停止 (退出码 0)"   'wait_for 5 "status $ID | grep -q \"\\\"state\\\":\\\"stopped\\\".*\\\"exitCode\\\":0\""'

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
check "下载文件"              '[ "$(c "$F/download?path=/out/a.txt")" = hello ]'
check "路径越界被拒"          'c "$F/content?path=../../../etc/hosts" | grep -q error'
WD=$(status "$ID" | jfield workDir)
ln -s /etc "$WD/escape"
check "符号链接逃逸被拒"      'c "$F/content?path=/escape/hosts" | grep -q error'
check "不能删除根目录"        'c -X POST $F/delete -d "{\"paths\":[\"/\"]}" | grep -q error'

# --- runtimes
check "运行时接口"            'c $B/api/runtimes | grep -q "^\["'
if [ "${SMOKE_NETWORK:-1}" != 0 ]; then
  RELS=$(c "$B/api/runtimes/releases?kind=java")
  check "Adoptium 版本列表"   'echo "$RELS" | grep -q "\"imageType\":\"jre\""'
  check "伪造版本 ID 被拒"    'c -X POST $B/api/runtimes/install -d "{\"kind\":\"java\",\"releaseId\":\"../../x\"}" | grep -q error'

  if [ "${SMOKE_JAVA:-0}" = 1 ]; then
    MAJOR="${SMOKE_JAVA_MAJOR:-21}"
    REL=$(echo "$RELS" | grep -o "\"id\":\"jdk-$MAJOR[^\"]*-jre\"" | head -1 | cut -d'"' -f4)
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
