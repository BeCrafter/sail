#!/usr/bin/env bash
#
# 检查三个 README 与真实命令树/帮助是否同步。任何一处漂移都报错退出:
#   1. 真实命令树 —— `sail __commands` 与各层 `--help` 的 Available Commands(唯一事实来源)
#   2. README.md        —— 英文完整文档
#   3. README.zh-CN.md  —— 中文完整文档(与英文逐项对等)
#   4. npm/main/README.md —— 随 npm 包发布的精简版(链回主 README 看细节)
#
# 为什么需要它:npm 包 README 独立维护、中文 README 单独成篇,新增命令/子命令或
# 改版时都容易漏改;README 里也可能残留已改名的命令或旗标。本脚本把这几类漂移挡在 CI 上。
#
# 注意:主 README 的 Usage 是「示例导览」而非逐旗标参考,完整旗标表以 `sail <cmd> --help`
# 为准(三份 README 都写明了这一点);因此这里只校验示例中用到的命令/子命令/旗标真实
# 存在,不要求 README 列出全部旗标。命令与旗标的「全量覆盖」由 cmd/help_coverage_test.go
# 与 cmd/i18n_coverage_test.go 在 go test 中守卫。
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
ZH_README="README.zh-CN.md"
NPM_README="npm/main/README.md"
for f in "$MAIN_README" "$ZH_README" "$NPM_README"; do
  [[ -f "$f" ]] || { echo -e "${RED}缺少 $f${NC}"; exit 1; }
done

# ── 1. 真实命令清单(name<TAB>group<TAB>aliases)─────────────
DUMP="$("$SAIL_BIN" __commands)"
[[ -n "$DUMP" ]] || { echo -e "${RED}无法从 $SAIL_BIN 取得命令清单${NC}"; exit 1; }
COMMANDS="$(printf '%s\n' "$DUMP" | cut -f1)"
echo "真实命令清单($(printf '%s\n' "$COMMANDS" | wc -l | tr -d ' ') 个):"
printf '%s\n' "$COMMANDS" | tr '\n' ' '; echo; echo


# help_subs <cmd> <sub?>:该命令 --help 里 Available Commands 列出的子命令。
# 三级命令固定为 `sail <cmd> <sub>` 形态(空格分隔,不引号)。
help_subs() {
  "$SAIL_BIN" $* --help --lang en 2>&1 |
    awk '/^Available Commands:/{f=1; next} /^$/{f=0} f&&/^  /{print $1}'
}

# build_flag_index:把「命令<TAB>旗标」全表算一次(命令数 × 一次 --help),写进 $FLAG_INDEX。
# 必须在主 shell 里调用:若包成函数,FLAG_INDEX 只会落在函数作用域里,返回即丢
# (命令替换还会把索引一并吞掉)。逐条查表时再起进程会慢到不可用,故预计算。
# 每行旗标形如 "-r, --recursive" / "--buckets",短长两种写法都入表——
# 文档里两种都会用,少记一种就会把合法写法误报成漂移。
build_flag_index() {
  local c
  while IFS= read -r c; do
    "$SAIL_BIN" $c --help --lang en 2>&1 | awk -v c="$c" '
      /^Flags:|^Global Flags:/{f=1; next}
      f && /^[^[:space:]]/{f=0}
      f && /^[[:space:]]+-/{
        line=$0; sub(/^[[:space:]]+/, "", line)
        n=split(line, p, /[[:space:]]+/)
        s=p[1]; sub(/,$/, "", s)
        if (s ~ /^-[^-]/) {                    # 短选项:与紧跟的长选项同属一条
          print c "\t" s
          if (n>1 && p[2] ~ /^--/) { l=p[2]; sub(/,$/, "", l); print c "\t" l }
        } else if (s ~ /^--/) print c "\t" s
      }'
  done < <(tree_cmds)
}

# all_flags <cmd> <sub?>:该命令可用旗标(去重),查预计算表
all_flags() {
  local cmd="$*"
  awk -F'\t' -v c="$cmd" '$1==c{print $2}' "$FLAG_INDEX" | sort -u
}

# tree_cmds:命令树里所有可见命令(含子命令),每行一个。
# 子命令从各层 --help 动态派生,不写死清单——写死的话,新增子命令会静默绕过
# 下面所有检查(第 2 节的 README 覆盖就再也管不到它),而这份脚本存在的意义
# 正是拦住这类漂移。参见 cmd/help_coverage_test.go 的同源保证。
#
# 每次调用要起 O(命令数) 个子进程,而调用方在逐词循环里,故结果缓存在
# TREE_CMDS 里:同一进程内命令树不会变,算一次就够(不加缓存会慢到不可用)。
tree_cmds() {
  [[ -n "${TREE_CMDS:-}" ]] && { printf '%s\n' "$TREE_CMDS"; return 0; }
  local out
  out="$(printf '%s\n' "$COMMANDS"
    for c in $COMMANDS; do
      for sub in $(help_subs "$c"); do
        [[ "$sub" == "help" ]] && continue
        printf '%s %s\n' "$c" "$sub"
      done
    done)"
  TREE_CMDS="$out"
  printf '%s\n' "$out"
}

# 预热命令树缓存。tree_cmds 的调用点几乎都在子 shell 里(< <(...) 或 $(...)),
# 子 shell 里设的变量回不到父 shell,缓存会一次次失效、命令树被反复重建
# (每建一次要起 O(命令数) 个 sail --help,是脚本耗时的大头)。这里在主 shell
# 先算一次,后续调用就都命中缓存了。
tree_cmds > /dev/null

# tree_aliases:命令别名(upload/download/cat 等),README 用它们举例属正常
tree_aliases() {
  printf '%s\n' "$DUMP" | awk -F'\t' 'NF>=3 && $3!=""{n=split($3,a,",");for(i=1;i<=n;i++)print a[i]}'
}

# doc_sail_tokens <readme>:代码块里每处 `sail` 调用起的空白切词(去掉行尾注释)。
# doc_cmds / doc_flags 都由它派生,避免两处各写一遍同一段 awk 而漂移。
doc_sail_tokens() {
  awk '
       function emit(line, n, w, i, k, out, at_command_start) {
         n=split(line, w)
         for(i=1;i<=n;i++) if(w[i]=="sail"){
           # 只把命令位置的 sail 当作调用。否则 `cd sail && go build -o sail .`
           # 会把路径/参数里的 sail 误识别成命令；管道和 shell 链接后的 sail
           # 仍会被识别,例如 `sail ls | sail rm -r -`。
           at_command_start = (i == 1 || w[i-1] ~ /^(\||\|\||&&|;|&|\|&)$/)
           if (!at_command_start) continue
           out=""
           for(k=i; k<=n; k++) out = out (out==""?"":" ") w[k]
           print out
         }
       }
       /^[[:space:]]*```/{inf=!inf; next}
       !inf{next}
       {
         line=$0
         sub(/#.*/, "", line)           # 剥掉行尾注释,避免把注释里的 sail 当调用
         if (line ~ /\\[[:space:]]*$/) { # 反斜杠续行:续上下一行再切词
           sub(/[[:space:]]*$/, "", line)
           sub(/\\$/, "", line)
           buf = buf line " "
           next
         }
         emit(buf line)
         buf = ""
       }
       END { if (buf != "") emit(buf) }' "$1"
}

# pipe_head <词…>:截到第一个管道/链接操作符为止,结果放 PIPE_HEAD 数组。
# 管道右侧是另一条命令:`sail ls … | sail rm -r -` 里 rm 的旗标不该记在 ls
# 名下(ls 恰好也有 -r,不看这一步就永远发现不了)。doc_cmd_of 与 doc_flags
# 都要用,故共用同一个截断口径。
PIPE_HEAD=()           # set -u 下必须先定义,空数组展开才不报 unbound
pipe_head() {
  PIPE_HEAD=()
  local t
  for t in "$@"; do
    case "$t" in '|'|'||'|'&&'|';'|'&'|'|&') break;; esac
    PIPE_HEAD+=("$t")
  done
}

# doc_cmd_of <前导旗标…> <词…>:从一次 sail 调用的词元里取出「最长匹配的已注册命令」。
# doc_cmds / doc_flags 共用它,保证两类判定口径一致:
#   - 跳过命令前的旗标及其取值(sail -p test upload …);
#   - 认出两段式子命令(serve webdav),不靠猜第二个词;
#   - 词元不是已知命令时原样返回,交由调用方按「文档命令有效性」报错,
#     不会被 && / | 之类的 shell 操作符带偏。
doc_cmd_of() {
  while [[ $# -gt 0 && "$1" == -* ]]; do
    case "$1" in
      --help|--version|-h|-v) shift;;    # 无值的全局旗标
      *=*) shift;;                       # 带 = 的旗标自带取值
      *)
        shift
        [[ $# -gt 0 ]] || break          # 末尾是需要值的旗标:没有命令可取
        shift                            # 跳过旗标值
        ;;
    esac
  done
  [[ $# -gt 0 ]] || return 0
  case "$1" in
    # shell 操作符/重定向:出现在 `sail` 之后的不是命令,而是 `cd sail && go build`
    # 这类把 sail 当路径/名字用的行。跳过,别按「未知命令」误报。
    '&'|'&&'|'|'|'||'|';'|'>'|'>>'|'<'|'2>'|'2>&1') return 0;;
  esac
  pipe_head "$@"
  [[ ${#PIPE_HEAD[@]} -gt 0 ]] || return 0
  set -- "${PIPE_HEAD[@]}"
  # 两段式子命令只在「第一个词本身是子命令宿主」时才成立。
  # 用缓存树里的两词条目判定:既认对(serve webdav),也不会把
  # `du --bogus-flag` 误拼成两词命令(du 有全局旗标但没有子命令)。
  local tree="$TREE_CMDS"
  if [[ $# -gt 1 ]] && printf '%s\n' "$tree" | grep -qxF "$1 $2"; then
    printf '%s' "$1 $2"; return 0
  fi
  if [[ $# -gt 1 ]] && printf '%s\n' "$tree" | grep -qF "$1 "; then
    # 第一个词确实是子命令宿主、第二个词却不是它的子命令(如 `sail serve webdav2`):
    # 报两词出去,让「文档命令有效性」直接点名 webdav2,而不是把它当参数忽略、
    # 再在后续检查里报成一个莫名其妙的旗标错误。
    printf '%s' "$1 $2"; return 0
  fi
  printf '%s' "$1"
}

# doc_cmds <readme>:示例里用到的命令名(含子命令),每行一个。
doc_cmds() {
  local line
  local -a words
  while read -r line; do
    [[ -n "$line" ]] || continue
    read -r -a words <<< "$line"
    [[ ${#words[@]} -gt 1 ]] || continue   # `sail --version` 等旗标-only 示例
    doc_cmd_of "${words[@]:1}"
    printf '\n'
  done < <(doc_sail_tokens "$1") | sort -u
}

# doc_flags <readme>:示例里用到的旗标,输出 "命令<TAB>旗标"。
# 旗标记在「最长匹配的已注册命令」名下:这样 doc_cmds 与 doc_flags 对
# `sail serve webdav --tls-cert x` 得到同一个归属,不会一件一议。
doc_flags() {
  local line cmd n flag
  local -a words args
  while read -r line; do
    [[ -n "$line" ]] || continue
    read -r -a words <<< "$line"
    [[ ${#words[@]} -gt 1 ]] || continue
    args=("${words[@]:1}")

    # 先跳过命令前的全局旗标及其取值,使后面的 shift 与 doc_cmd_of
    # 使用同一套「命令从哪里开始」的口径。
    while [[ ${#args[@]} -gt 0 && "${args[0]}" == -* ]]; do
      case "${args[0]}" in
        --help|--version|-h|-v|*=*) args=("${args[@]:1}");;
        *)
          args=("${args[@]:1}")
          [[ ${#args[@]} -gt 0 ]] && args=("${args[@]:1}")
          ;;
      esac
    done
    [[ ${#args[@]} -gt 0 ]] || continue
    pipe_head "${args[@]}"
    [[ ${#PIPE_HEAD[@]} -gt 0 ]] || continue
    args=("${PIPE_HEAD[@]}")
    cmd="$(doc_cmd_of "${args[@]}")" || true
    [[ -n "$cmd" ]] || continue
    # 别名没有自己的旗标行(upload→cp、cat→view):按规范名查表,
    # 否则合法的 `sail upload -r …` 会被当成漂移。
    case "$cmd" in
      upload|download) cmd=cp;;
      cat)             cmd=view;;
    esac
    n=$(wc -w <<< "$cmd" | tr -d ' ')
    args=("${args[@]:n}")
    while [[ ${#args[@]} -gt 0 ]]; do
      flag="${args[0]}"
      case "$flag" in
        --*=*)   printf '%s\t%s\n' "$cmd" "${flag%%=*}";;
        -)       :;;      # 单独一个 - 是 stdin/stdout 占位符,不是旗标
        -*)      printf '%s\t%s\n' "$cmd" "$flag";;
      esac
      args=("${args[@]:1}")
    done
  done < <(doc_sail_tokens "$1")
}

# doc_command_lines <readme>:代码块里所有 sail 调用的命令段(去注释、去空白)。
# 命令行本身与语言无关——中英 README 的同一条示例应当逐字一致,只注释不同。
doc_command_lines() {
  local line
  local -a words
  while read -r line; do
    [[ -n "$line" ]] || continue
    read -r -a words <<< "$line"
    pipe_head "${words[@]}"
    [[ ${#PIPE_HEAD[@]} -gt 0 ]] || continue
    printf '%s\n' "${PIPE_HEAD[*]}"
  done < <(doc_sail_tokens "$1") | sort
}

# cmd_covered <readme> <cmd>:readme 是否提及该命令。
# 覆盖三种写法:`sail <cmd>`、反引号包住的 `<cmd>`,或表格中段 `x` / `y`。
cmd_covered() {
  local readme="$1" cmd="$2"
  grep -qE "sail ${cmd}([^a-z-]|\$)" "$readme" && return 0
  grep -qE "\`${cmd}\`" "$readme" && return 0
  return 1
}

# ── 2. 每个命令(含子命令)必须在三份 README 中都出现 ────────
echo "── 命令覆盖检查 ──"
while IFS= read -r cmd; do
  [[ -n "$cmd" ]] || continue
  for readme in "$MAIN_README" "$ZH_README" "$NPM_README"; do
    cmd_covered "$readme" "$cmd" || err "$readme 未提及命令 \`$cmd\`"
  done
done < <(tree_cmds)
[[ $fail -eq 0 ]] && ok "所有命令(含子命令)均在三份 README 中出现"

# ── 3. 命令树的层级必须在 --help 里可见 ────────────────────
# 「帮助信息是否覆盖全部命令」:每个可见命令都要能通过父命令的
# Available Commands 找到,并且自身 --help 有 Usage 段。
echo
echo "── 帮助信息覆盖检查 ──"
parents="$("$SAIL_BIN" __commands | cut -f1)"
for c in $parents; do
  help_out="$("$SAIL_BIN" "$c" --help --lang en 2>&1 || true)"
  if ! printf '%s\n' "$help_out" | grep -q "^Usage:"; then
    err "sail $c --help 缺少 Usage 段"
  fi
  for sub in $("$SAIL_BIN" "$c" --help --lang en 2>&1 | awk '/^Available Commands:/{f=1;next} /^$/{f=0} f{print $1}'); do
    [[ "$sub" == "help" ]] && continue
    # 子命令必须自报 Usage,否则用户点进去看不到用法
    "$SAIL_BIN" "$c" "$sub" --help --lang en >/dev/null 2>&1 ||
      err "sail $c $sub --help 执行失败"
  done
done
[[ $fail -eq 0 ]] && ok "每个命令都能通过 --help 找到并给出用法"

# ── 4. README 里用到的命令必须真实存在(反向漂移)───────────
# 文档里写了一个已改名/不存在的命令,用户照抄即报错,这类漂移同样要拦。
# 别名(upload/download/cat)是合法写法,一并认。
echo
echo "── 文档命令有效性检查 ──"
doc_bad=0
valid_cmds="$( { tree_cmds; tree_aliases; } | sort -u )"
for readme in "$MAIN_README" "$ZH_README" "$NPM_README"; do
  while IFS= read -r cmd; do
    [[ -n "$cmd" ]] || continue
    if ! printf '%s\n' "$valid_cmds" | grep -qxF "$cmd"; then
      err "$readme 中的命令 \`sail $cmd\` 既不是命令也不是别名"
      doc_bad=1
    fi
  done < <(doc_cmds "$readme")
done
[[ $doc_bad -eq 0 ]] && ok "README 中出现的命令均真实存在(含别名)"

# ── 5. README 里用到的旗标必须属于该命令(或全局)──────────
# 与本轮新增的「命令必须真实存在」对称:文档写了一个不存在的旗标(或挂错了
# 命令),用户照抄即报错。中英 diff 只能抓单边漂移,两边写错同一个旗标要靠这里。
# 全量旗标表由 `sail <cmd> --help` 提供(三份 README 已写明),这里只查
# 「文档用到的旗标在这个命令上确实存在」,不要求 README 列出全部旗标。
echo
echo "── 文档旗标有效性检查 ──"
FLAG_INDEX="$(mktemp)"; trap 'rm -f "$FLAG_INDEX"' EXIT
build_flag_index > "$FLAG_INDEX"
flag_bad=0
for readme in "$MAIN_README" "$ZH_README" "$NPM_README"; do
  while IFS=$'\t' read -r cmd flag; do
    [[ -n "$cmd" && -n "$flag" ]] || continue
    # 命令本身就没通过上一条检查时不再报旗标,避免同一个笔误报两遍
    printf '%s\n' "$valid_cmds" | grep -qxF "$cmd" || continue
    case "$flag" in --help|-h|-v) continue;; esac    # 通用/全局开关,任何命令都接受
    if ! all_flags $cmd | grep -qxF -- "$flag"; then
      err "$readme 中 \`sail $cmd ... $flag\` 的旗标不属于该命令"
      flag_bad=1
    fi
  done < <(doc_flags "$readme")
done
[[ $flag_bad -eq 0 ]] && ok "README 中出现的旗标均属于对应命令"

# ── 6. 中文 README 与英文 README 的示例命令行必须逐条一致 ──
# 双语是两篇独立文件,最容易漂移的是「一边加了/改了示例,另一边没跟上」。
# 命令行本身与语言无关(两条命令行的差异只可能是漂移),注释则各写各的、
# 不参与比对。用 diff 而非集合比大小,单边增删一条都能看出来。
echo
echo "── 中英 README 示例一致性检查 ──"
diff_out="$(diff <(doc_command_lines "$MAIN_README") <(doc_command_lines "$ZH_README") || true)"
if [[ -z "$diff_out" ]]; then
  ok "中英 README 的示例命令行逐条一致"
else
  err "中英 README 示例不一致(< 仅英文, > 仅中文):"
  printf '%s\n' "$diff_out" | sed 's/^/       /'
fi

# ── 7. 主 README 提到的 group 必须在 npm README 也有 ───────
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

# ── 8. 关键功能小节必须同步存在 ────────────────────────────
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
echo -e "${RED}README 同步检查失败:请补齐三份 README(或修脚本里列出的漂移)${NC}"
exit 1
