package webdavfs

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/BeCrafter/sail/internal/vfs"
	"golang.org/x/net/webdav"
)

// Config 是 WebDAV HTTP 外壳的构造参数。
type Config struct {
	FileSystem *FileSystem
	User       string
	Password   string
	// MaxUploadSize 是请求体上限(字节);<= 0 表示不限制。
	MaxUploadSize int64
	// MaxUploadSizeText 是上限的人类可读文本,只用于 413 指引文案。
	MaxUploadSizeText string
	// Logger 为空则不记录访问日志。
	Logger *log.Logger
	// PrewarmDirs 是需要后台预热的目录路径(逻辑路径,如 "/yiche")。
	// 首个请求到达时启动,按缓存 TTL 周期刷新,让超大目录在用户访问前
	// 就处于热状态——它们的首次列举可能长达数十秒。
	PrewarmDirs []string
}

// Server 是 WebDAV 网关的 HTTP 外壳。
type Server struct {
	fs            *FileSystem
	user          string
	password      string
	maxUploadSize int64
	maxUploadText string
	logger        *log.Logger
	handler       http.Handler

	// prewarmDirs 是需要后台预热的目录;NewServer 时即启动,随进程存活。
	prewarmDirs []string
}

// NewServer 组装中间件链:
// Basic 认证 → 请求态/上传闸门 → 目录级 MOVE/COPY 退化 → webdav.Handler。
func NewServer(cfg Config) (*Server, error) {
	if cfg.FileSystem == nil {
		return nil, errors.New("webdavfs: 缺少 FileSystem")
	}
	if cfg.User == "" || cfg.Password == "" {
		return nil, errors.New("webdavfs: 必须提供 --user / --password,不允许匿名共享")
	}
	s := &Server{
		fs:            cfg.FileSystem,
		user:          cfg.User,
		password:      cfg.Password,
		maxUploadSize: cfg.MaxUploadSize,
		maxUploadText: cfg.MaxUploadSizeText,
		logger:        cfg.Logger,
		prewarmDirs:   cfg.PrewarmDirs,
	}
	dav := &webdav.Handler{
		FileSystem: cfg.FileSystem,
		// 进程内锁:重启即失效,不跨实例。P1 的取舍见 README。
		LockSystem: webdav.NewMemLS(),
	}
	s.handler = s.withBasicAuth(s.withRequestState(s.withDirCopyMove(dav)))
	// 构造即启动后台预热:进程生命周期内持续刷新,让热点目录在用户首次
	// 访问前就处于热状态。这些目录的首次列举可能长达数十秒,预热把这份
	// 代价挪到点击之前。cmd/serve.go 仅在真正要监听时才构造本对象,
	// 故不会在「构造即丢弃」的场景平白发起后端列举。
	if len(s.prewarmDirs) > 0 {
		s.fs.prewarm(context.Background(), s.prewarmDirs)
	}
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// withBasicAuth 拒绝一切未认证请求,但放行 OPTIONS。
// OPTIONS 是能力探测(返回 DAV/Allow 头),不含任何资源数据;macOS Finder /
// Windows WebClient 挂载时会先发一个不带凭据的 OPTIONS,收到 401 后不会带凭据
// 重试,而是停在「连接中」。故 OPTIONS 必须在认证之前放行。
func (s *Server) withBasicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(user), []byte(s.user)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(s.password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="sail webdav", charset="UTF-8"`)
			http.Error(w, "需要认证", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withRequestState 建立请求级状态(元信息缓存 + 上传闸门),并在读请求体之前
// 用 Content-Length 拒绝超限上传。
func (s *Server) withRequestState(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := &requestState{
			cache:         newReadCache(),
			method:        r.Method,
			maxUploadText: s.maxUploadText,
		}
		// Content-Type / Content-Length 只在带请求体的方法上才有意义。
		// COPY/MOVE 没有请求体,误当成 0 会让写句柄把「长度对不上」判成异常。
		if r.Method == http.MethodPut || r.Method == http.MethodPost {
			st.contentType = r.Header.Get("Content-Type")
			st.contentLength = r.ContentLength
		} else {
			st.contentLength = -1
		}
		r = r.WithContext(context.WithValue(r.Context(), stateCtxKey{}, st))
		gw := &guardWriter{ResponseWriter: w, state: st}

		start := time.Now()
		if s.maxUploadSize > 0 && r.ContentLength > s.maxUploadSize {
			// 上限在请求头就已可见:不读一个字节 body 直接拒绝,桶内无残留。
			writeGuardResponse(gw, vfs.ErrTooLarge)
		} else {
			next.ServeHTTP(gw, r)
		}
		s.logRequest(r, gw.status, time.Since(start))
	})
}

// withDirCopyMove 在 P1 拦掉所有涉及目录的 MOVE/COPY,让客户端退化为
// 「复制 + 删除」:
//   - 源是目录:webdav 会把 Rename 的任何错误映射成 403,拿不到 501;
//   - 目标是已存在的目录:webdav 的 moveFiles 会先 RemoveAll(目标) 再
//     Rename,那样会把整个目录递归删掉。
func (s *Server) withDirCopyMove(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "MOVE" || r.Method == "COPY" {
			if isDir, err := s.isDir(r, r.URL.Path); err == nil && isDir {
				http.Error(w, "目录级 MOVE/COPY 在 P1 不支持(501),请退化为复制 + 删除", http.StatusNotImplemented)
				return
			}
			if dst := destinationPath(r); dst != "" {
				if isDir, err := s.isDir(r, dst); err == nil && isDir {
					http.Error(w, "目标是目录时不做 MOVE/COPY(501),请退化为复制 + 删除", http.StatusNotImplemented)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) isDir(r *http.Request, name string) (bool, error) {
	fi, err := s.fs.Stat(r.Context(), name)
	if err != nil {
		return false, err
	}
	return fi.IsDir(), nil
}

// destinationPath 从 Destination 头里取出目标路径;同宿主校验交给 webdav 自己。
func destinationPath(r *http.Request) string {
	hdr := r.Header.Get("Destination")
	if hdr == "" {
		return ""
	}
	u, err := url.Parse(hdr)
	if err != nil {
		return ""
	}
	return u.Path
}

func (s *Server) logRequest(r *http.Request, status int, elapsed time.Duration) {
	if s.logger == nil {
		return
	}
	s.logger.Printf("%s %s %d %s", r.Method, r.URL.Path, status, elapsed.Round(time.Millisecond))
}

// stateCtxKey 是请求级状态在 context 中的键。
type stateCtxKey struct{}

// requestState 承载单个 HTTP 请求的壳级状态。
type requestState struct {
	cache         *readCache
	method        string
	contentType   string
	contentLength int64
	maxUploadText string

	mu    sync.Mutex
	fatal error
	muted bool
}

// overridesContentType 只在 GET/HEAD 上生效:PROPFIND 等的响应体是
// WebDAV XML,不能拿被请求对象的类型去覆盖。
func (s *requestState) overridesContentType() bool {
	return s.method == http.MethodGet || s.method == http.MethodHead
}

func (s *requestState) setFatal(err error) {
	// 只登记能映射成明确状态码的错误;其余交给 webdav 的默认映射。
	if !errors.Is(err, vfs.ErrTooLarge) && !errors.Is(err, vfs.ErrInsufficientStorage) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fatal == nil {
		s.fatal = err
	}
}

func stateOf(ctx context.Context) *requestState {
	st, _ := ctx.Value(stateCtxKey{}).(*requestState)
	return st
}

func cacheOf(ctx context.Context) *readCache {
	if st := stateOf(ctx); st != nil {
		return st.cache
	}
	return nil
}

func invalidateCache(ctx context.Context, prefix string) {
	if st := stateOf(ctx); st != nil {
		st.cache.invalidate(prefix)
	}
}

func registerFatal(ctx context.Context, err error) {
	if st := stateOf(ctx); st != nil {
		st.setFatal(err)
	}
}

func registerContentType(ctx context.Context, ct string) {
	if st := stateOf(ctx); st != nil {
		st.contentType = ct
	}
}

func requestContentType(ctx context.Context) string {
	if st := stateOf(ctx); st != nil {
		return st.contentType
	}
	return ""
}

func requestContentLength(ctx context.Context) int64 {
	if st := stateOf(ctx); st != nil {
		return st.contentLength
	}
	return -1
}

// guardWriter 兜住 webdav.Handler 的响应:把写路径登记的上传错误
// (413/507)换成可读响应,并让 GET/HEAD 回给客户端对象里存的 Content-Type。
type guardWriter struct {
	http.ResponseWriter
	state  *requestState
	status int
}

func (w *guardWriter) WriteHeader(code int) {
	w.state.mu.Lock()
	if w.state.muted {
		w.state.mu.Unlock()
		return
	}
	if fatal := w.state.fatal; fatal != nil {
		w.state.muted = true
		w.state.mu.Unlock()
		writeGuardResponse(w, fatal)
		return
	}
	ct := w.state.contentType
	override := w.state.overridesContentType()
	w.state.mu.Unlock()

	if ct != "" && override {
		w.ResponseWriter.Header().Set("Content-Type", ct)
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *guardWriter) Write(p []byte) (int, error) {
	w.state.mu.Lock()
	muted := w.state.muted
	w.state.mu.Unlock()
	if muted {
		// 已被 413/507 接管:丢弃 handler 后续写的状态文本。
		return len(p), nil
	}
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func writeGuardResponse(w *guardWriter, cause error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(cause, vfs.ErrTooLarge):
		status = http.StatusRequestEntityTooLarge
	case errors.Is(cause, vfs.ErrInsufficientStorage):
		status = http.StatusInsufficientStorage
	}
	w.ResponseWriter.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.ResponseWriter.WriteHeader(status)
	io.WriteString(w.ResponseWriter, guidance(cause, w.state.maxUploadText))
	w.status = status
}

// guidance 给的是可操作指引,不是错误码复述。
func guidance(cause error, maxUploadText string) string {
	limit := maxUploadText
	if limit == "" {
		limit = "已配置值"
	}
	switch {
	case errors.Is(cause, vfs.ErrTooLarge):
		return fmt.Sprintf(`上传被拒绝:文件超过服务端声明的单对象上限 %s。

可操作指引:
  1) 调高服务端 --backend-max-object-size(--max-upload-size 默认跟随它);
  2) 或改用 sail cp 上传到同一 bucket。
`, limit)
	case errors.Is(cause, vfs.ErrInsufficientStorage):
		return `上传被拒绝:服务端暂存盘空间不足,无法接收本次上传。

可操作指引:
  1) 清理 --staging-dir 指向的目录;
  2) 或将 --staging-dir 指向空间更大的磁盘后重启。
`
	default:
		return "上传失败\n"
	}
}
