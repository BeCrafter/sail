// Package smbfs 是 SMB2 协议壳:把协议无关的 vfs.FileSystem 暴露成操作系统自带
// 客户端可直接挂载的 SMB 共享(sail serve smb)。
//
// 与 internal/webdavfs 同级同职责:壳只经 vfs 契约触达内核,不直连 S3;
// 依赖方向仍是 协议壳 → vfs → S3。所选服务端库(github.com/go-filesystems/smb,
// BSD-3-Clause)的能力差异全部关在本包内 —— 换库或撤掉本层都不动内核。
//
// 两处由库能力决定、与 WebDAV 壳不同的结构约束(不是取舍,是上游接口使然):
//   - 一个共享只绑一个文件系统,故多用户模式下每个用户一个共享
//     (\\host\<用户名>),用库的 AllowUsers 保证互不越权;
//   - 库没有删除共享/用户的接口,故用户表不能像 WebDAV 那样热加载,
//     改配置需要重启。
package smbfs

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/BeCrafter/sail/internal/vfs"
	smb "github.com/go-filesystems/smb"
)

// UserEntry 是一名用户的共享条目:凭据 + 已装配好的内核栈(内核 → 配额装饰器)。
// 与 webdavfs.UserEntry 同构,由 cmd 层组装。
type UserEntry struct {
	Name       string
	Password   string
	FileSystem vfs.FileSystem
	// PrewarmDirs 是该用户空间内需要后台保热的目录(逻辑路径)。
	PrewarmDirs []string
}

// Share 描述一个生效的共享:哪个用户、挂载名是什么。多用户模式下每用户一个。
type Share struct {
	User string
	Name string
}

// Config 是 SMB 服务的装配参数,与 serve smb 的命令行参数一一对应。
type Config struct {
	Users []UserEntry
	// Listen 是监听地址。默认必须是高位端口:SMB 客户端的惯例端口 445 是特权
	// 端口,与「单二进制、零特权」的定位冲突。
	Listen string
	// Share 是单用户模式下的共享名;多用户模式下每个用户一个共享,名即用户名。
	Share string
	// ServerName 是 NTLM 挑战里服务端自称的名字,客户端界面会显示它。
	ServerName string
	// StagingDir 是壳层定位写的暂存目录;空 = 系统临时目录。
	StagingDir string
	// DirCacheTTL 是目录列表与条目元信息的缓存时长;<= 0 表示关闭。
	// 关掉会让「列目录」退化成每条目一次后端往返,不建议(见 cache.go)。
	DirCacheTTL time.Duration
	Logger      *log.Logger
}

// Server 是装配好的 SMB 服务。
type Server struct {
	cfg    Config
	lib    *smb.Server
	shares []Share
	caches []*cachedFS
	cancel context.CancelFunc
}

// ShareNameError describes a share-name validation failure. It stays in this
// package so the protocol layer does not depend on the CLI's presentation
// language; the command boundary can translate it before showing it to users.
type ShareNameError struct {
	Name         string
	FromUserName bool
	Reason       ShareNameErrorReason
}

// ShareNameErrorReason identifies the invalid share-name rule.
type ShareNameErrorReason uint8

const (
	ShareNameEmpty ShareNameErrorReason = iota
	ShareNamePathSeparator
	ShareNameReserved
	ShareNameColon
)

func (e *ShareNameError) Error() string {
	origin := "the share name"
	if e.FromUserName {
		origin = "the user name, which is the share name in multi-user mode,"
	}
	switch e.Reason {
	case ShareNameEmpty:
		return fmt.Sprintf("%s must not be empty", origin)
	case ShareNamePathSeparator:
		return fmt.Sprintf("%s contains a path separator, which SMB does not allow in a share name (%q)", origin, e.Name)
	case ShareNameReserved:
		return fmt.Sprintf("%s is the SMB protocol's own IPC$ name (%q)", origin, e.Name)
	case ShareNameColon:
		return fmt.Sprintf("%s contains \":\", which clients read as a port or stream separator (%q)", origin, e.Name)
	default:
		return fmt.Sprintf("%s is invalid (%q)", origin, e.Name)
	}
}

// DuplicateShareError describes two users that would register the same SMB
// share. It is also translated by the command boundary.
type DuplicateShareError struct {
	PreviousUser string
	User         string
	Name         string
}

func (e *DuplicateShareError) Error() string {
	return fmt.Sprintf("smbfs: users %q and %q both map to share %q; share names must differ", e.PreviousUser, e.User, e.Name)
}

// New 装配服务:为每个用户包一层缓存装饰器,建共享,登记凭据。
// 共享与用户都是库的不可变结构(创建后无法删除),故这里一次性建完。
func New(cfg Config) (*Server, error) {
	if len(cfg.Users) == 0 {
		return nil, errors.New("smbfs: at least one user is required")
	}
	shares, err := shareNames(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	lib := smb.New()
	lib.SetName(cfg.ServerName)
	s := &Server{cfg: cfg, lib: lib, shares: shares, cancel: cancel}
	for i := range cfg.Users {
		u := cfg.Users[i]
		core := newCachedFS(u.FileSystem, cfg.DirCacheTTL, cfg.Logger)
		s.caches = append(s.caches, core)
		lib.AddUser(u.Name, u.Password)
		// AllowUsers 是共享级互斥:多用户下没有它,任何登录用户都能挂载
		// 别人的空间(库的默认语义是「认证过的都能连」)。
		if err := lib.Share(shares[i].Name, newBackend(core, cfg.StagingDir, cfg.Logger), smb.AllowUsers(u.Name)); err != nil {
			cancel()
			return nil, fmt.Errorf("smbfs: share %q (user %q): %w", shares[i].Name, u.Name, err)
		}
		core.prewarm(ctx, u.PrewarmDirs)
	}
	return s, nil
}

// Shares 返回生效的共享清单(用户 → 挂载名),供启动横幅与文档使用。
func (s *Server) Shares() []Share { return s.shares }

// ListenAndServe 在 cfg.Listen 上服务,直到 ctx 取消或被关闭。
// 监听失败(端口占用等)立即返回错误;ctx 取消后优雅收摊并返回 nil。
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	return s.serve(ctx, ln)
}

// serve 接受一个已建好的监听器。库的 Serve 在 Close 后返回 nil,故取消时
// 主动 Close 再等它退出,不泄漏 goroutine。
func (s *Server) serve(ctx context.Context, ln net.Listener) error {
	errc := make(chan error, 1)
	go func() { errc <- s.lib.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		s.Close()
		<-errc
		return nil
	}
}

// Close 停掉预热 goroutine 并关闭监听与全部连接;幂等。
func (s *Server) Close() error {
	s.cancel()
	return s.lib.Close()
}

// shareNames 为每个用户决定共享名。
//
// SMB 的共享名是访问路径的一段(\\host\<共享名>),而一个共享只绑一个文件
// 系统,所以「同一共享按凭据显示不同内容」在这里做不到:多用户只能一人一个
// 共享,共享名取用户名(挂载时一眼能看出是谁的空间);单用户沿用 --share。
func shareNames(cfg Config) ([]Share, error) {
	out := make([]Share, 0, len(cfg.Users))
	seen := make(map[string]string, len(cfg.Users))
	for _, u := range cfg.Users {
		name := cfg.Share
		if len(cfg.Users) > 1 {
			name = u.Name
		}
		if err := validateShareName(name, len(cfg.Users) > 1); err != nil {
			return nil, fmt.Errorf("smbfs: user %q: %w", u.Name, err)
		}
		if prev, dup := seen[strings.ToUpper(name)]; dup {
			return nil, &DuplicateShareError{PreviousUser: prev, User: u.Name, Name: name}
		}
		seen[strings.ToUpper(name)] = u.Name
		out = append(out, Share{User: u.Name, Name: name})
	}
	return out, nil
}

// validateShareName 校验共享名。库自己对空名与分隔符报错,但报错信息不带出处,
// 而多用户下共享名取自用户名 —— 说清楚「是用户名不能用」比让用户去猜有用。
func validateShareName(name string, fromUserName bool) error {
	switch {
	case name == "":
		return &ShareNameError{Name: name, FromUserName: fromUserName, Reason: ShareNameEmpty}
	case strings.ContainsAny(name, `\/`):
		return &ShareNameError{Name: name, FromUserName: fromUserName, Reason: ShareNamePathSeparator}
	case strings.EqualFold(name, "IPC$"):
		return &ShareNameError{Name: name, FromUserName: fromUserName, Reason: ShareNameReserved}
	case strings.Contains(name, ":"):
		return &ShareNameError{Name: name, FromUserName: fromUserName, Reason: ShareNameColon}
	}
	return nil
}
