#!/usr/bin/env bash
# sail 本地构建发布 —— 不走 CI,本地 go 交叉编译 4 平台 + npm 发布。
# 版本号唯一来源仍是 git tag(只读,不创建/推送),或直接指定版本号。
#
# 发布前会:检查 npm registry/代理配置(强制走官方 registry 避免发到镜像),
# 检测到未登录时交互式 npm login,登录成功后自动继续发布。
#
# 用法:
#   ./scripts/release.sh 0.1.0            # 指定版本:编译+发布
#   ./scripts/release.sh patch            # 基于最近 tag +patch
#   ./scripts/release.sh minor | major
#   ./scripts/release.sh 0.1.0 --dry-run  # 只编译+设版本+npm publish --dry-run,不真发
#
# 环境变量:
#   NPM_REGISTRY  覆盖发布目标 registry(默认 https://registry.npmjs.org/)
# 前置: go 工具链;npm 账号需有 @becrafter scope 发布权。
# 产物: 4 个 @becrafter/sail-<os>-<cpu> 平台子包 + 主包 @becrafter/sail。

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

RED='\033[0;31m'; GREEN='\033[0;32m'; CYAN='\033[0;36m'; YELLOW='\033[0;33m'; NC='\033[0m'
info() { echo -e "${CYAN}▸${NC} $1"; }
ok()   { echo -e "${GREEN}✓${NC} $1"; }
warn() { echo -e "${YELLOW}!${NC} $1"; }
die()  { echo -e "${RED}✗${NC} $1" >&2; exit 1; }

# ── npm registry(强制官方,避免本地镜像/私服导致发到错误源)──
NPM_REGISTRY="${NPM_REGISTRY:-https://registry.npmjs.org/}"
export NPM_REGISTRY

# 检查本地 npm 配置:若默认 registry / scope 指向镜像或设了 HTTP 代理,给出提示。
# 实际发布仍强制走 NPM_REGISTRY(--registry 覆盖),保证发到官方源。
check_npm_env() {
  local def scope hp hx
  def="$(npm config get registry 2>/dev/null | tr -d '\r')"
  scope="$(npm config get @becrafter:registry 2>/dev/null | tr -d '\r')"
  # npm 对未设置的配置项可能返回 "undefined"/"null",统一视为未设置
  [[ "$def" == "undefined" || "$def" == "null" || -z "$def" ]] && def=""
  [[ "$scope" == "undefined" || "$scope" == "null" || -z "$scope" ]] && scope=""
  info "npm registry: default=${def:-<未设置>}  @becrafter:registry=${scope:-<未设置>}"
  if [[ -n "$scope" && "$scope" != "$NPM_REGISTRY" ]]; then
    warn "@becrafter:registry 指向 $scope(非官方);--registry 会覆盖,若异常请清理: npm config delete @becrafter:registry"
  elif [[ -z "$scope" && -n "$def" && "$def" != "$NPM_REGISTRY" ]]; then
    warn "默认 registry 指向 $def(非官方镜像);发布强制走官方 $NPM_REGISTRY"
  fi
  hp="$(npm config get proxy 2>/dev/null | tr -d '\r')"
  hx="$(npm config get https-proxy 2>/dev/null | tr -d '\r')"
  [[ "$hp" == "undefined" || "$hp" == "null" ]] && hp=""
  [[ "$hx" == "undefined" || "$hx" == "null" ]] && hx=""
  if [[ -n "$hp" || -n "$hx" ]]; then
    info "检测到 npm 代理: proxy=${hp:-<无>}  https-proxy=${hx:-<无>}(仅转发,不影响目标 registry)"
  fi
}

# 确保已登录 npm(官方 registry);未登录则交互式 npm login,登录后继续。
ensure_npm_login() {
  local who
  if who="$(npm whoami --registry="$NPM_REGISTRY" 2>/dev/null)" && [[ -n "$who" ]]; then
    ok "已登录 npm: $who"
    return 0
  fi
  if [[ "${CI:-}" == "true" ]]; then
    die "CI 环境 npm 未认证(whoami 失败):请确认 Secrets.NPM_TOKEN 已配置且 setup-node 用 NODE_AUTH_TOKEN 注入"
  fi
  warn "未登录 npm(官方 registry $NPM_REGISTRY),即将启动交互式登录…"
  if ! npm login --registry="$NPM_REGISTRY"; then
    die "npm login 失败,请手动执行: npm login --registry=$NPM_REGISTRY"
  fi
  who="$(npm whoami --registry="$NPM_REGISTRY" 2>/dev/null)" || who=""
  if [[ -z "$who" ]]; then
    die "登录后仍无法通过 whoami 校验,请检查账号/凭证后重试"
  fi
  ok "已登录 npm: $who"
}

DRY_RUN=false; VER_ARG=""; BUMP=""
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=true ;;
    -h|--help) sed -n '2,/^$/p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    patch|minor|major) BUMP="$arg" ;;
    *) VER_ARG="$arg" ;;
  esac
done

# ── 版本号 ──
bump_ver() {
  local base="$1" part="$2" major minor patch
  IFS=. read -r major minor patch <<< "${base#v}"
  [[ -n "${major:-}" ]] || major=
  [[ -n "${minor:-}" ]] || minor=
  [[ -n "${patch:-}" ]] || patch=
  case "$part" in
    major) major=$((major+1)); minor=0; patch=0 ;;
    minor) minor=$((minor+1)); patch=0 ;;
    patch) patch=$((patch+1)) ;;
  esac
  echo "$major.$minor.$patch"
}
LAST_TAG="$(git describe --tags --abbrev= 2>/dev/null || echo "")"
if [[ -n "$VER_ARG" ]]; then
  VERSION="${VER_ARG#v}"
elif [[ -n "$BUMP" ]]; then
  VERSION="$(bump_ver "${LAST_TAG:-v0.0.0}" "$BUMP")"
else
  die "请指定版本号或 bump 类型: ./scripts/release.sh 0.1.0 | patch | minor | major"
fi
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "非法版本号: $VERSION (需 X.Y.Z)"

# ── 预检 ──
info "版本 $VERSION (最近 tag: ${LAST_TAG:-无})$($DRY_RUN && echo ' [dry-run]')"
go build ./... || die "go build 失败"
go vet ./...   || die "go vet 失败"
node --check scripts/publish-npm.js || die "publish-npm.js 语法错误"
node --check npm/main/bin/sail.js   || die "sail.js 语法错误"
check_npm_env
if ! $DRY_RUN; then
  if [[ "${CI:-}" == "true" ]]; then
    info "CI 环境:跳过交互式登录(依赖 NODE_AUTH_TOKEN;token 无效时 npm publish 会显式报错)"
  else
    ensure_npm_login
  fi
fi

# ── 准备发布暂存区 dist/(复制模板 package.json + 编译二进制;不入源码树)──
# 源码树 npm/*/package.json 是版本占位 0.0.0 的模板,本流程只写 dist/ 副本,保持工作树干净。
# 格式: npmKey:GOOS:GOARCH:binName
PLATFORMS=(
  "darwin-arm64:darwin:arm64:sail"
  "darwin-x64:darwin:amd64:sail"
  "linux-arm64:linux:arm64:sail"
  "linux-x64:linux:amd64:sail"
)
STAGE="$ROOT/dist"
rm -rf "$STAGE"
info "暂存区: $STAGE(已 gitignore,不入库)"
info "复制模板 + go 交叉编译 4 平台 (CGO_ENABLED=0, ldflags -s -w)"
for entry in "${PLATFORMS[@]}"; do
  IFS=: read -r pkgkey goos goarch binname <<< "$entry"
  src="npm/$pkgkey"; dst="$STAGE/$pkgkey"
  mkdir -p "$dst/bin"
  cp "$src/package.json" "$dst/package.json"
  GOOS=$goos GOARCH=$goarch CGO_ENABLED=0 go build -ldflags "-s -w -X github.com/BeCrafter/sail/internal/version.Version=$VERSION" -o "$dst/bin/$binname" .
  chmod +x "$dst/bin/$binname"
  ok "built $pkgkey  ($(file -b "$dst/bin/$binname" | cut -d, -f1))"
done
# 主包:复制 package.json / bin/sail.js / README.md(保留 bin/ 结构,package.json 的 bin 指向 bin/sail.js)
mkdir -p "$STAGE/main/bin"
cp npm/main/package.json "$STAGE/main/package.json"
cp npm/main/bin/sail.js "$STAGE/main/bin/sail.js"
cp npm/main/README.md "$STAGE/main/README.md"

# ── 发布(从暂存区;源码树 package.json 保持 0.0.0 不变)──
if $DRY_RUN; then
  info "dry-run: 设版本 + npm publish --dry-run,不真发"
  node scripts/publish-npm.js "$VERSION" --dry-run --stage "$STAGE"
else
  node scripts/publish-npm.js "$VERSION" --stage "$STAGE"
fi
ok "$VERSION 本地发布完成"
