# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概览

sail 是一个 S3 对象存储 CLI(Go,单静态二进制,零运行时依赖),支持任意 S3 兼容服务。核心能力:上传/下载/列举/删除/同步/presign/CDN URL/内容查看,以及 `sail serve webdav` —— 把 bucket(或指定前缀)暴露成可挂载的网络盘(macOS Finder / Windows 资源管理器)。npm 包 `@becrafter/sail` 是跨平台安装器,下载对应平台二进制。

## 常用命令

```bash
make build              # 编译到 ./sail(带版本注入;直接 go build -o sail . 也行)
make test               # go vet ./... + go test ./...(单测零外部依赖,无需凭证)
make e2e                # 端到端验证(需真实凭证:复用 ~/.config/sail/config.yaml 或 SAIL_E2E_* 变量)
make release VERSION=0.1.0   # 发布到 npm(本地需 npm login;CI 走 tag 触发)
make release-dry VERSION=0.1.0  # 发布预演,不真发

go test ./internal/vfs/s3fs -run TestName -v   # 跑单个测试
go test ./internal/webdavfs -run 'TestX|TestY'  # 跑多个
```

- 单测用 `internal/fakes3`(内存版 S3 端点)做后端替身,统计请求次数,可注入失败/延迟;不需要 docker/网络。
- e2e 脚本:`scripts/e2e.sh`(CLI 全命令,小文件)与 `scripts/e2e-serve.sh`(WebDAV 协议流,curl 打真实服务)。两者都需要真实后端凭证。
- CI(`.github/workflows/test.yml`):`go vet` + `go test ./...` + `scripts/check-readme-sync.sh`(需先 `go build -o sail .`)。

## 架构

分层,依赖方向单向:**`cmd/`(cobra 命令) → `internal/webdavfs`(WebDAV 协议壳) → `internal/vfs`(协议无关内核契约) → `internal/vfs/s3fs`(S3 实现)**。

- **`internal/vfs/vfs.go`** 定义 `FileSystem` 六方法契约。**本包不得引入 net/http、webdav 或 S3 SDK 类型;协议壳也不得绕过本包直连 S3**——这是为将来新增协议壳(SMB 等)留的边界。目录语义由 S3 共同前缀 + 目录标记对象(key 以 `/` 结尾的 0 字节对象)共同推断。
- **`internal/vfs/s3fs/`** 是内核实现;`--chunked-upload` 时大文件以「manifest(逻辑 key)+ 分片(`.sail/parts/<逻辑路径>/<ver>/`)」表示,所有片 key 必须经 `f.key()` 施加前缀(F4 教训)。
- **`internal/vfs/quotafs/`** 是内核的**装饰器**(配额会计,`serve.users[].quota`):物理字节快照(TTL 5min)+ 在途预留 + 准入/提交点双检查。它包装 `vfs.FileSystem`,与协议无关。
- **`internal/webdavfs/`** 是薄壳:`server.go`(认证/路由/上传闸门/507 输出)、`fs.go`(webdav.FileSystem 适配 + 目录列表缓存 + 预热)。
- **`internal/s3del/`** 是**所有删除路径的统一入口**:批量端点优先,不支持时降级为**并发**单删并缓存探测结论。新增删除路径必须走它,不要再写串行回退(原因见文件头注释:某网关单次 DELETE 固定 ~28s,串行 = N×28s)。
- **`cmd/serve.go`** 组装运行时:配置解析 → client → s3fs → quotafs → webdavfs,含多用户路由表、fsnotify 配置热加载(Load→Validate→Swap,失败保留旧状态)。
- **`internal/client/client.go`** 构造 S3 SDK 客户端,内置若干 S3 兼容适配(见下)。
- **`internal/i18n/`** 双语机制:英文原文即 key,中文在 `zh_*.go` 的 map 里;同一 key 的不一致翻译会在 init 时 panic。`Apply` 在 Execute 前重写命令 help。新增面向用户字符串时同步补中文。
- **配置**:`~/.config/sail/config.yaml`,解析链为 flag > 环境变量 > profile;`serve` 块的字段与 `serve webdav` 同名 flag 一一对应。

### 必须遵守的实现约束

- **写路径提交点**:`WriteHandle.Commit` 幂等,未 Commit 不得产生任何对象。WebDAV 壳把提交挂在 `File.Stat()`(x/net 的 PUT 调用序是 `io.Copy → Stat → Close`,响应 ETag 由这次 Stat 决定;中途断连会跳过 Stat,Close 只清理)。
- **错误语义**:`vfs` 的错误必须 `errors.Is` 可判定,协议壳据此映射状态码(ErrNotExist→404、ErrExist→405、ErrNotSupported→501、ErrTooLarge→413、ErrInsufficientStorage→507)。不新造错误码;配额语境用 `ErrQuotaExceeded` 包装而非新增状态码。
- **可选能力接口**:`WriteOptioner`(暂存盘预留)、`InfoOpener`(省一次 HEAD,读路径 1-RTT)、`QuotaCounter`(s3fs.Usage)。内核实现不了时应安全退回,不得给出错误结果。
- **S3 兼容适配**(改动 client 前先读该文件注释):checksum 设为 `WhenRequired`(部分服务不认 aws-chunked)、空 region 用 us-east-1 占位、`CopyObject` 后 HEAD 校验、XML 时间格式规范化。

## 文档同步(强制)

三个 README 必须同步,`scripts/check-readme-sync.sh` 以真实命令树(`sail __commands`)为准做校验,**CI 与发布预检都会拦截**:

- `README.md`(英文,完整文档)与 `README.zh-CN.md`(中文)—— 双语内容对等,改动要成对。
- `npm/main/README.md` —— 随 npm 包发布的独立精简版(发布时由 `scripts/release.sh` 复制),新增命令/功能小节的要点也要同步过去,可链回主 README 看细节。

改 `serve`/WebDAV 相关内容时,三处都要更新(flag 表、多用户、配额、挂载说明等小节)。

## 排查「慢」的路径(方法论)

背景:曾出现「Finder 连接 serve 特别慢」的报障,最终定位与 sail 无关。沉淀以下分层路径,避免重复走弯路:

1. **先分域,再定位**。同一个「慢」至少是三种互不相关的问题,混在一起会得出错误结论:①连接/挂载慢(客户端行为);②单请求慢(服务端/网关);③批量操作慢(并发度、网关对特定 API 的实现)。
2. **服务端日志是第一证据**。`sail serve webdav` 对每个请求打 `METHOD path status elapsed user=xxx`(毫秒级)。若全部请求 elapsed <300ms 而用户仍感觉慢,问题在客户端,服务端无责;日志时间戳可直接与客户端事件对齐算时间差。
3. **客户端侧看 macOS 统一日志**(免 sudo):`/usr/bin/log show --start ... --end ... --predicate 'process == "webdavfs_agent"'`。注意:zsh 里必须写全路径 `/usr/bin/log`(否则命中 shell 内建);`mount_webdav` 只是 fork/exec 薄壳(只持有 cwd 和 wait4),真正干活的是 `webdavfs_agent` 子进程——用 `lsof` 查薄壳会误判"什么都没干"。
4. **对照实验隔离变量**,最少三件套:把目标指向**无人监听的端口**(若耗时不变 → 与目标服务无关);换主机名重测一组(IP / localhost / `*.local`);用**已知延迟的慢操作**当探针(例如看挂载窗口内服务端是否根本收不到请求)。
5. **网关行为用最小探针直测**:复用 `internal/client.New` + `internal/config` 写临时 main(用后即删),直接计时单次 API 调用,区分「单次请求就慢」还是「SDK 重试/回退放大」。删除路径参考 `internal/s3del` 的注释。

### 已确认的结论(2026-09-16 实测)

- **macOS 挂载慢的常见根因是系统代理自动发现(PAC/WPAD)超时**:客户端首次建连前要向系统代理组件取配置,若系统开了 PAC 自动发现而 WPAD 主机不可达,该查询固定 ~45-60s 才超时回退直连。**每次新挂载进程都要重付一次,且与目标地址无关**(死端口同样慢,IP/localhost/`*.local` 都逃不掉,系统代理例外表拦不住 PAC 评估)。诊断入口:`scutil --proxy`。
  **解决方向**(已实测验证):关闭系统代理的「自动代理配置(PAC)」与「自动发现代理」。命令行等价:`networksetup -listallnetworkservices` 找到开着的服务名,再 `networksetup -setproxyautodiscovery <服务名> off`(本机实测**无需 sudo**;回滚 = 把 `off` 改回 `on`)。修复前后实测(同一台机器、同一测量脚本):**挂载 58-96s → 1s**、死端口探针 14-44s → 0s、挂载后首次 `ls` 59.5s → 0.7s;此前记忆中的「客户端固有 20-40s 基线」很可能也被该超时污染。sail 服务端侧无优化空间(全程 ~2s)。这是系统级设置、影响所有 CFNetwork 应用,**调整前须与用户确认**;若该配置来自办公网络下发,关闭可能影响需要代理的上网场景。
- **服务端固定延迟会按对象数放大**:xueersi 自建测试网关(profile `test`)的单次 DELETE 固定 ~27-30s(连不存在的 key 也一样),批量 `DeleteObjects` 返 500 不可用;BOS 对照 38ms 正常。**解决**:sail 侧已由 `internal/s3del` 并发化缓解,**总耗时 ≈ ⌈对象数/并发度⌉ × 单次延迟**;网关侧属其自身缺陷,最小复现 = 对不存在的 key 发一次 DELETE 并计时(或观察批量端点返 500),需推动网关团队修复,不要在 sail 侧再加特例适配。
- **挂载验收注意**:macOS WebDAVFS 落 mount 表本身就需要数十秒且波动大,不要用 30-50s 超时判定失败;校验挂载点要用 `pwd -P` 解析后的真实路径(`/tmp` 在 mount 表里显示为 `/private/tmp`);客户端缓存会掩盖服务端损坏,读断言要在服务端侧复核。

## 提交约定

提交信息用中文 + conventional 前缀(`feat`/`fix`/`perf`/`docs`/`refactor`/`chore`),涉及需求编号时带上(如 `feat(MERC-11): ...`);正文列要点。PR 一律用 merge commit 合并。
