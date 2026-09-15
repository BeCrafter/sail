#!/usr/bin/env bash
#
# 检查 README 与命令清单是否同步。
#
# 三个来源必须一致,任何一处漂移都报错退出:
#   1. 真实命令树 —— 从 `sail __commands` 拿(唯一事实来源,见 cmd/root.go)
#   2. 主 README.md      —— 面向仓库/完整文档
#   3. npm/main/README.md —— 会随 npm 包一起发布的精简版
#
# 为什么需要它:npm 包的 README 是独立维护的精简版,新增命令或主
# README 改版时容易漏改,导致发布出去的包文档缺内容。本脚本把这种
# 漂移挡在 CI 上。
#
# 用法:
#   ./scripts/check-readme-sync.sh            # 默认用 ./sail
#   SAIL_BIN=/path/to/sail ./scripts/check-readme-sync.sh
#
# 退出码:0 全部同步;1 有漂移。

set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; NC='\033[0m'
fail=0
ok()  { echo -e "${GREEN}[OK]${NC}   $1"; }
err() { echo -e "${RED}[FAIL]${NC} $1"; fail=1; }

SAIL_BIN="${SAIL_BIN:-./sail}"
[[ -x "$SAIL_BIN" ]] || { echo -e "${RED}找不到可执行文件 $SAIL_BIN,请先构建${NC}"; exit 1; }

MAIN_README="README.md"
NPM_README="npm/main/README.md"
for f in "$MAIN_README" "$NPM_README"; do
  [[ -f "$f" ]] || { echo -e "${RED}缺少 $f${NC}"; exit 1; }
done

# ── 1. 真实命令清单(name<TAB>group)────────────────────────
DUMP="$("$SAIL_BIN" __commands)"
[[ -n "$DUMP" ]] || { echo -e "${RED}无法从 $SAIL_BIN 取得命令清单${NC}"; exit 1; }
COMMANDS="$(printf '%s\n' "$DUMP" | cut -f1)"
echo "真实命令清单($(printf '%s\n' "$COMMANDS" | wc -l | tr -d ' ') 个):"
printf '%s\n' "$COMMANDS" | tr '\n' ' '; echo; echo

# cmd_covered <readme> <cmd>:readme 是否提及该命令。
# 覆盖三种写法:`sail <cmd>`、反引号包住的 `<cmd>`,或表格中段 `x` / `y`。
cmd_covered() {
  local readme="$1" cmd="$2"
  grep -qE "sail ${cmd}([^a-z-]|\$)" "$readme" && return 0
  grep -qE "\`${cmd}\`" "$readme" && return 0
  return 1
}

# ── 2. 每个命令必须在两个 README 中都出现 ──────────────────
echo "── 命令覆盖检查 ──"
for cmd in $COMMANDS; do
  for readme in "$MAIN_README" "$NPM_README"; do
    cmd_covered "$readme" "$cmd" || err "$readme 未提及命令 \`$cmd\`"
  done
done
[[ $fail -eq 0 ]] && ok "所有命令均在两个 README 中出现"

# ── 3. 主 README 提到的 group 必须在 npm README 也有 ───────
# 分组是面向用户的结构。哪些组存在由真实命令树决定;若某组在主 README
# 有对应内容、npm README 却完全没有,说明精简版漏了一整类功能
# (例如曾经的 serve / WebDAV)。
echo
echo "── 命令分组一致性 ──"
# group → 该组下的命令名(空格分隔)
for gid in $(printf '%s\n' "$DUMP" | cut -f2 | sort -u); do
  # 该组是否在主 README 有实质内容:检查组内任一命令被提及
  members="$(printf '%s\n' "$DUMP" | awk -F'\t' -v g="$gid" '$2==g{print $1}')"
  main_has=0
  for m in $members; do cmd_covered "$MAIN_README" "$m" && { main_has=1; break; }; done
  [ "$main_has" -eq 1 ] || continue
  # 组内每个命令都必须在 npm README 出现,否则视为该组覆盖不全
  npm_missing=""
  for m in $members; do cmd_covered "$NPM_README" "$m" || npm_missing="$npm_missing $m"; done
  if [[ -n "$npm_missing" ]]; then
    err "分组 '$gid' 在 npm README 覆盖不全,缺少:$npm_missing"
  else
    ok "分组 '$gid' 在 npm README 覆盖完整"
  fi
done

# ── 4. 关键功能小节必须同步存在 ────────────────────────────
# 主 README 的顶级功能小节,若 npm README 完全没有对应内容,说明
# 精简版漏了新功能。
echo
echo "── 关键功能小节 ──"
for kw in "serve webdav" "WebDAV"; do
  if grep -qi "$kw" "$MAIN_README"; then
    if grep -qi "$kw" "$NPM_README"; then
      ok "npm README 覆盖了 \"$kw\""
    else
      err "主 README 有 \"$kw\" 但 npm README 完全没有"
    fi
  fi
done

echo
if [[ $fail -eq 0 ]]; then
  echo -e "${GREEN}README 同步检查通过${NC}"
  exit 0
fi
echo -e "${RED}README 同步检查失败:请补齐 npm/main/README.md(或主 README)${NC}"
exit 1
