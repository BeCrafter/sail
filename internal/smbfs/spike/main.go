// sail-smb-spike —— CRAF-1 阶段 0 的选型 spike 入口(一次性代码)。
//
//	go run ./internal/smbfs/spike --impl sonroyaalmerol --profile test \
//	  --prefix _spike/smb --listen 127.0.0.1:1445 --user alice --password secret
//
// 指向真实后端,由外部脚本用原生客户端(macOS mount_smbfs)与 Go 客户端验收。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/config"
	"github.com/BeCrafter/sail/internal/vfs"
	"github.com/BeCrafter/sail/internal/vfs/quotafs"
	"github.com/BeCrafter/sail/internal/vfs/s3fs"

	gofssmb "github.com/go-filesystems/smb"
	sonntlm "github.com/sonroyaalmerol/go-smb-server/smb/ntlmssp"
	sonserver "github.com/sonroyaalmerol/go-smb-server/smb/server"
	sonvfs "github.com/sonroyaalmerol/go-smb-server/smb/vfs"
)

func main() {
	var (
		impl       = flag.String("impl", "sonroyaalmerol", "sonroyaalmerol | gofilesystems")
		profile    = flag.String("profile", "", "sail config profile")
		bucket     = flag.String("bucket", "", "override bucket")
		prefix     = flag.String("prefix", "", "bucket-relative root prefix")
		listen     = flag.String("listen", "127.0.0.1:1445", "listen address")
		user       = flag.String("user", "alice", "user name")
		password   = flag.String("password", "secret", "password")
		share      = flag.String("share", "sail", "share name")
		serverName = flag.String("server-name", "SAIL", "NTLM target name")
		stagingDir = flag.String("staging-dir", "", "staging dir")
		quota      = flag.String("quota", "", "quota like 10MiB (empty = unlimited)")
		chunked    = flag.Bool("chunked-upload", false, "enable chunked storage")
		chunkSize  = flag.Int64("chunk-size", 0, "chunk size bytes")
		debug      = flag.Bool("debug", false, "log every core call with latency")
		dirCache   = flag.Duration("dir-cache-ttl", 0, "directory listing cache TTL (0 = off)")
	)
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	r, err := cfg.Resolve(*profile)
	if err != nil {
		log.Fatalf("resolve profile: %v", err)
	}
	if *bucket != "" {
		r.Bucket = *bucket
	}
	s3c, err := client.New(ctx, r)
	if err != nil {
		log.Fatalf("s3 client: %v", err)
	}

	core, err := s3fs.New(s3fs.Config{
		Client:        s3c,
		Bucket:        r.Bucket,
		Prefix:        *prefix,
		StagingDir:    *stagingDir,
		ChunkedUpload: *chunked,
		ChunkSize:     *chunkSize,
	})
	if err != nil {
		log.Fatalf("s3fs: %v", err)
	}

	var fs vfs.FileSystem = core
	if *quota != "" {
		limit, err := config.ParseQuota(*quota)
		if err != nil {
			log.Fatalf("quota: %v", err)
		}
		q := quotafs.New(core, core, limit, 0, log.New(os.Stderr, "[quota] ", 0))
		q.SetLabel("quotafs[" + *user + "]")
		q.Refresh(ctx)
		fs = q
	}

	// 顺序要紧:logFS 贴着内核,只记真正打到后端的调用;缓存盖在最外面。
	if *debug {
		fs = &logFS{fs: fs, log: log.New(os.Stderr, "[core] ", log.Lmicroseconds)}
	}
	if *dirCache > 0 {
		fs = newCachedFS(fs, *dirCache)
	}
	ops := &coreOps{fs: fs}
	log.Printf("spike impl=%s bucket=%s prefix=%q listen=%s share=%s user=%s quota=%q",
		*impl, r.Bucket, *prefix, *listen, *share, *user, *quota)

	switch *impl {
	case "sonroyaalmerol":
		creds := sonntlm.NewMemoryCredentials()
		creds.Add("", *user, *password)
		creds.Add("WORKGROUP", *user, *password)
		creds.Add("SAIL", *user, *password)
		srv, err := sonserver.New(
			sonserver.WithAddr(*listen),
			sonserver.WithShares(sonvfs.NewDiskShare(*share, &sonBackend{ops: ops})),
			sonserver.WithAuth(sonntlm.NewServer(creds, *serverName)),
		)
		if err != nil {
			log.Fatalf("server: %v", err)
		}
		log.Printf("serving (sonroyaalmerol) pid=%d", os.Getpid())
		if err := srv.ListenAndServe(ctx); err != nil {
			log.Fatalf("serve: %v", err)
		}
	case "gofilesystems":
		srv := gofssmb.New()
		srv.SetName(*serverName)
		srv.AddUser(*user, *password)
		if err := srv.Share(*share, &gofsAdapter{ops: ops}); err != nil {
			log.Fatalf("share: %v", err)
		}
		log.Printf("serving (gofilesystems) pid=%d", os.Getpid())
		if err := srv.ListenAndServe(*listen); err != nil {
			log.Fatalf("serve: %v", err)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown --impl %q\n", *impl)
		os.Exit(2)
	}
	time.Sleep(100 * time.Millisecond)
}
