// smbdrive —— spike 期的 SMB 客户端驱动,用 Go 客户端把验收动作脚本化。
// 原生客户端(macOS 挂载)另测,这里只做「服务器本身对不对」的快速定位。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"time"

	smb2 "github.com/hirochachacha/go-smb2"
)

func main() {
	var (
		addr  = flag.String("addr", "127.0.0.1:1445", "server address")
		user  = flag.String("user", "alice", "user")
		pass  = flag.String("password", "", "password")
		share = flag.String("share", "sail", "share")
		op    = flag.String("op", "ls", "ls|write|read|rename|rm|mkdir|rmdir|hashes|bigwrite")
		p     = flag.String("path", "", "path")
		dst   = flag.String("dst", "", "destination path")
		size  = flag.Int("size", 0, "bytes for write/bigwrite")
	)
	flag.Parse()

	conn, err := net.DialTimeout("tcp", *addr, 10*time.Second)
	if err != nil {
		fatal("dial", err)
	}
	defer conn.Close()

	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{User: *user, Password: *pass},
	}
	start := time.Now()
	s, err := d.Dial(conn)
	if err != nil {
		fatal("dial smb", err)
	}
	fmt.Printf("session established in %s (dialect negotiated)\n", time.Since(start).Round(time.Millisecond))
	defer s.Logoff()

	fs, err := s.Mount(*share)
	if err != nil {
		fatal("mount share", err)
	}
	defer fs.Umount()

	start = time.Now()
	switch *op {
	case "ls":
		entries, err := fs.ReadDir(*p)
		if err != nil {
			fatal("readdir", err)
		}
		fmt.Printf("readdir %q -> %d entries in %s\n", *p, len(entries), time.Since(start).Round(time.Millisecond))
		for _, e := range entries {
			kind := "-"
			if e.IsDir() {
				kind = "d"
			}
			fmt.Printf("  %s %10d %s\n", kind, e.Size(), e.Name())
		}
	case "mkdir":
		if err := fs.Mkdir(*p, 0o755); err != nil {
			fatal("mkdir", err)
		}
		fmt.Printf("mkdir %q ok in %s\n", *p, time.Since(start).Round(time.Millisecond))
	case "rmdir":
		if err := fs.RemoveAll(*p); err != nil {
			fatal("rmdir", err)
		}
		fmt.Printf("rmdir %q ok in %s\n", *p, time.Since(start).Round(time.Millisecond))
	case "write":
		data := make([]byte, *size)
		for i := range data {
			data[i] = byte('a' + i%26)
		}
		sum := sha256.Sum256(data)
		f, err := fs.Create(*p)
		if err != nil {
			fatal("create", err)
		}
		if _, err := f.Write(data); err != nil {
			fatal("write", err)
		}
		if err := f.Close(); err != nil {
			fatal("close", err)
		}
		fmt.Printf("write %q %d bytes sha=%s in %s\n", *p, len(data), hex.EncodeToString(sum[:8]), time.Since(start).Round(time.Millisecond))
	case "bigwrite":
		data := make([]byte, 1<<20)
		for i := range data {
			data[i] = byte(i)
		}
		f, err := fs.Create(*p)
		if err != nil {
			fatal("create", err)
		}
		h := sha256.New()
		written := 0
		for written < *size {
			n := len(data)
			if *size-written < n {
				n = *size - written
			}
			if _, err := f.Write(data[:n]); err != nil {
				fatal("write chunk", err)
			}
			h.Write(data[:n])
			written += n
		}
		if err := f.Close(); err != nil {
			fatal("close", err)
		}
		fmt.Printf("bigwrite %q %d bytes sha=%s in %s\n", *p, written, hex.EncodeToString(h.Sum(nil)[:8]), time.Since(start).Round(time.Millisecond))
	case "read":
		f, err := fs.Open(*p)
		if err != nil {
			fatal("open", err)
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		if err != nil {
			fatal("read", err)
		}
		f.Close()
		fmt.Printf("read %q %d bytes sha=%s in %s\n", *p, n, hex.EncodeToString(h.Sum(nil)[:8]), time.Since(start).Round(time.Millisecond))
	case "rm":
		if err := fs.Remove(*p); err != nil {
			fatal("remove", err)
		}
		fmt.Printf("remove %q ok in %s\n", *p, time.Since(start).Round(time.Millisecond))
	case "rename":
		start = time.Now()
		if err := fs.Rename(*p, *dst); err != nil {
			fatal("rename", err)
		}
		fmt.Printf("rename %q -> %q ok in %s\n", *p, *dst, time.Since(start).Round(time.Millisecond))
	case "hashes":
		f, err := fs.Open(*p)
		if err != nil {
			fatal("open", err)
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		if err != nil {
			fatal("read", err)
		}
		f.Close()
		fmt.Printf("sha256 %s %s (%d bytes)\n", path.Base(*p), hex.EncodeToString(h.Sum(nil)), n)
	default:
		fatal("op", fmt.Errorf("unknown op %q", *op))
	}
}

func fatal(what string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
	os.Exit(1)
}
