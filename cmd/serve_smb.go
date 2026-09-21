package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/i18n"
	"github.com/BeCrafter/sail/internal/smbfs"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
)

var serveSmbOpts serveFlags

var serveSmbCmd = &cobra.Command{
	Use:   "smb",
	Short: "Share a bucket over SMB2 (mountable directly by macOS Finder / Windows Explorer)",
	Long: `Share the whole bucket over SMB2 (or the prefix given by --prefix); which bucket that is
comes from the profile, so name it with --profile (the global --bucket flag and SAIL_BUCKET
still override it when needed). Clients mount it with capabilities built into the OS, no
software to install.

Design boundaries:
  - SMB2's WRITE carries a 64-bit offset; the kernel's write handle is a sequential stream.
    Positional writes therefore land in a local staging file first and are uploaded when the
    client closes the handle, so point --staging-dir at a disk with room for the files being
    written (staging peak is about one file's size x concurrently open files, counted twice
    while the upload reads it back).
  - A failed commit cannot be reported to the client: the SMB2 library closes the handle
    without checking the result, so the client sees a successful CLOSE. Failures are logged
    on the server instead — watch the log if a file looks unchanged.
  - Quota and per-file size limits surface to clients as "permission denied", not as a
    distinct error: the library translates filesystem errors to SMB status codes itself and
    offers no hook to map them.
  - User names and passwords are NTLM credentials, not HTTP Basic; a client that authenticates
    is choosing a share, and in multi-user mode each user gets their own share named after
    them (a share binds exactly one filesystem, so "one share, different content per user"
    is not expressible).
  - The user table is NOT hot-reloaded: the library can add shares and users but never remove
    them, so changing serve.users requires a restart.
  - With --chunked-upload on, files larger than --chunk-size are stored as chunks under the
    reserved .sail/ prefix plus a small manifest object at the logical key: the bucket then
    contains .sail/ objects, and "sail presign" fails loud on such a bucket because a presigned
    URL would hand out the manifest instead of the file.

Examples:
  sail serve smb --profile prod --listen :1445 \
    --user alice --password '***' --share sail
  sail serve smb --profile prod --prefix shared --share sail
    # then mount smb://host:1445/sail`,
	Args: cobra.NoArgs,
	RunE: runServeSmb,
}

func runServeSmb(cmd *cobra.Command, _ []string) error {
	o := serveSmbOpts
	r, _, err := loadResolved()
	if err != nil {
		return err
	}
	s, err := mergeServeSMB(o, r, cmd.Flags().Changed)
	if err != nil {
		return err
	}

	// 与 WebDAV 同一条理由:SMB 共享的是整桶,没有桶就无从共享。
	if r.Bucket == "" {
		return errors.New(i18n.Tf(
			"no bucket to share: pass --bucket, set SAIL_BUCKET, or add \"bucket\" to profile %q in the config file — SMB exposes a whole bucket, and without one there is nothing to share",
			r.ProfileName))
	}

	logger := log.New(os.Stderr, "", log.LstdFlags)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	s3c, err := client.New(ctx, r)
	if err != nil {
		return err
	}

	rt := newCoreRuntime(s3c, r.Bucket, s.serveSettings, logger)
	users := effectiveUserTable(s.serveSettings)
	entries := make([]smbfs.UserEntry, 0, len(users))
	for _, u := range users {
		st, err := rt.coreStack(u)
		if err != nil {
			return err
		}
		entries = append(entries, smbfs.UserEntry{
			Name:        u.Name,
			Password:    u.Password,
			FileSystem:  st.qfs,
			PrewarmDirs: s.prewarm,
		})
		// 多用户模式下为用户空间补建目录 marker(I10);单用户兼容模式保持
		// 既有行为,不自动创建。异步、失败仅告警。
		if eff := config.EffectivePrefix(s.prefix, u.Prefix); len(users) > 1 && eff != "" {
			go func(eff string) {
				if err := ensureUserDir(s3c, r.Bucket, eff, logger); err != nil {
					logger.Print(i18n.Tf("WARN: creating directory for user space %q failed (users still work; restarting retries): %v", eff, err))
					return
				}
				logger.Print(i18n.Tf("directory created for user space: %s/", eff))
			}(eff)
		}
	}

	srv, err := smbfs.New(smbfs.Config{
		Users:       entries,
		Listen:      s.listen,
		Share:       s.share,
		ServerName:  s.serverName,
		StagingDir:  s.stagingDir,
		DirCacheTTL: s.dirCacheTTL,
		Logger:      logger,
	})
	if err != nil {
		return err
	}
	defer srv.Close()

	fmt.Fprint(os.Stderr, i18n.Tf(
		"sail smb started: %s  bucket=%s profile=%s%s user=%s max-object-size=%s staging=%s chunked=%s share=%s\n",
		s.listen, r.Bucket, r.ProfileName, exposePrefix(s.prefix), usersBanner(s.serveSettings),
		humanSize(s.maxUpload), stagingDirOf(s.stagingDir), chunkedText(s.chunkedUpload, s.chunkSize), s.share))
	for _, line := range userSpaceLines(s.serveSettings) {
		fmt.Fprint(os.Stderr, line)
	}
	for _, sh := range srv.Shares() {
		fmt.Fprint(os.Stderr, i18n.Tf("  share %s -> user %s\n", sh.Name, sh.User))
	}
	if urls := smbMountURLs(s.listen, srv.Shares()); len(urls) > 0 {
		fmt.Fprint(os.Stderr, i18n.Tf("  mount at: %s\n", strings.Join(urls, "  ")))
		if len(urls) > 1 {
			fmt.Fprint(os.Stderr, i18n.T("  (localhost = this machine; LAN IP = other devices)\n"))
		}
	}
	// 用户表不能热加载是库的能力边界(它能加共享与用户,却没有删的接口),
	// 启动时就说清楚,免得运维改了配置等半天没反应。
	fmt.Fprint(os.Stderr, i18n.T("  the user table is read at startup only: changing serve.users requires a restart\n"))

	return srv.ListenAndServe(ctx)
}

// newCoreRuntime 组装「只建栈、不建协议壳」的运行期状态。SMB 命令只需要内核栈
// 与共享的 S3 client:它没有网关、没有热加载(库不支持增删共享/用户)。
func newCoreRuntime(s3c *s3.Client, bucket string, s serveSettings, logger *log.Logger) *serveRuntime {
	return &serveRuntime{
		settings: s,
		bucket:   bucket,
		s3c:      s3c,
		logger:   logger,
		stacks:   map[string]*userStack{},
	}
}

// smbMountURLs 渲染可直接粘贴的 SMB 挂载地址(macOS 认 smb://host:端口/共享)。
// Windows 的写法不同(\\host@端口\共享),那条放在 README 与启动横幅的说明里。
func smbMountURLs(listen string, shares []smbfs.Share) []string {
	hosts, port, ok := listenHosts(listen)
	if !ok || len(shares) == 0 {
		return nil
	}
	urls := make([]string, 0, len(hosts)*len(shares))
	for _, h := range hosts {
		for _, sh := range shares {
			urls = append(urls, "smb://"+hostPort(h, port)+"/"+sh.Name)
		}
	}
	return urls
}

func init() {
	serveSmbCmd.Flags().StringVar(&serveSmbOpts.listen, "listen", ":1445", "listen address; 445 is the port SMB clients dial by default but it needs root, so this defaults to a high port and clients name it when mounting")
	serveSmbCmd.Flags().StringVar(&serveSmbOpts.prefix, "prefix", "", "shared root prefix (mapped to /); out-of-prefix paths are always rejected")
	serveSmbCmd.Flags().StringVar(&serveSmbOpts.user, "user", "", "NTLM user name (required)")
	serveSmbCmd.Flags().StringVar(&serveSmbOpts.password, "password", "", "NTLM password (required)")
	serveSmbCmd.Flags().StringVar(&serveSmbOpts.share, "share", "sail", "share name for the single-user case; in multi-user mode each user gets a share named after them instead")
	serveSmbCmd.Flags().StringVar(&serveSmbOpts.serverName, "server-name", "SAIL", "the name this server calls itself in the NTLM challenge (clients display it)")
	serveSmbCmd.Flags().StringVar(&serveSmbOpts.backendMaxSize, "backend-max-object-size", "5TiB", "declared backend per-object limit (S3 has no capability negotiation, it can't be probed)")
	serveSmbCmd.Flags().StringVar(&serveSmbOpts.maxUploadSize, "max-upload-size", "", "per-file size limit, defaults to --backend-max-object-size; over the limit the write is refused (clients see permission denied)")
	serveSmbCmd.Flags().StringVar(&serveSmbOpts.stagingDir, "staging-dir", "", "staging directory for positional writes, defaults to the system temp dir; peak is about one file's size x concurrently open files")
	serveSmbCmd.Flags().BoolVar(&serveSmbOpts.chunkedUpload, "chunked-upload", false, "store files larger than --chunk-size as chunks plus a manifest (default off: 1 file = 1 object)")
	serveSmbCmd.Flags().StringVar(&serveSmbOpts.chunkSize, "chunk-size", "4GiB", "max physical chunk size and the chunked-storage threshold (5MiB ~ 5GiB); requires --chunked-upload")
	serveSmbCmd.Flags().StringVar(&serveSmbOpts.dirCacheTTL, "dir-cache-ttl", "60s", "how long a directory listing and its entry metadata are cached (e.g. 60s, 10m; 0 disables); SMB's directory listing stats every entry one by one, so turning this off costs one backend round trip per entry")
	serveSmbCmd.Flags().StringSliceVar(&serveSmbOpts.prewarm, "prewarm", nil, "directories to keep hot in the background (comma-separated logical paths, e.g. /yiche,/modelImage); each is listed once at startup then refreshed, so the first visit does not pay the full listing cost")

	serveCmd.AddCommand(serveSmbCmd)
}
