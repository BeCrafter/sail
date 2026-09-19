#!/usr/bin/env bash
#
# sail serve webdav 端到端验证脚本(真实 S3 兼容后端,读写往返)。
# 覆盖挂载视角的协议流:OPTIONS 能力探测(不带认证)→ PROPFIND 列目录 →
# GET/Range 读取 → PUT 上传 → MOVE 改名 → DELETE 删除,全程带 Basic 认证,
# 用 curl 直接对服务发 HTTP 请求,验证「挂成网络盘」这条链路的真实行为。
# 不测极限(大文件/海量对象)。
#
# 用法一(复用已有配置,推荐,不碰凭证;桶取自 profile 的 bucket,无需再指定):
#   SAIL_E2E_CONFIG=~/.config/sail/config.yaml SAIL_E2E_PROFILE=test ./scripts/e2e-serve.sh
# 用法二(环境变量传凭证,自建临时配置,需指定桶):
#   SAIL_E2E_ENDPOINT=... SAIL_E2E_ACCESS_KEY=... SAIL_E2E_SECRET_KEY=... \
#     SAIL_E2E_BUCKET=... ./scripts/e2e-serve.sh
#
# 环境变量:
#   SAIL_E2E_LISTEN  服务监听地址,默认 :18443
#   SAIL_E2E_USER / SAIL_E2E_PASSWORD  Basic 凭据,默认 e2e / e2e-secret
#   SAIL_E2E_PREFIX  共享根前缀,默认空(整桶);测试对象都写在该前缀下,清理只删该前缀
#   SAIL_E2E_BUCKET  仅用法二需要;用法一由 profile.bucket 提供,忽略此值

set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; CYAN='\033[0;36m'; NC='\033[0m'
pass=0; fail=0; skipped=0
ok()   { echo -e "${GREEN}[PASS]${NC} $1"; pass=$((pass+1)); }
err()  { echo -e "${RED}[FAIL]${NC} $1"; fail=$((fail+1)); }
skip() { echo -e "${YELLOW}[SKIP]${NC} $1"; skipped=$((skipped+1)); }
step() { echo -e "\n${CYAN}━━━ $1 ━━━${NC}"; }

# has <grep 参数...>:从 stdin 读完全部输入后再判断是否命中。
# 断言不要写成 `cmd | grep -q`:grep 命中即退出会给生产者 SIGPIPE,在
# `set -o pipefail` 下管道状态变成 141,断言会被误判为失败。
has() { grep "$@" >/dev/null; }

SAIL_BIN="${SAIL_BIN:-./sail}"
[[ -x "$SAIL_BIN" ]] || { echo -e "${RED}找不到 $SAIL_BIN,请先 go build -o sail .${NC}"; exit 1; }
command -v curl >/dev/null 2>&1 || { echo -e "${RED}需要 curl${NC}"; exit 1; }
# nc 仅用于发 `OPTIONS *`(curl 无法直接设置请求目标为 *);缺省时跳过该项。
HAVE_NC=""; command -v nc >/dev/null 2>&1 && HAVE_NC=1

# ── 配置:方式一(已有配置) / 方式二(env) ─────────────────────
CONFIG_FILE="${SAIL_E2E_CONFIG:-}"
BUCKET="${SAIL_E2E_BUCKET:-}"
LISTEN="${SAIL_E2E_LISTEN:-:18443}"
USER="${SAIL_E2E_USER:-e2e}"
PASSWORD="${SAIL_E2E_PASSWORD:-e2e-secret}"
PREFIX="${SAIL_E2E_PREFIX:-}"
PROFILE="${SAIL_E2E_PROFILE:-}"

if [[ -n "$CONFIG_FILE" ]]; then
    # 桶取自 profile 的 bucket,不再要求 SAIL_E2E_BUCKET(避免与 profile 不一致时,
    # serve 暴露一个桶、清理却删另一个桶)。清理用 s3:/// 空桶段,让 sail 走同一解析链。
    if [[ -n "$PROFILE" ]]; then
        SAIL="$SAIL_BIN -c $CONFIG_FILE -p $PROFILE"
    else
        SAIL="$SAIL_BIN -c $CONFIG_FILE"
    fi
    $SAIL ls --buckets >/dev/null 2>&1 || { echo -e "${RED}配置不可用:请检查 endpoint/密钥/默认桶${NC}"; exit 1; }
else
    ENDPOINT="${SAIL_E2E_ENDPOINT:-}"; ACCESS_KEY="${SAIL_E2E_ACCESS_KEY:-}"
    SECRET_KEY="${SAIL_E2E_SECRET_KEY:-}"
    [[ -z "$ENDPOINT" || -z "$ACCESS_KEY" || -z "$SECRET_KEY" || -z "$BUCKET" ]] && {
        echo -e "${RED}方式二需 SAIL_E2E_ENDPOINT/ACCESS_KEY/SECRET_KEY/BUCKET${NC}"; exit 1; }
    PROFILE="${SAIL_E2E_PROFILE:-e2e-serve}"
    WORK_DIR_CFG="$(mktemp -d)"; CONFIG_FILE="$WORK_DIR_CFG/.config/sail/config.yaml"
    mkdir -p "$(dirname "$CONFIG_FILE")"
    cat > "$CONFIG_FILE" <<EOF
default-profile: $PROFILE
profiles:
  $PROFILE:
    endpoint: $ENDPOINT
    access-key: $ACCESS_KEY
    secret-key: $SECRET_KEY
    bucket: "$BUCKET"
    path-style: true
EOF
    SAIL="$SAIL_BIN -c $CONFIG_FILE -p $PROFILE"
fi

# 测试用唯一前缀,清理只删它,不碰桶里其它数据。
# 桶用 s3:/// 空桶段表示:由 sail 按 profile 解析(serve 与清理同源,天然一致)。
TEST_PREFIX="sail-serve-e2e-$(date +%s)"
S3_ROOT="s3:///${PREFIX:+$PREFIX/}$TEST_PREFIX"
BASE="http://127.0.0.1:${LISTEN#:}"

cleanup() {
    echo -e "\n${CYAN}━━━ 清理 ━━━${NC}"
    [[ -n "${SERVE_PID:-}" ]] && kill "$SERVE_PID" 2>/dev/null || true
    [[ -n "${MU_PID:-}" ]] && kill "$MU_PID" 2>/dev/null || true
    $SAIL rm -r "$S3_ROOT/" >/dev/null 2>&1 || true
    rm -rf "${WORK_DIR_CFG:-}" 2>/dev/null || true
    echo -e "${GREEN}清理完成${NC}"
}
trap cleanup EXIT

# dav <method> <path> [extra...]:带认证发起 WebDAV 请求,返回响应头。
dav() {
    local method="$1" path="$2"; shift 2
    curl -sS -u "$USER:$PASSWORD" -X "$method" "$@" "$BASE$path"
}

echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${CYAN} sail serve webdav E2E  桶=${BUCKET:-<profile.bucket>}  前缀=${PREFIX:-<整桶>}  测试前缀=$TEST_PREFIX${NC}"
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"

# ════════════════════════════════════════════════════════
step "0. 启动服务"
serve_args=(serve webdav --listen "$LISTEN" --user "$USER" --password "$PASSWORD")
[[ -n "$PREFIX" ]] && serve_args+=(--prefix "$PREFIX")
$SAIL "${serve_args[@]}" >/tmp/sail-serve-e2e.log 2>&1 &
SERVE_PID=$!
# 等监听就绪(轮询,不依赖 sleep 时长)
ready=""
for _ in $(seq 1 50); do
    if curl -sS -o /dev/null "$BASE/" 2>/dev/null; then ready=1; break; fi
    sleep 0.2
done
[[ -n "$ready" ]] && ok "服务已监听 $LISTEN" || { err "服务未就绪: $(cat /tmp/sail-serve-e2e.log)"; exit 1; }
# 横幅按 --lang/配置/环境输出中英双语之一,两种都认。
grep -Eq "mount at:|挂载地址:" /tmp/sail-serve-e2e.log && ok "启动横幅含挂载地址" || err "横幅缺挂载地址"

# ════════════════════════════════════════════════════════
step "1. OPTIONS 能力探测(不带认证,应 200 + DAV/Allow)"
OPT_HEADERS="$(curl -sS -i -X OPTIONS "$BASE/")"
echo "$OPT_HEADERS" | has '^HTTP/1.1 200' && ok "OPTIONS 返回 200" || err "OPTIONS 非 200: $(echo "$OPT_HEADERS" | head -1)"
echo "$OPT_HEADERS" | has -i '^Dav: 1, 2' && ok "  带 DAV: 1,2" || err "  缺 DAV 头"
echo "$OPT_HEADERS" | has -i '^Allow: .*PROPFIND' && ok "  Allow 含 PROPFIND" || err "  Allow 缺 PROPFIND"

# OPTIONS *(星号):macOS Finder 挂载 WebDAV 的第一步,靠响应里的 DAV 头判定「是不是
# WebDAV 服务器」。net/http 默认会用内置 globalOptionsHandler 截胡,回空 200 无 DAV 头,
# Finder 因而卡在「连接中」——必须设 DisableGeneralOptionsHandler。用原始 TCP 才能发 `*`。
if [[ -n "$HAVE_NC" ]]; then
    OPTSTAR="$(printf 'OPTIONS * HTTP/1.1\r\nHost: 127.0.0.1:%s\r\nConnection: close\r\n\r\n' "${LISTEN#:}" \
        | nc -w 5 127.0.0.1 "${LISTEN#:}" 2>/dev/null || true)"
    echo "$OPTSTAR" | has '^HTTP/1.1 200' && ok "OPTIONS * 返回 200" || err "OPTIONS * 非 200"
    echo "$OPTSTAR" | has -i '^Dav: 1, 2' && ok "  OPTIONS * 带 DAV 头(Finder 判定 WebDAV 的关键)" || err "  OPTIONS * 缺 DAV 头(globalOptionsHandler 未禁用?)"
else
    skip "OPTIONS * 检查(缺 nc,跳过)"
fi

# ════════════════════════════════════════════════════════
step "2. PROPFIND 根目录(带认证,应 207 且含根 href)"
PF="$(dav PROPFIND / -H 'Depth: 1' --data '<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>')"
echo "$PF" | has '<D:href>/</D:href>' && ok "PROPFIND 根返回 207 含根" || err "PROPFIND 根异常: $(echo "$PF" | head -c 200)"

# ════════════════════════════════════════════════════════
step "2b. 目录列表缓存:第二次 PROPFIND 同一目录应显著更快"
T1=$(curl -sS -u "$USER:$PASSWORD" -X PROPFIND -H 'Depth: 1' --data '<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>' -o /dev/null -w '%{time_total}' "$BASE/")
T2=$(curl -sS -u "$USER:$PASSWORD" -X PROPFIND -H 'Depth: 1' --data '<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>' -o /dev/null -w '%{time_total}' "$BASE/")
# 用 awk 比较:第二次应 <= 第一次(命中缓存)。给宽松容差避免抖动误判。
if awk "BEGIN{exit !($T2 <= $T1 + 0.05)}"; then
    ok "  缓存命中(第1次 ${T1}s,第2次 ${T2}s)"
else
    err "  第二次更慢,缓存可能未生效(第1次 ${T1}s,第2次 ${T2}s)"
fi

# ════════════════════════════════════════════════════════
step "3. PUT 上传 → PROPFIND 可见 → GET 逐字节一致"
CONTENT="hello sail serve e2e $(date +%s)"
printf '%s' "$CONTENT" > /tmp/sail-serve-e2e-body
PUT_CODE="$(dav PUT "/$TEST_PREFIX/hello.txt" --data-binary @/tmp/sail-serve-e2e-body -o /dev/null -w '%{http_code}')"
[[ "$PUT_CODE" == "201" ]] && ok "PUT 返回 201" || err "PUT 返回 $PUT_CODE"

PF2="$(dav PROPFIND "/$TEST_PREFIX" -H 'Depth: 1' --data '<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>')"
echo "$PF2" | has 'hello.txt' && ok "  PROPFIND 可见新文件" || err "  PROPFIND 未见新文件"

GOT="$(dav GET "/$TEST_PREFIX/hello.txt")"
[[ "$GOT" == "$CONTENT" ]] && ok "  GET 内容一致" || err "  GET 内容不一致"

# ════════════════════════════════════════════════════════
step "4. Range 下载(应 206 + 正确 Content-Range)"
RANGE_HEADERS="$(dav GET "/$TEST_PREFIX/hello.txt" -H 'Range: bytes=0-4' -D - -o /dev/null)"
echo "$RANGE_HEADERS" | has '^HTTP/1.1 206' && ok "Range 返回 206" || err "Range 非 206"
echo "$RANGE_HEADERS" | has -i "content-range: bytes 0-4/$(printf '%s' "$CONTENT" | wc -c | tr -d ' ')" \
    && ok "  Content-Range 正确" || err "  Content-Range 错误"

# ════════════════════════════════════════════════════════
step "5. MOVE 改名 → 旧 404 → 新可读"
dav MOVE "/$TEST_PREFIX/hello.txt" -H "Destination: $BASE/$TEST_PREFIX/renamed.txt" -o /dev/null -w '%{http_code}' >/dev/null
OLD_CODE="$(dav GET "/$TEST_PREFIX/hello.txt" -o /dev/null -w '%{http_code}')"
NEW_CODE="$(dav GET "/$TEST_PREFIX/renamed.txt" -o /dev/null -w '%{http_code}')"
[[ "$OLD_CODE" == "404" ]] && ok "MOVE 后旧路径 404" || err "MOVE 后旧路径仍 $OLD_CODE"
[[ "$NEW_CODE" == "200" ]] && ok "  新路径可读" || err "  新路径 $NEW_CODE"

# ════════════════════════════════════════════════════════
step "6. DELETE 删除 → 404"
DEL_CODE="$(dav DELETE "/$TEST_PREFIX/renamed.txt" -o /dev/null -w '%{http_code}')"
[[ "$DEL_CODE" == "204" ]] && ok "DELETE 返回 204" || err "DELETE 返回 $DEL_CODE"
GONE_CODE="$(dav GET "/$TEST_PREFIX/renamed.txt" -o /dev/null -w '%{http_code}')"
[[ "$GONE_CODE" == "404" ]] && ok "  删除后 404" || err "  删除后仍 $GONE_CODE"

# ════════════════════════════════════════════════════════
step "7. 认证兜底:未认证请求应 401"
NOAUTH="$(curl -sS -o /dev/null -w '%{http_code}' -X PROPFIND -H 'Depth: 0' --data '<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>' "$BASE/")"
[[ "$NOAUTH" == "401" ]] && ok "未认证 PROPFIND 返回 401" || err "未认证 PROPFIND 返回 $NOAUTH"

# ════════════════════════════════════════════════════════
step "8. Content-Type 判定(无头/通用类型/显式类型/内容探测/边界)"
# ct_put <path> <header 值|-> <file>:hdr 为 - 时不发 Content-Type(实测 macOS
# 自带 WebDAV 客户端就是这个形态)。
ct_put() {
    local path="$1" hdr="$2" file="$3"
    if [[ "$hdr" == "-" ]]; then
        curl -sS -u "$USER:$PASSWORD" -X PUT -H 'Content-Type:' --data-binary "@$file" \
            -o /dev/null -w '%{http_code}' "$BASE$path"
    else
        curl -sS -u "$USER:$PASSWORD" -X PUT -H "Content-Type: $hdr" --data-binary "@$file" \
            -o /dev/null -w '%{http_code}' "$BASE$path"
    fi
}
# ct_of <path>:回读视角 —— HEAD 响应头里的 Content-Type(链接/GET 所见)。
ct_of() { curl -sS -u "$USER:$PASSWORD" -I "$BASE$1" | tr -d '\r' | sed -n 's/^[Cc]ontent-[Tt]ype: //p'; }
# ct_prop <path>:挂载视角 —— PROPFIND Depth:0 的 getcontenttype(x/net 输出带
# DAV: 命名空间前缀,形态是 <D:getcontenttype>)。
ct_prop() {
    dav PROPFIND "$1" -H 'Depth: 0' \
        --data '<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>' \
        | tr -d '\n' | grep -o 'getcontenttype>[^<]*' | sed -n '1s/^getcontenttype>//p'
}
# ct_check <描述> <path> <期望>:两个视角都应等于期望值。
ct_check() {
    local desc="$1" path="$2" want="$3" got_head got_prop
    got_head="$(ct_of "$path")"; got_prop="$(ct_prop "$path")"
    if [[ "$got_head" == "$want" && "$got_prop" == "$want" ]]; then
        ok "  $desc"
    else
        err "  $desc(HEAD=$got_head PROPFIND=$got_prop,期望 $want)"
    fi
}

printf '# note\n' > /tmp/sail-serve-e2e-note.md
printf '\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01' > /tmp/sail-serve-e2e-raw.png
: > /tmp/sail-serve-e2e-blank
CTP="/$TEST_PREFIX/ct"

# 后端能力预检:显式类型应原样回读。少数自建网关忽略请求里的 Content-Type
# 自行嗅探(实测如此),这类后端上本节无意义,整段跳过。
ct_put "$CTP/.probe" text/x-sail-probe /tmp/sail-serve-e2e-note.md >/dev/null
if [[ "$(ct_of "$CTP/.probe")" != "text/x-sail-probe" ]]; then
    skip "整个 Content-Type 段:该后端不保留客户端 Content-Type(自行嗅探),本节无意义"
else

PUT_CODE="$(ct_put "$CTP/note.md" - /tmp/sail-serve-e2e-note.md)"
[[ "$PUT_CODE" == "201" ]] && ok "无 Content-Type 的 PUT 返回 201" || err "PUT 返回 $PUT_CODE"
ct_check "无 Content-Type → 按扩展名判定为 text/markdown" "$CTP/note.md" "text/markdown; charset=utf-8"

ct_put "$CTP/generic.md" application/octet-stream /tmp/sail-serve-e2e-note.md >/dev/null
ct_check "application/octet-stream 视为没意见 → 按扩展名" "$CTP/generic.md" "text/markdown; charset=utf-8"

ct_put "$CTP/generic2.md" "Binary/Octet-Stream; charset=binary" /tmp/sail-serve-e2e-note.md >/dev/null
ct_check "通用二进制的大小写/参数变体同样归一化" "$CTP/generic2.md" "text/markdown; charset=utf-8"

ct_put "$CTP/custom.md" text/x-custom /tmp/sail-serve-e2e-note.md >/dev/null
ct_check "客户端显式类型原样保留(不被扩展名顶掉)" "$CTP/custom.md" "text/x-custom"

ct_put "$CTP/blob" - /tmp/sail-serve-e2e-raw.png >/dev/null
ct_check "无扩展名 + PNG 内容 → 内容探测出 image/png" "$CTP/blob" "image/png"

ct_put "$CTP/blank" - /tmp/sail-serve-e2e-blank >/dev/null
ct_check "边界:0 字节对象 → application/octet-stream" "$CTP/blank" "application/octet-stream"

ct_put "$CTP/blank.md" - /tmp/sail-serve-e2e-blank >/dev/null
ct_check "边界:0 字节但扩展名可判 → 仍按扩展名" "$CTP/blank.md" "text/markdown; charset=utf-8"

fi

rm -f /tmp/sail-serve-e2e-note.md /tmp/sail-serve-e2e-raw.png /tmp/sail-serve-e2e-blank

# ════════════════════════════════════════════════════════
step "9. 多用户隔离(RFC 4331 配额属性)"
# 需要一份带 serve.users 的临时配置:方式二知道 endpoint/密钥,直接写;
# 方式一(复用已有配置)则复制原配置、在目标 profile 下注入 serve 段 ——
# 凭证沿用原样(${VAR} 引用照旧由环境解析),不解析 YAML,只按行插一个缩进正确的块。
MU_LISTEN=":$(( ${LISTEN#:} + 1 ))"
MU_CFG="$(mktemp -d)/config.yaml"
MU_BASE="http://127.0.0.1:${MU_LISTEN#:}"
MU_USERS=$(cat <<EOF
      users:
        - name: alice
          password: alice-pw
          prefix: $TEST_PREFIX/alice/
          quota: 1MB
        - name: bob
          password: bob-pw
          prefix: $TEST_PREFIX/bob/
EOF
)
MU_SERVE_BLOCK=$(cat <<EOF
    serve:
      listen: "$MU_LISTEN"
      prefix: $PREFIX
$MU_USERS
EOF
)
if [[ -n "${ENDPOINT:-}" ]]; then
    cat > "$MU_CFG" <<EOF
default-profile: e2e-mu
profiles:
  e2e-mu:
    endpoint: $ENDPOINT
    access-key: $ACCESS_KEY
    secret-key: $SECRET_KEY
    bucket: "$BUCKET"
    path-style: true
$MU_SERVE_BLOCK
EOF
    MU_SAIL="$SAIL_BIN -c $MU_CFG"
else
    MU_PROF="${PROFILE:-$(sed -n 's/^default-profile:[[:space:]]*//p' "$CONFIG_FILE" | head -1)}"
    if [[ -z "$MU_PROF" ]] || ! grep -qE "^  ${MU_PROF}:[[:space:]]*$" "$CONFIG_FILE"; then
        MU_SAIL=""
        skip "多用户用例:配置里找不到 profile \"${MU_PROF:-<default>}\"(用方式二可覆盖)"
    elif awk -v prof="$MU_PROF" '$0 == "  " prof ":" {inblk=1; next} /^  [^ ]/ {inblk=0} inblk && /^    serve:/ {f=1} END{exit !f}' "$CONFIG_FILE"; then
        MU_SAIL=""
        skip "多用户用例:该 profile 已有 serve 段,不覆盖你的配置(用方式二可覆盖)"
    else
        BLK="$(mktemp)"
        printf '%s\n' "$MU_SERVE_BLOCK" > "$BLK"
        awk -v prof="$MU_PROF" -v blk="$BLK" '
            { print }
            $0 == "  " prof ":" { while ((getline l < blk) > 0) print l; close(blk) }
        ' "$CONFIG_FILE" > "$MU_CFG"
        MU_SAIL="$SAIL_BIN -c $MU_CFG -p $MU_PROF"
    fi
fi
if [[ -n "$MU_SAIL" ]]; then
    $MU_SAIL serve webdav >/tmp/sail-serve-e2e-mu.log 2>&1 &
    MU_PID=$!
    mu_ready=""
    for _ in $(seq 1 50); do
        if curl -sS -o /dev/null "$MU_BASE/" 2>/dev/null; then mu_ready=1; break; fi
        sleep 0.2
    done
    if [[ -z "$mu_ready" ]]; then
        err "多用户实例未就绪: $(tail -3 /tmp/sail-serve-e2e-mu.log)"
    else
        ok "多用户实例已监听 $MU_LISTEN"

        # 前缀隔离:alice 写,bob 读不到。
        printf 'alice-data' > /tmp/sail-serve-e2e-a
        A_PUT="$(curl -sS -u alice:alice-pw -T /tmp/sail-serve-e2e-a -o /dev/null -w '%{http_code}' "$MU_BASE/a.txt")"
        [[ "$A_PUT" == "201" ]] && ok "  alice PUT 201" || err "  alice PUT 返回 $A_PUT"
        B_GET="$(curl -sS -u bob:bob-pw -o /dev/null -w '%{http_code}' "$MU_BASE/a.txt")"
        [[ "$B_GET" == "404" ]] && ok "  bob 看不到 alice 的对象(前缀隔离)" || err "  bob 读到 alice 的对象: $B_GET"

        # 配额:1MB 上限,写 2MB 应在读 body 前被拦(507)。
        head -c 2097152 /dev/zero > /tmp/sail-serve-e2e-big
        A_BIG="$(curl -sS -u alice:alice-pw -T /tmp/sail-serve-e2e-big -o /dev/null -w '%{http_code}' "$MU_BASE/big.bin")"
        [[ "$A_BIG" == "507" ]] && ok "  超配额写入返回 507" || err "  超配额写入返回 $A_BIG(期望 507)"

        # RFC 4331:目录 PROPFIND 播报配额属性(挂载端据此显示剩余空间)。
        PFQ="$(curl -sS -u alice:alice-pw -X PROPFIND -H 'Depth: 0' \
            --data '<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>' "$MU_BASE/")"
        echo "$PFQ" | has 'quota-available-bytes' && ok "  PROPFIND 播报 quota-available-bytes" \
            || err "  PROPFIND 未播报配额属性"
    fi
    kill "$MU_PID" 2>/dev/null || true
    unset MU_PID
    rm -f /tmp/sail-serve-e2e-a /tmp/sail-serve-e2e-big
fi

# ════════════════════════════════════════════════════════
echo -e "\n${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${CYAN} 汇总  ${GREEN}通过 $pass  ${RED}失败 $fail  ${YELLOW}跳过 $skipped${NC}"
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
if [[ $fail -gt 0 ]]; then echo -e "${RED}有 $fail 项失败${NC}"; exit 1; else echo -e "${GREEN}全部通过${NC}"; exit 0; fi
