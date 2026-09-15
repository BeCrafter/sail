#!/usr/bin/env bash
#
# sail serve webdav 端到端验证脚本(真实 S3 兼容后端,读写往返)。
# 覆盖挂载视角的协议流:OPTIONS 能力探测(不带认证)→ PROPFIND 列目录 →
# GET/Range 读取 → PUT 上传 → MOVE 改名 → DELETE 删除,全程带 Basic 认证,
# 用 curl 直接对服务发 HTTP 请求,验证「挂成网络盘」这条链路的真实行为。
# 不测极限(大文件/海量对象)。
#
# 用法一(复用已有配置,推荐,不碰凭证;桶取自 profile 的 bucket,无需再指定):
#   SAIL_E2E_CONFIG=~/.sail/config.yaml SAIL_E2E_PROFILE=test ./scripts/e2e-serve.sh
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
pass=0; fail=0
ok()   { echo -e "${GREEN}[PASS]${NC} $1"; pass=$((pass+1)); }
err()  { echo -e "${RED}[FAIL]${NC} $1"; fail=$((fail+1)); }
step() { echo -e "\n${CYAN}━━━ $1 ━━━${NC}"; }

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
    WORK_DIR_CFG="$(mktemp -d)"; CONFIG_FILE="$WORK_DIR_CFG/.sail/config.yaml"
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
grep -q "mount at:" /tmp/sail-serve-e2e.log && ok "启动横幅含 mount at 地址" || err "横幅缺 mount at"

# ════════════════════════════════════════════════════════
step "1. OPTIONS 能力探测(不带认证,应 200 + DAV/Allow)"
OPT_HEADERS="$(curl -sS -i -X OPTIONS "$BASE/")"
echo "$OPT_HEADERS" | grep -q '^HTTP/1.1 200' && ok "OPTIONS 返回 200" || err "OPTIONS 非 200: $(echo "$OPT_HEADERS" | head -1)"
echo "$OPT_HEADERS" | grep -qi '^Dav: 1, 2' && ok "  带 DAV: 1,2" || err "  缺 DAV 头"
echo "$OPT_HEADERS" | grep -qi '^Allow: .*PROPFIND' && ok "  Allow 含 PROPFIND" || err "  Allow 缺 PROPFIND"

# OPTIONS *(星号):macOS Finder 挂载 WebDAV 的第一步,靠响应里的 DAV 头判定「是不是
# WebDAV 服务器」。net/http 默认会用内置 globalOptionsHandler 截胡,回空 200 无 DAV 头,
# Finder 因而卡在「连接中」——必须设 DisableGeneralOptionsHandler。用原始 TCP 才能发 `*`。
if [[ -n "$HAVE_NC" ]]; then
    OPTSTAR="$(printf 'OPTIONS * HTTP/1.1\r\nHost: 127.0.0.1:%s\r\nConnection: close\r\n\r\n' "${LISTEN#:}" \
        | nc -w 5 127.0.0.1 "${LISTEN#:}" 2>/dev/null || true)"
    echo "$OPTSTAR" | grep -q '^HTTP/1.1 200' && ok "OPTIONS * 返回 200" || err "OPTIONS * 非 200"
    echo "$OPTSTAR" | grep -qi '^Dav: 1, 2' && ok "  OPTIONS * 带 DAV 头(Finder 判定 WebDAV 的关键)" || err "  OPTIONS * 缺 DAV 头(globalOptionsHandler 未禁用?)"
else
    skip "OPTIONS * 检查(缺 nc,跳过)"
fi

# ════════════════════════════════════════════════════════
step "2. PROPFIND 根目录(带认证,应 207 且含根 href)"
PF="$(dav PROPFIND / -H 'Depth: 1' --data '<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>')"
echo "$PF" | grep -q '<D:href>/</D:href>' && ok "PROPFIND 根返回 207 含根" || err "PROPFIND 根异常: $(echo "$PF" | head -c 200)"

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
echo "$PF2" | grep -q 'hello.txt' && ok "  PROPFIND 可见新文件" || err "  PROPFIND 未见新文件"

GOT="$(dav GET "/$TEST_PREFIX/hello.txt")"
[[ "$GOT" == "$CONTENT" ]] && ok "  GET 内容一致" || err "  GET 内容不一致"

# ════════════════════════════════════════════════════════
step "4. Range 下载(应 206 + 正确 Content-Range)"
RANGE_HEADERS="$(dav GET "/$TEST_PREFIX/hello.txt" -H 'Range: bytes=0-4' -D - -o /dev/null)"
echo "$RANGE_HEADERS" | grep -q '^HTTP/1.1 206' && ok "Range 返回 206" || err "Range 非 206"
echo "$RANGE_HEADERS" | grep -qi "content-range: bytes 0-4/$(printf '%s' "$CONTENT" | wc -c | tr -d ' ')" \
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
echo -e "\n${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${CYAN} 汇总  ${GREEN}通过 $pass  ${RED}失败 $fail${NC}"
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
if [[ $fail -gt 0 ]]; then echo -e "${RED}有 $fail 项失败${NC}"; exit 1; else echo -e "${GREEN}全部通过${NC}"; exit 0; fi
