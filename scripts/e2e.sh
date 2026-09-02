#!/usr/bin/env bash
#
# sail 端到端验证脚本(小文件、轻量)
# 覆盖:cp(本地↔s3、s3↔s3、递归、管道、dry-run、默认桶)、mv、stat、view/cat、
# ls、rm、presign、url、config(-p 指定 profile / setup 增改·重置,均无需 S3)。
# 不测极限(大文件/海量对象)。
#
# 用法:
#   # 方式一:复用已有配置(推荐,不碰凭证)
#   SAIL_E2E_CONFIG=~/.sail/config.yaml SAIL_E2E_BUCKET=<默认桶> ./scripts/e2e.sh
#   # 方式二:交互输入凭证
#   ./scripts/e2e.sh
#   # 方式三:环境变量传凭证(自建临时配置)
#   SAIL_E2E_ENDPOINT=... SAIL_E2E_ACCESS_KEY=... SAIL_E2E_SECRET_KEY=... \
#     SAIL_E2E_BUCKET=... SAIL_E2E_CDN_DOMAIN=... ./scripts/e2e.sh

set -euo pipefail
# 切到仓库根,保证相对路径(如 ./sail)无论从哪调用都正确
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; CYAN='\033[0;36m'; NC='\033[0m'
pass=0; fail=0; skipped=
ok()   { echo -e "${GREEN}[PASS]${NC} $1"; pass=$((pass+1)); }
err()  { echo -e "${RED}[FAIL]${NC} $1"; fail=$((fail+1)); }
skip() { echo -e "${YELLOW}[SKIP]${NC} $1"; skipped=$((skipped+1)); }
step() { echo -e "\n${CYAN}━━━ $1 ━━━${NC}"; }

# wait_for_list <s3uri> <substr>:轮询列举直到出现 substr 或超时。
# 兜底部分 S3 服务写入后 list 的最终一致性延迟,避免 cp 后立即 mv 漏列对象。
wait_for_list() {
  local uri="$1" needle="$2" i
  for ((i=0; i<20; i++)); do
    if $SAIL ls "$uri" 2>/dev/null | grep -q "$needle"; then return 0; fi
    sleep 0.3
  done
  return 1
}

s3exists() { $SAIL stat "$1" >/dev/null 2>&1; }
s3size()   { $SAIL stat "$1" 2>/dev/null | grep '^size:' | sed -E 's/.*\(([0-9]+) bytes\).*/\1/'; }

SAIL_BIN="${SAIL_BIN:-./sail}"
[[ -x "$SAIL_BIN" ]] || { echo -e "${RED}找不到 $SAIL_BIN,请先 go build -o sail .${NC}"; exit 1; }

# ── 配置:方式一(已有配置) / 方式二(交互) / 方式三(env) ─────
CONFIG_FILE="${SAIL_E2E_CONFIG:-}"
BUCKET="${SAIL_E2E_BUCKET:-}"

if [[ -n "$CONFIG_FILE" ]]; then
    SAIL="$SAIL_BIN -c $CONFIG_FILE"
    [[ -n "$BUCKET" ]] || { echo -e "${RED}方式一需同时指定 SAIL_E2E_BUCKET(应等于配置默认桶)${NC}"; exit 1; }
else
    ENDPOINT="${SAIL_E2E_ENDPOINT:-}"; ACCESS_KEY="${SAIL_E2E_ACCESS_KEY:-}"
    SECRET_KEY="${SAIL_E2E_SECRET_KEY:-}"; CDN_DOMAIN="${SAIL_E2E_CDN_DOMAIN:-}"
    PROFILE="${SAIL_E2E_PROFILE:-e2e-test}"; REGION="${SAIL_E2E_REGION:-}"
    PATH_STYLE="${SAIL_E2E_PATH_STYLE:-true}"
    if [[ -z "$ENDPOINT" ]]; then
        echo -e "${CYAN}sail 端到端验证${NC}"; echo "请提供测试环境的 S3 凭证:"
        read -rp "endpoint: " ENDPOINT; read -rp "access-key: " ACCESS_KEY
        read -rp "secret-key: " SECRET_KEY; read -rp "bucket: " BUCKET
        read -rp "CDN 域名 (可留空): " CDN_DOMAIN
    fi
    [[ -z "$ENDPOINT" || -z "$ACCESS_KEY" || -z "$SECRET_KEY" || -z "$BUCKET" ]] && { echo -e "${RED}endpoint/access-key/secret-key/bucket 不能为空${NC}"; exit 1; }
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
    region: "$REGION"
    path-style: $PATH_STYLE
    cdn-domain: "$CDN_DOMAIN"
EOF
    SAIL="$SAIL_BIN -c $CONFIG_FILE"
fi

# ── 临时目录与测试前缀 ───────────────────────────────────
WORK_DIR="$(mktemp -d)"; TEST_DIR="$WORK_DIR/testdata"; PREFIX="sail-e2e-$(date +%s)"
mkdir -p "$TEST_DIR/subdir"
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${CYAN} sail 端到端验证  桶=$BUCKET  前缀=$PREFIX${NC}"
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"

cleanup() { echo -e "\n${CYAN}━━━ 清理 ━━━${NC}"; $SAIL rm -r "s3://$BUCKET/$PREFIX/" 2>/dev/null || true; rm -rf "$WORK_DIR" "${WORK_DIR_CFG:-}" 2>/dev/null || true; echo -e "${GREEN}清理完成${NC}"; }
trap cleanup EXIT

# ── 测试文件(小文件) ───────────────────────────────────
echo "hello sail e2e test" > "$TEST_DIR/small.txt"
printf '{"b":2,"a":1,"nested":{"x":[1,2,3]}}' > "$TEST_DIR/test.json"
printf 'name,age\nalice,30\nbob,25\n' > "$TEST_DIR/test.csv"
echo "nested file" > "$TEST_DIR/subdir/nested.txt"
echo "mv local src" > "$TEST_DIR/mv-local.txt"

# ════════════════════════════════════════════════════════
step "1. ls — 列举(验证连接)"
$SAIL cp "$TEST_DIR/small.txt" "s3://$BUCKET/$PREFIX/small.txt" >/dev/null 2>&1
$SAIL ls "s3://$BUCKET/$PREFIX/" 2>&1 | grep -q "small.txt" && ok "ls 列举成功" || err "ls 未找到对象"
$SAIL ls -l "s3://$BUCKET/$PREFIX/" 2>&1 | grep -q "small.txt" && ok "ls -l 长格式正确" || err "ls -l 失败"

# ════════════════════════════════════════════════════════
step "2. cp 本地→s3(显式桶 + upload 别名 + s3:/// 默认桶)"
$SAIL cp "$TEST_DIR/test.json" "s3://$BUCKET/$PREFIX/test.json" >/dev/null 2>&1 && ok "cp 本地→s3 显式桶" || err "cp 本地→s3 失败"
s3exists "s3://$BUCKET/$PREFIX/test.json" && ok "  对象存在" || err "  对象未找到"
$SAIL upload "$TEST_DIR/test.csv" "s3://$BUCKET/$PREFIX/test.csv" >/dev/null 2>&1 && ok "  upload 别名可用" || err "  upload 别名失败"
$SAIL cp "$TEST_DIR/small.txt" "s3:///$PREFIX/default.txt" >/dev/null 2>&1 && ok "  cp s3:/// 默认桶" || err "  cp s3:/// 失败"
s3exists "s3://$BUCKET/$PREFIX/default.txt" && ok "  s3:/// 落到默认桶正确" || err "  s3:/// 未落到默认桶"

# ════════════════════════════════════════════════════════
step "3. cp -r 目录递归 + 管道输入"
$SAIL cp -r "$TEST_DIR" "s3://$BUCKET/$PREFIX/dir/" >/dev/null 2>&1 && ok "cp -r 目录递归成功" || err "cp -r 目录递归失败"
$SAIL ls "s3://$BUCKET/$PREFIX/dir/" 2>&1 | grep -q "subdir/nested.txt" && ok "  递归保留子目录结构" || err "  递归未保留子目录"
echo "pipe content" | $SAIL cp - "s3://$BUCKET/$PREFIX/pipe.txt" >/dev/null 2>&1 && ok "  cp - 管道输入" || err "  cp - 管道失败"
s3exists "s3://$BUCKET/$PREFIX/pipe.txt" && ok "  管道对象存在" || err "  管道对象未找到"

# ════════════════════════════════════════════════════════
step "ls -d + tree(目录列举与树形)"
# ls -d:只列 dir 下的子目录(testdata 有 subdir/)
$SAIL ls -d "s3://$BUCKET/$PREFIX/dir/" 2>&1 | grep -q "^subdir/$" && ok "ls -d 只列子目录" || err "ls -d 未正确列子目录"
# ls -d -l:-d 忽略 -l,仍只列目录
$SAIL ls -d -l "s3://$BUCKET/$PREFIX/dir/" 2>&1 | grep -q "^subdir/$" && ok "ls -d -l 仍只列目录(-d 忽略 -l)" || err "ls -d -l 异常"
# ls -d 无尾斜杠:prefix 不强制以 / 结尾,仍只列子目录(回归:曾因缺尾斜杠输出空)
$SAIL ls -d "s3://$BUCKET/$PREFIX/dir" 2>&1 | grep -q "^subdir/$" && ok "ls -d 无尾斜杠仍只列子目录" || err "ls -d 无尾斜杠未正确列子目录"
# tree:含子目录与文件
TREE_OUT=$($SAIL tree "s3://$BUCKET/$PREFIX/dir/" 2>&1)
echo "$TREE_OUT" | grep -q "subdir/" && ok "tree 含子目录" || err "tree 未含子目录"
echo "$TREE_OUT" | grep -q "nested.txt" && ok "tree 含文件" || err "tree 未含文件"
# tree -d:只目录,不含文件
$SAIL tree -d "s3://$BUCKET/$PREFIX/dir/" 2>&1 | grep -q "small.txt" && err "tree -d 不应含文件" || ok "tree -d 不含文件"
# tree -L 1:深度 1,截断深层(nested.txt 在 subdir/ 下,深度 2)
$SAIL tree -L 1 "s3://$BUCKET/$PREFIX/dir/" 2>&1 | grep -q "nested.txt" && err "tree -L 1 不应含深层文件" || ok "tree -L 1 截断深层"
# tree -s --human:文件附人类可读大小
$SAIL tree -s --human "s3://$BUCKET/$PREFIX/dir/" 2>&1 | grep -qE "small.txt +[0-9]" && ok "tree -s --human 带大小" || err "tree -s --human 异常"
# 本地 tree
$SAIL tree "$TEST_DIR" 2>&1 | grep -q "subdir/" && ok "tree 本地目录" || err "tree 本地目录异常"

# ════════════════════════════════════════════════════════
step "4. cp s3→本地 + 内容一致性"
$SAIL cp "s3://$BUCKET/$PREFIX/small.txt" "$WORK_DIR/dl.txt" >/dev/null 2>&1 && ok "cp s3→本地" || err "cp s3→本地失败"
diff "$TEST_DIR/small.txt" "$WORK_DIR/dl.txt" >/dev/null 2>&1 && ok "  下载内容一致" || err "  下载内容不一致"

# ════════════════════════════════════════════════════════
step "5. cp s3→s3(CopyObject 回退,大小一致)"
$SAIL cp "s3://$BUCKET/$PREFIX/small.txt" "s3://$BUCKET/$PREFIX/copy.txt" >/dev/null 2>&1 && ok "cp s3→s3" || err "cp s3→s3 失败"
SRC_SZ=$(s3size "s3://$BUCKET/$PREFIX/small.txt"); DST_SZ=$(s3size "s3://$BUCKET/$PREFIX/copy.txt")
if [[ -n "$SRC_SZ" && "$SRC_SZ" == "$DST_SZ" ]]; then ok "  复制大小一致($DST_SZ bytes,回退正确)"; else err "  大小不一致(src=$SRC_SZ dst=$DST_SZ)"; fi

# ════════════════════════════════════════════════════════
step "6. cp --dry-run(预演不写入)"
$SAIL cp --dry-run "$TEST_DIR/small.txt" "s3://$BUCKET/$PREFIX/dryrun.txt" 2>&1 | grep -q "将复制" && ok "dry-run 打印预演" || err "dry-run 未打印预演"
s3exists "s3://$BUCKET/$PREFIX/dryrun.txt" && err "dry-run 不应写入对象" || ok "dry-run 未写入对象"

# ════════════════════════════════════════════════════════
step "7. mv(复制+删源)"
$SAIL mv "s3://$BUCKET/$PREFIX/copy.txt" "s3://$BUCKET/$PREFIX/moved.txt" >/dev/null 2>&1 && ok "mv s3→s3" || err "mv s3→s3 失败"
s3exists "s3://$BUCKET/$PREFIX/moved.txt" && ok "  目标存在" || err "  目标未生成"
s3exists "s3://$BUCKET/$PREFIX/copy.txt" || ok "  源已删除" || err "  源未删除"
$SAIL mv "$TEST_DIR/mv-local.txt" "s3://$BUCKET/$PREFIX/mv-local.txt" >/dev/null 2>&1 && ok "mv 本地→s3" || err "mv 本地→s3 失败"
[[ ! -f "$TEST_DIR/mv-local.txt" ]] && ok "  本地源已删" || err "  本地源未删"
$SAIL mv "s3://$BUCKET/$PREFIX/mv-local.txt" "$WORK_DIR/mv-back.txt" >/dev/null 2>&1 && ok "mv s3→本地" || err "mv s3→本地 失败"
s3exists "s3://$BUCKET/$PREFIX/mv-local.txt" || ok "  s3 源已删" || err "  s3 源未删"

# ════════════════════════════════════════════════════════
step "8. mv -r 递归(非 TTY 用 --yes)"
# cp -r s3→s3:先确认源复制成功(此前未校验,失败会被后续 mv 误判为"目标为空")
$SAIL cp -r "s3://$BUCKET/$PREFIX/dir/" "s3://$BUCKET/$PREFIX/dir3/" >/dev/null 2>&1 && ok "cp -r s3→s3(dir/→dir3/)" || err "cp -r s3→s3 失败(dir/→dir3/)"
# 兜底:部分 S3 服务写入后 list 有最终一致性延迟,等 dir3/ 可见再 mv
wait_for_list "s3://$BUCKET/$PREFIX/dir3/" "nested.txt"
$SAIL mv -r --yes "s3://$BUCKET/$PREFIX/dir3/" "s3://$BUCKET/$PREFIX/dir4/" >/dev/null 2>&1 && ok "mv -r --yes" || err "mv -r --yes 失败"
$SAIL ls "s3://$BUCKET/$PREFIX/dir4/" 2>&1 | grep -q "nested.txt" && ok "  目标含文件" || err "  目标为空"
[[ -z "$($SAIL ls "s3://$BUCKET/$PREFIX/dir3/" 2>/dev/null)" ]] && ok "  源已清空" || err "  源未清空"

# ════════════════════════════════════════════════════════
step "9. stat(s3 + 本地 + s3:/// 默认桶)"
STAT_OUT=$($SAIL stat "s3://$BUCKET/$PREFIX/small.txt" 2>&1)
echo "$STAT_OUT" | grep -q "^key: s3://$BUCKET/$PREFIX/small.txt" && ok "stat s3 key 正确" || err "stat s3 key 错误"
echo "$STAT_OUT" | grep -q "^etag:" && ok "  含 etag" || err "  缺 etag"
$SAIL stat "s3:///$PREFIX/small.txt" 2>&1 | grep -q "^key: s3://$BUCKET/$PREFIX/small.txt" && ok "stat s3:/// 默认桶填充正确" || err "stat s3:/// 默认桶错误"
STAT_LOC=$($SAIL stat "$TEST_DIR/small.txt" 2>&1) || true
if echo "$STAT_LOC" | grep -q "^name: small.txt"; then ok "stat 本地文件正确"; else err "stat 本地文件错误 (exists=$([[ -f "$TEST_DIR/small.txt" ]] && echo y || echo n)): $STAT_LOC"; fi
$SAIL stat "$TEST_DIR" 2>&1 | grep -q "^is-dir: true" && ok "stat 本地目录 is-dir 正确" || err "stat 本地目录错误"

# ════════════════════════════════════════════════════════
step "10. view(文本/json/csv)+ cat 别名"
$SAIL view "s3://$BUCKET/$PREFIX/small.txt" 2>&1 | grep -q "hello sail e2e test" && ok "view 文本正确" || err "view 文本错误"
$SAIL view "s3://$BUCKET/$PREFIX/test.json" > "$WORK_DIR/v.json" 2>&1
[[ $(wc -l < "$WORK_DIR/v.json") -gt 1 ]] && ok "view json 美化(多行)" || err "view json 未美化"
$SAIL view "s3://$BUCKET/$PREFIX/test.csv" 2>&1 | grep -q "alice" && ok "view csv 输出" || err "view csv 异常"
$SAIL cat "s3://$BUCKET/$PREFIX/test.json" > "$WORK_DIR/c.json" 2>&1
grep -q '^{"b":2,"a":1,.*}$' "$WORK_DIR/c.json" && ok "cat 原样输出(单行)" || err "cat 非原样"
diff "$WORK_DIR/c.json" <($SAIL view --raw "s3://$BUCKET/$PREFIX/test.json" 2>&1) >/dev/null 2>&1 && ok "cat == view --raw" || err "cat != view --raw"

# ════════════════════════════════════════════════════════
step "11. url(校验含 bucket)+ presign"
URL_OUT=$($SAIL url "s3://$BUCKET/$PREFIX/small.txt" 2>&1) || true
if echo "$URL_OUT" | grep -q "^https\?://"; then
    echo "$URL_OUT" | grep -q "/$BUCKET/" && ok "url 含 bucket" || err "url 缺 bucket"
    $SAIL url "s3:///$PREFIX/small.txt" 2>&1 | grep -q "/$BUCKET/" && ok "url s3:/// 默认桶含 bucket" || err "url s3:/// 缺 bucket"
    $SAIL url "s3://$BUCKET/$PREFIX/small.txt" --cdn "https://ov.example.com" 2>&1 | grep -q "ov.example.com" && ok "url --cdn 覆盖" || err "url --cdn 失败"
else
    skip "url 未配置 cdn-domain(跳过)"
fi

# ── url bucket 去重(不依赖 config cdn-domain):自动检测 / --no-bucket / cdn-bucket-path ──
# 自动检测:--cdn 域名已含 bucket 则不重复拼接
AUTO_URL=$($SAIL url "s3://$BUCKET/$PREFIX/small.txt" --cdn "https://ov.example.com/$BUCKET" 2>&1) || true
if echo "$AUTO_URL" | grep -qF "https://ov.example.com/$BUCKET/$PREFIX/small.txt" && ! echo "$AUTO_URL" | grep -qF "/$BUCKET/$BUCKET/"; then
    ok "url 域名含 bucket 自动去重"
else
    err "url 自动去重失败: $AUTO_URL"
fi
# --no-bucket:显式不追加 bucket(覆盖自动检测,域名不含 bucket 也不追加)
NOB_URL=$($SAIL url "s3://$BUCKET/$PREFIX/small.txt" --cdn "https://ov.example.com" --no-bucket 2>&1) || true
if echo "$NOB_URL" | grep -qF "https://ov.example.com/$PREFIX/small.txt" && ! echo "$NOB_URL" | grep -qF "/$BUCKET/"; then
    ok "url --no-bucket 不追加 bucket"
else
    err "url --no-bucket 失败: $NOB_URL"
fi
# cdn-bucket-path 配置字段(url 不连 S3,用临时配置)
gen_cdn_cfg() {
    cat > "$WORK_DIR/cdn-config.yaml" <<EOF
default-profile: e2e-cdn
profiles:
  e2e-cdn:
    endpoint: https://s3.example.com
    access-key: e2e-dummy
    secret-key: e2e-dummy
    bucket: "$BUCKET"
    path-style: true
    cdn-domain: "https://ov.example.com/$BUCKET"
    cdn-bucket-path: $1
EOF
}
gen_cdn_cfg "true"
CFG_TRUE=$("$SAIL_BIN" -c "$WORK_DIR/cdn-config.yaml" url "s3://$BUCKET/$PREFIX/small.txt" 2>&1)
if echo "$CFG_TRUE" | grep -qF "https://ov.example.com/$BUCKET/$PREFIX/small.txt" && ! echo "$CFG_TRUE" | grep -qF "/$BUCKET/$BUCKET/"; then
    ok "url cdn-bucket-path:true 去重(覆盖自动检测)"
else
    err "url cdn-bucket-path:true 失败: $CFG_TRUE"
fi
gen_cdn_cfg "false"
CFG_FALSE=$("$SAIL_BIN" -c "$WORK_DIR/cdn-config.yaml" url "s3://$BUCKET/$PREFIX/small.txt" 2>&1)
if echo "$CFG_FALSE" | grep -qF "/$BUCKET/$BUCKET/"; then
    ok "url cdn-bucket-path:false 强制追加(覆盖自动检测)"
else
    err "url cdn-bucket-path:false 失败: $CFG_FALSE"
fi

PS_OUT=$($SAIL presign "s3://$BUCKET/$PREFIX/small.txt" 2>&1) || true
if echo "$PS_OUT" | grep -q "X-Amz-Signature"; then
    echo "$PS_OUT" | grep -q "/$BUCKET/" && ok "presign 含 bucket" || err "presign 缺 bucket"
elif echo "$PS_OUT" | grep -qi "error"; then
    skip "presign 服务端不支持(部分 S3 兼容服务不支持 query string 认证)"
else
    err "presign 输出异常: $PS_OUT"
fi

# ════════════════════════════════════════════════════════
step "12. rm + rm -r"
$SAIL rm "s3://$BUCKET/$PREFIX/pipe.txt" >/dev/null 2>&1 && ok "rm 删除 pipe.txt" || err "rm 删除失败"
s3exists "s3://$BUCKET/$PREFIX/pipe.txt" && err "  rm 后仍存在" || ok "  rm 后已删除"
$SAIL rm -r "s3://$BUCKET/$PREFIX/dir4/" >/dev/null 2>&1 && ok "rm -r 删除 dir4" || err "rm -r 失败"
[[ -z "$($SAIL ls "s3://$BUCKET/$PREFIX/dir4/" 2>/dev/null)" ]] && ok "  rm -r 后已清空" || err "  rm -r 后有残留"

# ════════════════════════════════════════════════════════
step "13. 错误处理"
$SAIL stat "s3://$BUCKET/$PREFIX/__nope__" >/dev/null 2>&1 && err "stat 不应找到不存在对象" || ok "stat 不存在对象正确报错"
$SAIL cp "$TEST_DIR/small.txt" "$WORK_DIR/should-fail.txt" >/dev/null 2>&1 && err "cp 本地→本地不应成功" || ok "cp 本地→本地正确拒绝"
$SAIL mv -r "s3://$BUCKET/$PREFIX/dir/" "s3://$BUCKET/$PREFIX/dir5/" </dev/null >/dev/null 2>&1 && err "mv -r 非 TTY 无 --yes 不应成功" || ok "mv -r 非 TTY 无 --yes 正确拒绝"
# ════════════════════════════════════════════════════════
step "14. config: 多 profile 指定(-p)与 setup 增改/重置(无需 S3)"

# ── 多 profile:无 -p(默认)与 -p 指定 ──
cat > "$WORK_DIR/multi-cfg.yaml" <<EOF
default-profile: prod
profiles:
  prod:
    endpoint: https://s3.example.com
    access-key: ak
    secret-key: sk
    bucket: "bucket-a"
    region: "us-east-1"
    path-style: true
    cdn-domain: "https://cdn-prod.example.com"
  test:
    endpoint: https://s3-test.example.com
    access-key: ak
    secret-key: sk
    bucket: "bucket-b"
    region: "us-east-1"
    path-style: true
    cdn-domain: "https://cdn-test.example.com"
EOF
DEF_URL=$("$SAIL_BIN" -c "$WORK_DIR/multi-cfg.yaml" url "s3://x/key" 2>&1)
echo "$DEF_URL" | grep -qF "cdn-prod.example.com" && ok "无 -p 用 default-profile(prod)" || err "无 -p 未用默认 profile: $DEF_URL"
TST_URL=$("$SAIL_BIN" -c "$WORK_DIR/multi-cfg.yaml" -p test url "s3://x/key" 2>&1)
echo "$TST_URL" | grep -qF "cdn-test.example.com" && ok "-p test 指定 test profile" || err "-p test 未生效: $TST_URL"
PRD_URL=$("$SAIL_BIN" -c "$WORK_DIR/multi-cfg.yaml" -p prod url "s3://x/key" 2>&1)
echo "$PRD_URL" | grep -qF "cdn-prod.example.com" && ok "-p prod 指定 prod profile" || err "-p prod 未生效: $PRD_URL"
if "$SAIL_BIN" -c "$WORK_DIR/multi-cfg.yaml" -p nosuch url "s3://x/key" >/dev/null 2>&1; then
    err "-p 不存在的 profile 不应成功"
else
    ok "-p 不存在的 profile 正确报错"
fi

# ── config setup 交互式新建(单 profile,自动设为默认) ──
printf 'prod\nhttps://s3.example.com\nak\nsk\nbucket-a\n\nus-east-1\ny\n\n' | \
    SHELL= "$SAIL_BIN" -c "$WORK_DIR/setup-new.yaml" config setup >/dev/null 2>&1
grep -q 'default-profile: prod' "$WORK_DIR/setup-new.yaml" && ok "setup 新建默认=prod" || err "setup 新建未设默认"
grep -q '  prod:' "$WORK_DIR/setup-new.yaml" && ok "setup 新建含 prod" || err "setup 新建缺 prod"

# ── setup 新增 profile,保留现有(默认不变) ──
cat > "$WORK_DIR/add-cfg.yaml" <<EOF
default-profile: prod
profiles:
  prod:
    endpoint: https://s3.example.com
    access-key: ak
    secret-key: sk
    bucket: "bucket-a"
    region: "us-east-1"
    path-style: true
    cdn-domain: ""
EOF
printf 'test\nhttps://s3-test.example.com\nak\nsk\nbucket-b\n\nus-east-1\ny\n\n' | \
    SHELL= "$SAIL_BIN" -c "$WORK_DIR/add-cfg.yaml" config setup >/dev/null 2>&1
grep -q '  prod:' "$WORK_DIR/add-cfg.yaml" && ok "setup 增 profile 保留 prod" || err "setup 增 profile 丢 prod"
grep -q '  test:' "$WORK_DIR/add-cfg.yaml" && ok "setup 增 profile 加 test" || err "setup 增 profile 未加 test"
grep -q 'bucket: "bucket-a"' "$WORK_DIR/add-cfg.yaml" && ok "setup 增 profile 保留 prod.bucket" || err "setup 增 profile 改动了 prod.bucket"
grep -q 'default-profile: prod' "$WORK_DIR/add-cfg.yaml" && ok "setup 增 profile 默认不变" || err "setup 增 profile 默认被改"

# ── 把新增 profile 设为默认(promote) ──
printf 'test\nhttps://s3-test.example.com\nak\nsk\nbucket-b\n\nus-east-1\ny\ny\n' | \
    SHELL= "$SAIL_BIN" -c "$WORK_DIR/add-cfg.yaml" config setup >/dev/null 2>&1
grep -q 'default-profile: test' "$WORK_DIR/add-cfg.yaml" && ok "setup 可把新 profile 设为默认" || err "setup 未能把新 profile 设为默认"
grep -q '  prod:' "$WORK_DIR/add-cfg.yaml" && grep -q '  test:' "$WORK_DIR/add-cfg.yaml" && ok "promote 后两 profile 均在" || err "promote 后 profile 缺失"

# ── setup --reset:2 profile → 单 profile ──
cat > "$WORK_DIR/reset-cfg.yaml" <<EOF
default-profile: prod
profiles:
  prod:
    endpoint: https://s3.example.com
    access-key: ak
    secret-key: sk
    bucket: "bucket-a"
    region: "us-east-1"
    path-style: true
    cdn-domain: ""
  test:
    endpoint: https://s3-test.example.com
    access-key: ak
    secret-key: sk
    bucket: "bucket-b"
    region: "us-east-1"
    path-style: true
    cdn-domain: ""
EOF
printf 'prod\nhttps://s3.example.com\nak\nsk\nbucket-a\n\nus-east-1\ny\n\n' | \
    SHELL= "$SAIL_BIN" -c "$WORK_DIR/reset-cfg.yaml" config setup --reset >/dev/null 2>&1
if grep -q '  prod:' "$WORK_DIR/reset-cfg.yaml" && ! grep -q '  test:' "$WORK_DIR/reset-cfg.yaml"; then
    ok "setup --reset 只剩单 profile"
else
    err "setup --reset 未清空"
fi
grep -q 'default-profile: prod' "$WORK_DIR/reset-cfg.yaml" && ok "setup --reset 默认=prod" || err "setup --reset 默认错误"

# ── setup 留空 ak/sk:写入按 profile 派生的占位符,结尾输出摘要与 export 指引 ──
SETUP_OUT=$(printf 'prod\nhttps://s3.example.com\n\n\nbucket-a\n\nus-east-1\ny\n\n' | \
    SHELL= "$SAIL_BIN" -c "$WORK_DIR/placeholder-cfg.yaml" config setup 2>&1)
if grep -qF 'access-key: ${SAIL_PROD_ACCESS_KEY}' "$WORK_DIR/placeholder-cfg.yaml" \
    && grep -qF 'secret-key: ${SAIL_PROD_SECRET_KEY}' "$WORK_DIR/placeholder-cfg.yaml"; then
    ok "setup 留空 ak/sk 写派生占位符"
else
    err "setup 留空 ak/sk 未写派生占位符"
fi
if echo "$SETUP_OUT" | grep -q '配置摘要' \
    && echo "$SETUP_OUT" | grep -q 'export SAIL_PROD_ACCESS_KEY=' \
    && echo "$SETUP_OUT" | grep -q '缺少 access-key/secret-key'; then
    ok "setup 结尾输出配置摘要与 export 指引"
else
    err "setup 结尾缺配置摘要/export 指引"
fi

# ── setup endpoint 留空原地重问(1 次空后给有效值) ──
printf 'prod\n\nhttps://s3.example.com\nak\nsk\nbucket-a\n\nus-east-1\ny\n\n' | \
    SHELL= "$SAIL_BIN" -c "$WORK_DIR/retry-cfg.yaml" config setup >/dev/null 2>&1
grep -qF 'endpoint: https://s3.example.com' "$WORK_DIR/retry-cfg.yaml" \
    && ok "setup endpoint 留空重问后写入" || err "setup endpoint 重问未生效"

# ── setup endpoint 连续 3 次留空:报错退出且不写盘 ──
rm -f "$WORK_DIR/fail-cfg.yaml"
if printf 'prod\n\n\n\n' | SHELL= "$SAIL_BIN" -c "$WORK_DIR/fail-cfg.yaml" config setup >/dev/null 2>&1; then
    err "setup endpoint 连续留空应报错退出"
else
    ok "setup endpoint 连续留空正确报错"
fi
if [[ ! -f "$WORK_DIR/fail-cfg.yaml" ]]; then
    ok "setup 报错退出时未写盘"
else
    err "setup 报错退出时不应写盘"
fi

# ════════════════════════════════════════════════════════
echo -e "\n${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${CYAN} 汇总  ${GREEN}通过 $pass  ${RED}失败 $fail  ${YELLOW}跳过 $skipped${NC}"
echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
if [[ $fail -gt 0 ]]; then echo -e "${RED}有 $fail 项失败${NC}"; exit 1; else echo -e "${GREEN}全部通过${NC}"; exit 0; fi
