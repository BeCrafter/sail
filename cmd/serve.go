package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/BeCrafter/sail/internal/vfs/s3fs"
	"github.com/BeCrafter/sail/internal/webdavfs"
	"github.com/spf13/cobra"
)

// serveWebdavFlags 是 `sail serve webdav` 的全部参数。
type serveWebdavFlags struct {
	listen            string
	prefix            string
	user              string
	password          string
	tlsCert           string
	tlsKey            string
	backendMaxSize    string
	maxUploadSize     string
	stagingDir        string
	chunkedUpload     bool
	chunkSize         string
	dirCacheTTL       string
	prewarm           []string
	printWindowsSetup bool
}

var serveWebdavOpts serveWebdavFlags

var serveCmd = &cobra.Command{
	GroupID: "server",
	Use:     "serve",
	Short:   "Start a server that shares a bucket over a standard protocol",
	Long: `Share a bucket with the file manager built into the OS; clients install nothing.

  serve webdav  -- share over the WebDAV protocol (HTTPS optional)`,
}

var serveWebdavCmd = &cobra.Command{
	Use:   "webdav",
	Short: "Share a bucket over WebDAV (mountable directly by macOS Finder / Windows Explorer)",
	Long: `Share the whole bucket over the WebDAV protocol (or the prefix given by --prefix); clients
mount it with capabilities built into the OS, no software to install.

Design boundaries:
  - Uploads land in full on the --staging-dir staging disk and are only chunked and uploaded to
    S3 on commit; staging peak is about one file's size x concurrent uploads, so point it at a
    disk with enough room.
  - The server deliberately offers no flag to "raise the Windows 50MB gate": that gate lives in
    the client registry and no server-side flag can move it. Use --print-windows-setup for the
    client-side procedure.
  - LOCK is an in-process lock, lost on restart and not shared across instances.
  - Directory-level MOVE/COPY returns 501, leaving the client to fall back to "copy + delete".
  - With --chunked-upload on, files larger than --chunk-size are stored as chunks under the
    reserved .sail/ prefix plus a small manifest object at the logical key: the bucket then
    contains .sail/ objects, and "sail presign" fails loud on such a bucket because a presigned
    URL would hand out the manifest instead of the file.

Examples:
  sail serve webdav --bucket mybucket --listen :8443 \
    --user alice --password '***' --tls-cert c.pem --tls-key k.pem
  sail serve webdav --print-windows-setup`,
	Args: cobra.NoArgs,
	RunE: runServeWebdav,
}

func runServeWebdav(cmd *cobra.Command, _ []string) error {
	o := serveWebdavOpts

	if o.printWindowsSetup {
		fmt.Print(windowsSetupText(o.listen))
		return nil
	}

	r, _, err := loadResolved()
	if err != nil {
		return err
	}
	s, err := mergeServe(o, r, cmd.Flags().Changed)
	if err != nil {
		return err
	}

	// 桶取自统一解析链的出口 r.Bucket(--bucket > SAIL_BUCKET > profile.bucket)。
	// config.Resolve 刻意不校验 Bucket 非空(多数命令可由 s3://bucket/key 显式给出),
	// 而 WebDAV 共享的是整桶,没有桶就无从共享,故这一条由 serve 自己兜。
	if r.Bucket == "" {
		return errors.New(i18n.Tf(
			"no bucket to share: pass --bucket, set SAIL_BUCKET, or add \"bucket\" to profile %q in the config file — WebDAV exposes a whole bucket, and without one there is nothing to share",
			r.ProfileName))
	}
	ctx := context.Background()
	s3c, err := client.New(ctx, r)
	if err != nil {
		return err
	}

	core, err := s3fs.New(s3fs.Config{
		Client:        s3c,
		Bucket:        r.Bucket,
		Prefix:        s.prefix,
		StagingDir:    s.stagingDir,
		MaxUploadSize: s.maxUpload,
		ChunkedUpload: s.chunkedUpload,
		ChunkSize:     s.chunkSize,
	})
	if err != nil {
		return err
	}
	srv, err := webdavfs.NewServer(webdavfs.Config{
		FileSystem:        webdavfs.NewWithListingCache(core, s.dirCacheTTL),
		User:              s.user,
		Password:          s.password,
		MaxUploadSize:     s.maxUpload,
		MaxUploadSizeText: humanSize(s.maxUpload),
		Logger:            log.New(os.Stderr, "", log.LstdFlags),
		PrewarmDirs:       s.prewarm,
	})
	if err != nil {
		return err
	}

	httpSrv := newServeHTTPServer(s.listen, srv)

	scheme := "http"
	if s.tlsCert != "" {
		scheme = "https"
	}
	fmt.Fprint(os.Stderr, i18n.Tf(
		"sail webdav started: %s://%s  bucket=%s profile=%s%s user=%s max-object-size=%s staging=%s chunked=%s\n",
		scheme, s.listen, r.Bucket, r.ProfileName, exposePrefix(s.prefix), s.user, humanSize(s.maxUpload), stagingDirOf(s.stagingDir), chunkedText(s.chunkedUpload, s.chunkSize)))
	if urls := serveURLs(scheme, s.listen); len(urls) > 0 {
		fmt.Fprint(os.Stderr, i18n.Tf("  mount at: %s\n", strings.Join(urls, "  ")))
		if len(urls) > 1 {
			fmt.Fprint(os.Stderr, i18n.T("  (localhost = this machine; LAN IP = other devices)\n"))
		}
	}

	if s.tlsCert != "" {
		return httpSrv.ListenAndServeTLS(s.tlsCert, s.tlsKey)
	}
	return httpSrv.ListenAndServe()
}

// serveSettings 是合并并校验后的 serve 生效参数。
type serveSettings struct {
	listen        string
	prefix        string
	user          string
	password      string
	tlsCert       string
	tlsKey        string
	stagingDir    string
	maxUpload     int64
	chunkedUpload bool
	chunkSize     int64
	dirCacheTTL   time.Duration
	prewarm       []string
}

// mergeServe 按「flag(显式设置) > profile.serve.* > flag 默认值」合并并校验
// 全部 serve 参数。changed 返回某 flag 是否被显式设置;测试可传显式 map。
func mergeServe(o serveWebdavFlags, r *config.Resolved, changed func(string) bool) (serveSettings, error) {
	listen := pick(changed("listen"), o.listen, r.Serve.Listen)
	prefix := pick(changed("prefix"), o.prefix, r.Serve.Prefix)
	user := pick(changed("user"), o.user, r.Serve.User)
	password := pick(changed("password"), o.password, r.Serve.Password)
	tlsCert := pick(changed("tls-cert"), o.tlsCert, r.Serve.TLSCert)
	tlsKey := pick(changed("tls-key"), o.tlsKey, r.Serve.TLSKey)
	stagingDir := pick(changed("staging-dir"), o.stagingDir, r.Serve.StagingDir)
	backendMaxRaw := pick(changed("backend-max-object-size"), o.backendMaxSize, r.Serve.BackendMaxSize)
	maxUploadRaw := pick(changed("max-upload-size"), o.maxUploadSize, r.Serve.MaxUploadSize)
	chunkSizeRaw := pick(changed("chunk-size"), o.chunkSize, r.Serve.ChunkSize)
	chunkedUpload := r.Serve.ChunkedUpload
	if changed("chunked-upload") {
		chunkedUpload = o.chunkedUpload
	}

	if user == "" || password == "" {
		return serveSettings{}, errors.New(i18n.Tf(
			"--user and --password are required (from flags or profile %q \"serve\" config): this gateway does not allow anonymous sharing",
			r.ProfileName))
	}
	if (tlsCert == "") != (tlsKey == "") {
		return serveSettings{}, errors.New(i18n.T("--tls-cert and --tls-key must be supplied together"))
	}

	backendMax, err := parseSize(backendMaxRaw)
	if err != nil {
		return serveSettings{}, fmt.Errorf(i18n.T("invalid --backend-max-object-size: %w"), err)
	}
	maxUpload := backendMax
	if maxUploadRaw != "" {
		if maxUpload, err = parseSize(maxUploadRaw); err != nil {
			return serveSettings{}, fmt.Errorf(i18n.T("invalid --max-upload-size: %w"), err)
		}
	}
	chunkSize, err := parseChunkSize(chunkedUpload, chunkSizeRaw, backendMax)
	if err != nil {
		return serveSettings{}, err
	}
	dirCacheTTL, err := parseDuration(o.dirCacheTTL)
	if err != nil {
		return serveSettings{}, fmt.Errorf(i18n.T("invalid --dir-cache-ttl: %w"), err)
	}

	return serveSettings{
		listen:        listen,
		prefix:        prefix,
		user:          user,
		password:      password,
		tlsCert:       tlsCert,
		tlsKey:        tlsKey,
		stagingDir:    stagingDir,
		maxUpload:     maxUpload,
		chunkedUpload: chunkedUpload,
		chunkSize:     chunkSize,
		dirCacheTTL:   dirCacheTTL,
		prewarm:       o.prewarm,
	}, nil
}

// parseDuration 解析 --dir-cache-ttl。空串或 "0" 表示关闭缓存;
// 否则必须是合法的 Go duration(如 "5s"、"1m")。
func parseDuration(raw string) (time.Duration, error) {
	t := strings.TrimSpace(raw)
	if t == "" || t == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(t)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, fmt.Errorf("duration must not be negative")
	}
	return d, nil
}

// newServeHTTPServer 构造 WebDAV 网关的 http.Server。
func newServeHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:    addr,
		Handler: handler,
		// 不设 ReadTimeout/WriteTimeout:大文件上传下载会长时间占用连接,
		// 超时会把正常传输掐断。超时由客户端与 TCP 层负责。
		ReadHeaderTimeout: 30 * time.Second,
		// macOS Finder 挂载 WebDAV 的第一步是发 `OPTIONS *`(星号)探测能力,
		// 靠响应里的 DAV 头判断「是不是 WebDAV 服务器」。默认 net/http 会用内置的
		// globalOptionsHandler 直接回空 200,请求到不了 WebDAV handler,于是没有 DAV 头,
		// Finder 判定为非 WebDAV 而卡在「连接中」。禁用后 OPTIONS * 落到我们的 handler。
		DisableGeneralOptionsHandler: true,
	}
}

// serveURLs 把 --listen 地址解析成可直接挂载的完整 URL 列表。
//
// listen 常写成 ":8080" 或 "0.0.0.0:8080",这些不是客户端能连的地址。这里按
// 实际绑定展开成:
//   - 指定了具体 host(如 192.168.1.5:8080)时,只给那一个地址;
//   - 通配地址(空 / 0.0.0.0 / ::)时,给出 localhost(本机挂载)+ 各网卡的
//     局域网 IPv4(其它设备挂载),让用户按场景复制。
//
// 解析失败或无需展开(如 "unix:/tmp/x.sock")时返回 nil,不打断启动。
func serveURLs(scheme, listen string) []string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		return nil
	}
	// 显式绑定了具体主机:只暴露它,不猜其它地址。
	if ip := net.ParseIP(host); host != "" && (ip == nil || !ip.IsUnspecified()) {
		return []string{urlFor(scheme, host, port)}
	}
	// 通配绑定:本机 + 局域网。
	urls := []string{urlFor(scheme, "localhost", port)}
	for _, ip := range lanIPv4s() {
		urls = append(urls, urlFor(scheme, ip, port))
	}
	return urls
}

// urlFor 拼一个挂载 URL;IPv6 字面量加方括号。
func urlFor(scheme, host, port string) string {
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host + ":" + port + "/"
}

// lanIPv4s 枚举本机上可用的局域网 IPv4 地址(排除回环与非全局单播)。
// 通过枚举网卡得到,不依赖任何外部网络(不再向 8.8.8.8 探测),
// 因此离线/内网环境同样可用。顺序按网卡名排序,结果稳定。
func lanIPv4s() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var names []string
	byName := map[string][]string{}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.IsLoopback() {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil {
				continue
			}
			// 排除链路本地 169.254.0.0/16(awdl/llw 等接口常见),它对其它设备不可达。
			if ip4[0] == 169 && ip4[1] == 254 {
				continue
			}
			byName[ifc.Name] = append(byName[ifc.Name], ip4.String())
		}
	}
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []string
	for _, name := range names {
		out = append(out, byName[name]...)
	}
	return out
}

// pick 实现「flag(显式设置) > 配置 > flag 默认值」的合并:
//   - changed 为 true:flag 显式给出,直接取 flagVal;
//   - 否则配置非空则取配置;
//   - 都没有时落回 flag 默认值(flagVal 本身即默认值)。
func pick(changed bool, flagVal, cfgVal string) string {
	if changed {
		return flagVal
	}
	if cfgVal != "" {
		return cfgVal
	}
	return flagVal
}

// exposePrefix 渲染启动横幅里的共享前缀段:未设前缀时整段省略,
// 免得 `prefix=""` 让「没设前缀」看起来像一个空前缀。
func exposePrefix(p string) string {
	if p = corePrefix(p); p != "" {
		return " prefix=" + p
	}
	return ""
}

func corePrefix(p string) string {
	return strings.Trim(p, "/")
}

// 分片阈值的最小/最大允许值:下限是 S3 multipart 最小片,上限是单次
// PutObject 的 5GiB 硬顶。
const (
	minChunkSize = 5 << 20
	maxChunkSize = 5 << 30
)

// parseChunkSize 解析并校验 --chunk-size。未开分片时返回 0(不参与校验);
// 开启时必须是 [5MiB, 5GiB],且不超过声明的后端单对象上限 —— 越界拒绝启动,
// 不在运行期炸。同时受两道上限约束:L 与 S3 单次 PutObject 的 5GiB。
func parseChunkSize(enabled bool, raw string, backendMax int64) (int64, error) {
	if !enabled {
		return 0, nil
	}
	size, err := parseSize(raw)
	if err != nil {
		return 0, errors.New(i18n.Tf("invalid --chunk-size: %v", err))
	}
	if size < minChunkSize || size > maxChunkSize {
		return 0, errors.New(i18n.Tf(
			"--chunk-size must be between %s and %s, got %s",
			humanSize(minChunkSize), humanSize(int64(maxChunkSize)), humanSize(size)))
	}
	if backendMax > 0 && size > backendMax {
		return 0, errors.New(i18n.Tf(
			"--chunk-size (%s) exceeds --backend-max-object-size (%s): raise the backend limit or lower the chunk size",
			humanSize(size), humanSize(backendMax)))
	}
	return size, nil
}

// chunkedText 渲染启动横幅里的分片状态。
func chunkedText(enabled bool, chunkSize int64) string {
	if !enabled {
		return "off"
	}
	return "on(" + humanSize(chunkSize) + ")"
}

func stagingDirOf(dir string) string {
	if dir != "" {
		return dir
	}
	return os.TempDir() + "(system default)"
}

// sizeUnits 是 --backend-max-object-size / --max-upload-size 支持的单位(大小写不敏感)。
var sizeUnits = map[string]int64{
	"":    1,
	"B":   1,
	"K":   1000,
	"KB":  1000,
	"KIB": 1 << 10,
	"M":   1000 * 1000,
	"MB":  1000 * 1000,
	"MIB": 1 << 20,
	"G":   1000 * 1000 * 1000,
	"GB":  1000 * 1000 * 1000,
	"GIB": 1 << 30,
	"T":   1000 * 1000 * 1000 * 1000,
	"TB":  1000 * 1000 * 1000 * 1000,
	"TIB": 1 << 40,
	"P":   1000 * 1000 * 1000 * 1000 * 1000,
	"PB":  1000 * 1000 * 1000 * 1000 * 1000,
	"PIB": 1 << 50,
}

// parseSize 解析 "5TiB" / "1GiB" / "500MB" / "1024" 这类人类可读大小。
func parseSize(s string) (int64, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, fmt.Errorf("size is empty")
	}
	i := 0
	for i < len(t) && (t[i] >= '0' && t[i] <= '9' || t[i] == '.') {
		i++
	}
	numPart := t[:i]
	unit := strings.ToUpper(strings.TrimSpace(t[i:]))
	if numPart == "" {
		return 0, fmt.Errorf("size %q is missing a number", s)
	}
	v, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse %q: %w", s, err)
	}
	mult, ok := sizeUnits[unit]
	if !ok {
		return 0, fmt.Errorf("unrecognized unit %q (supported: B/KB/KiB/MB/MiB/GB/GiB/TB/TiB/PB/PiB)", unit)
	}
	return int64(v * float64(mult)), nil
}

// humanSize 把字节数渲染成人类可读文本,只用于提示文案。
func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// windowsSetupText 打印 Windows 客户端的注册表配置与挂载命令。
// 50MB 闸门在客户端注册表,服务端参数改不动它,所以这里给的是客户端侧动作。
func windowsSetupText(listen string) string {
	port := "80"
	if _, p, err := net.SplitHostPort(listen); err == nil && p != "" {
		port = p
	}
	return i18n.Tf(`Mount a sail WebDAV drive in Windows Explorer
=============================================

1) Raise the WebClient upload limit (about 50MB by default)

   This gate lives in the Windows client registry; changing server-side flags has no effect.
   Create upgrade-webclient.reg and double-click to import (administrator required):

Windows Registry Editor Version 5.00

[HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Services\WebClient\Parameters]
"FileSizeLimitInBytes"=dword:ffffffff
"BasicAuthLevel"=dword:00000002

   Or do it in one go from an administrator PowerShell:

Set-ItemProperty -Path "HKLM:\SYSTEM\CurrentControlSet\Services\WebClient\Parameters" -Name FileSizeLimitInBytes -Value 4294967295 -Type DWord
Set-ItemProperty -Path "HKLM:\SYSTEM\CurrentControlSet\Services\WebClient\Parameters" -Name BasicAuthLevel -Value 2 -Type DWord

2) The WebClient service must be restarted for the new limit to take effect

Restart-Service WebClient
# If it reports the service is missing or cannot restart, use instead:
net stop webclient
net start webclient

3) Mount the drive

   HTTPS (recommended, Basic credentials never cross the network in the clear):
     net use Z: \\<host>@SSL@%s\DavWWWRoot /user:<username>

   HTTP (only on a trusted intranet):
     net use Z: \\<host>@%s\DavWWWRoot /user:<username>

   Unmount: net use Z: /delete
`, port, port)
}

func init() {
	serveWebdavCmd.Flags().StringVar(&serveWebdavOpts.listen, "listen", ":8080", "listen address")
	serveWebdavCmd.Flags().StringVar(&serveWebdavOpts.prefix, "prefix", "", "shared root prefix (mapped to /); out-of-prefix paths are always rejected")
	serveWebdavCmd.Flags().StringVar(&serveWebdavOpts.user, "user", "", "Basic auth username (required)")
	serveWebdavCmd.Flags().StringVar(&serveWebdavOpts.password, "password", "", "Basic auth password (required)")
	serveWebdavCmd.Flags().StringVar(&serveWebdavOpts.tlsCert, "tls-cert", "", "TLS certificate file (supplying it together with --tls-key enables HTTPS)")
	serveWebdavCmd.Flags().StringVar(&serveWebdavOpts.tlsKey, "tls-key", "", "TLS private key file (supplying it together with --tls-cert enables HTTPS)")
	serveWebdavCmd.Flags().StringVar(&serveWebdavOpts.backendMaxSize, "backend-max-object-size", "5TiB", "declared backend per-object limit (S3 has no capability negotiation, it can't be probed)")
	serveWebdavCmd.Flags().StringVar(&serveWebdavOpts.maxUploadSize, "max-upload-size", "", "request body limit, defaults to --backend-max-object-size; over the limit returns 413")
	serveWebdavCmd.Flags().StringVar(&serveWebdavOpts.stagingDir, "staging-dir", "", "write staging directory, defaults to the system temp dir; peak is about one file's size x concurrent uploads")
	serveWebdavCmd.Flags().BoolVar(&serveWebdavOpts.chunkedUpload, "chunked-upload", false, "store files larger than --chunk-size as chunks plus a manifest (default off: 1 file = 1 object)")
	serveWebdavCmd.Flags().StringVar(&serveWebdavOpts.chunkSize, "chunk-size", "4GiB", "max physical chunk size and the chunked-storage threshold (5MiB ~ 5GiB); requires --chunked-upload")
	serveWebdavCmd.Flags().StringVar(&serveWebdavOpts.dirCacheTTL, "dir-cache-ttl", "60s", "how long a directory listing is cached (e.g. 60s, 10m; 0 disables); expired entries are served stale while refreshing in the background, so a warm directory never blocks. External bucket changes become visible after at most this long")
	serveWebdavCmd.Flags().StringSliceVar(&serveWebdavOpts.prewarm, "prewarm", nil, "directories to keep hot in the background (comma-separated logical paths, e.g. /yiche,/modelImage); each is listed once at startup then refreshed, so the first visit does not pay the full listing cost")
	serveWebdavCmd.Flags().BoolVar(&serveWebdavOpts.printWindowsSetup, "print-windows-setup", false, "print the Windows client registry setup and mount command, then exit")

	serveCmd.AddCommand(serveWebdavCmd)
}
