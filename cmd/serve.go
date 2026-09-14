package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BeCrafter/sail/internal/client"
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
	printWindowsSetup bool
}

var serveWebdavOpts serveWebdavFlags

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start a server that shares a bucket over a standard protocol",
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

	if cfgBucket == "" {
		return errors.New(i18n.T("--bucket is required: WebDAV shares a whole bucket, and without one there is no data to expose"))
	}
	if o.user == "" || o.password == "" {
		return errors.New(i18n.T("--user and --password are required: this gateway does not allow anonymous sharing"))
	}
	if (o.tlsCert == "") != (o.tlsKey == "") {
		return errors.New(i18n.T("--tls-cert and --tls-key must be supplied together"))
	}

	backendMax, err := parseSize(o.backendMaxSize)
	if err != nil {
		return fmt.Errorf(i18n.T("invalid --backend-max-object-size: %w"), err)
	}
	maxUpload := backendMax
	if o.maxUploadSize != "" {
		if maxUpload, err = parseSize(o.maxUploadSize); err != nil {
			return fmt.Errorf(i18n.T("invalid --max-upload-size: %w"), err)
		}
	}
	chunkSize, err := parseChunkSize(o.chunkedUpload, o.chunkSize, backendMax)
	if err != nil {
		return err
	}

	r, _, err := loadResolved()
	if err != nil {
		return err
	}
	ctx := context.Background()
	s3c, err := client.New(ctx, r)
	if err != nil {
		return err
	}

	core, err := s3fs.New(s3fs.Config{
		Client:        s3c,
		Bucket:        cfgBucket,
		Prefix:        o.prefix,
		StagingDir:    o.stagingDir,
		MaxUploadSize: maxUpload,
		ChunkedUpload: o.chunkedUpload,
		ChunkSize:     chunkSize,
	})
	if err != nil {
		return err
	}
	srv, err := webdavfs.NewServer(webdavfs.Config{
		FileSystem:        webdavfs.New(core),
		User:              o.user,
		Password:          o.password,
		MaxUploadSize:     maxUpload,
		MaxUploadSizeText: humanSize(maxUpload),
		Logger:            log.New(os.Stderr, "", log.LstdFlags),
	})
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:    o.listen,
		Handler: srv,
		// 不设 ReadTimeout/WriteTimeout:大文件上传下载会长时间占用连接,
		// 超时会把正常传输掐断。超时由客户端与 TCP 层负责。
		ReadHeaderTimeout: 30 * time.Second,
	}

	scheme := "http"
	if o.tlsCert != "" {
		scheme = "https"
	}
	fmt.Fprint(os.Stderr, i18n.Tf(
		"sail webdav started: %s://%s  bucket=%s prefix=%q user=%s max-object-size=%s staging=%s chunked=%s\n",
		scheme, o.listen, cfgBucket, corePrefix(o.prefix), o.user, humanSize(maxUpload), stagingDirOf(o.stagingDir), chunkedText(o.chunkedUpload, chunkSize)))

	if o.tlsCert != "" {
		return httpSrv.ListenAndServeTLS(o.tlsCert, o.tlsKey)
	}
	return httpSrv.ListenAndServe()
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
	serveWebdavCmd.Flags().BoolVar(&serveWebdavOpts.printWindowsSetup, "print-windows-setup", false, "print the Windows client registry setup and mount command, then exit")

	serveCmd.AddCommand(serveWebdavCmd)
}
