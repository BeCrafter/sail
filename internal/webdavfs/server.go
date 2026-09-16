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
	"sync/atomic"
	"time"

	"github.com/BeCrafter/sail/internal/vfs"
	"golang.org/x/net/webdav"
)

// Config 是 WebDAV HTTP 外壳的构造参数。单用户模式填 FileSystem/User/Password;
// 多用户模式填 Users,两种模式互斥。
type Config struct {
	// 单用户模式:该文件系统 + 一对 Basic 凭据。
	FileSystem *FileSystem
	User       string
	Password   string
	// Users 是多用户路由表(每用户一个已构建好的 FileSystem)。非空时启用
	// 多用户模式,上方 FileSystem/User/Password 必须为空。
	Users []UserEntry
	// MaxUploadSize 是请求体上限(字节);<= 0 表示不限制。
	MaxUploadSize int64
	// MaxUploadSizeText 是上限的人类可读文本,只用于 413 指引文案。
	MaxUploadSizeText string
	// Logger 为空则不记录访问日志。
	Logger *log.Logger
	// PrewarmDirs 是单用户模式下需要后台预热的目录路径(逻辑路径,如 "/yiche")。
	// 多用户模式下每个用户自己的预热目录在 UserEntry.PrewarmDirs 里。
	// 首个请求到达时启动,按缓存 TTL 周期刷新,让超大目录在用户访问前
	// 就处于热状态——它们的首次列举可能长达数十秒。
	PrewarmDirs []string
}

// UserEntry 是一名用户的路由表条目:凭据 + 该用户已构建好的文件系统。
// 两次 SwapUsers 传入同一 FileSystem 指针即视为同一栈复用(handler、
// MemLS 与预热 goroutine 原样保留,凭据热替换)。
type UserEntry struct {
	Name        string
	Password    string
	FileSystem  *FileSystem
	PrewarmDirs []string
}

// stack 是一名用户的协议栈:文件系统 + 预构建的 webdav.Handler(独立 MemLS)。
// 栈按 FileSystem 指针对应,凭据变化不重建;退役时只需停掉预热 goroutine,
// 在途请求持有的 handler 引用自然完成(I9)。
type stack struct {
	fs          *FileSystem
	handler     http.Handler
	prewarmStop func()
}

// userEntry 是路由表里的一条凭据记录,指向共享的栈。
type userEntry struct {
	password string
	stack    *stack
}

// Server 是 WebDAV 网关的 HTTP 外壳。
type Server struct {
	// stacks 是当前生效的用户路由表(name → entry),atomic.Value 快照替换:
	// 认证与路由 O(1),reload 换表对在途请求无感。
	stacks        atomic.Value // map[string]*userEntry
	maxUploadSize int64
	maxUploadText string
	logger        *log.Logger
}

// NewServer 组装中间件链:Basic 认证(查表)→ 按用户路由 → 请求态/上传闸门 →
// 目录级 MOVE/COPY 退化 → webdav.Handler。
func NewServer(cfg Config) (*Server, error) {
	if cfg.FileSystem == nil && len(cfg.Users) == 0 {
		return nil, errors.New("webdavfs: 缺少 FileSystem")
	}
	if len(cfg.Users) > 0 {
		if cfg.FileSystem != nil || cfg.User != "" || cfg.Password != "" {
			return nil, errors.New("webdavfs: Users 与单用户 FileSystem/User/Password 不能同时配置")
		}
	} else {
		if cfg.User == "" || cfg.Password == "" {
			return nil, errors.New("webdavfs: 必须提供 --user / --password,不允许匿名共享")
		}
	}
	s := &Server{
		maxUploadSize: cfg.MaxUploadSize,
		maxUploadText: cfg.MaxUploadSizeText,
		logger:        cfg.Logger,
	}
	entries := cfg.Users
	if len(entries) == 0 {
		entries = []UserEntry{{
			Name:        cfg.User,
			Password:    cfg.Password,
			FileSystem:  cfg.FileSystem,
			PrewarmDirs: cfg.PrewarmDirs,
		}}
	}
	// 构造即启动各用户的后台预热:进程生命周期内持续刷新,让热点目录在用户
	// 首次访问前就处于热状态。cmd/serve.go 仅在真正要监听时才构造本对象,
	// 故不会在「构造即丢弃」的场景平白发起后端列举。
	if err := s.SwapUsers(entries); err != nil {
		return nil, err
	}
	return s, nil
}

// SwapUsers 原子替换用户路由表(热加载的 Swap 步,I8/I9):
//   - 同一 FileSystem 指针 = 同一栈:只换凭据条目,handler/MemLS/预热保留,
//     密码热替换、改 quota(未来)都不重建栈;
//   - 新出现的 FileSystem 构建新栈(含启动预热);
//   - 消失的栈退役:预热 goroutine 取消,在途请求持旧 handler 引用自然完成。
func (s *Server) SwapUsers(entries []UserEntry) error {
	if len(entries) == 0 {
		return errors.New("webdavfs: 用户路由表不能为空,至少保留一名用户")
	}
	seenName := map[string]bool{}
	seenFS := map[*FileSystem]bool{}
	for _, e := range entries {
		if e.Name == "" {
			return errors.New("webdavfs: 用户名不能为空")
		}
		if e.Password == "" {
			return fmt.Errorf("webdavfs: 用户 %q 缺少密码,不允许匿名共享", e.Name)
		}
		if e.FileSystem == nil {
			return fmt.Errorf("webdavfs: 用户 %q 缺少 FileSystem", e.Name)
		}
		if seenName[e.Name] {
			return fmt.Errorf("webdavfs: 用户名 %q 重复", e.Name)
		}
		if seenFS[e.FileSystem] {
			return fmt.Errorf("webdavfs: 用户 %q 与其他用户共用同一 FileSystem", e.Name)
		}
		seenName[e.Name] = true
		seenFS[e.FileSystem] = true
	}

	old := s.current()
	oldByFS := map[*FileSystem]*userEntry{}
	for _, e := range old {
		oldByFS[e.stack.fs] = e
	}
	next := make(map[string]*userEntry, len(entries))
	for _, en := range entries {
		if oldE, ok := oldByFS[en.FileSystem]; ok {
			// 同栈复用:换凭据,不重建。
			next[en.Name] = &userEntry{password: en.Password, stack: oldE.stack}
			delete(oldByFS, en.FileSystem)
			continue
		}
		next[en.Name] = &userEntry{password: en.Password, stack: s.buildStack(en.FileSystem, en.PrewarmDirs)}
	}
	// 退役:未被新表复用的栈,停掉其预热 goroutine。
	for _, e := range oldByFS {
		if e.stack.prewarmStop != nil {
			e.stack.prewarmStop()
		}
	}
	s.stacks.Store(next)
	return nil
}

// buildStack 构建单用户协议栈。预热 goroutine 挂在可取消 ctx 上,
// 栈退役时随 prewarmStop 停止,不泄漏(I9)。
func (s *Server) buildStack(fs *FileSystem, prewarmDirs []string) *stack {
	st := &stack{fs: fs}
	dav := &webdav.Handler{
		FileSystem: fs,
		// 进程内锁:重启即失效,不跨实例。P1 的取舍见 README。
		LockSystem: webdav.NewMemLS(),
	}
	st.handler = s.withRequestState(s.withDirCopyMove(fs, dav))
	if len(prewarmDirs) > 0 && fs.dirs != nil {
		ctx, cancel := context.WithCancel(context.Background())
		fs.prewarm(ctx, prewarmDirs)
		st.prewarmStop = cancel
	}
	return st
}

func (s *Server) current() map[string]*userEntry {
	m, _ := s.stacks.Load().(map[string]*userEntry)
	return m
}

// allowMethods 是 OPTIONS 的能力头。取 x/net/webdav 对目录与文件的并集,
// 不区分具体资源——能力头是进程级常量,不该为它去查后端。
const allowMethods = "OPTIONS, LOCK, GET, HEAD, POST, PUT, DELETE, PROPPATCH, COPY, MOVE, UNLOCK, PROPFIND"

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		// OPTIONS 是能力探测(返回 DAV/Allow 头),不含任何资源数据;macOS Finder /
		// Windows WebClient 挂载时会先发一个不带凭据的 OPTIONS,收到 401 后不会带
		// 凭据重试,而是停在「连接中」。故 OPTIONS 在认证之前放行。
		//
		// 这里直接应答、不转给 webdav.Handler:后者会 Stat(reqPath) 才能决定
		// Allow 头(x/net/webdav/webdav.go),那等于让无凭据请求也能触达后端,
		// 且 Allow 会随「路由表里首个用户」的数据漂移。
		start := time.Now()
		w.Header().Set("DAV", "1, 2")
		w.Header().Set("MS-Author-Via", "DAV")
		w.Header().Set("Allow", allowMethods)
		w.WriteHeader(http.StatusOK)
		// 不转栈就没有栈里的访问日志中间件,这里补记一条,保持审计完整
		// (OPTIONS 无凭据,归因恒为 user=-)。
		s.logRequest(r, http.StatusOK, time.Since(start))
		return
	}
	e, name := s.authenticate(r)
	if e == nil {
		s.unauthorizedWithLog(w, r)
		return
	}
	ctx := context.WithValue(r.Context(), userCtxKey{}, name)
	e.stack.handler.ServeHTTP(w, r.WithContext(ctx))
}

// authenticate 查表认证:按用户名 O(1) 定位条目,密码做常量时间比较。
// 用户名存在性经 map 命中即可见——这是冻结契约认可的取舍(map + 单次常量时间比较)。
func (s *Server) authenticate(r *http.Request) (*userEntry, string) {
	user, pass, ok := r.BasicAuth()
	if !ok {
		return nil, ""
	}
	e := s.current()[user]
	if e == nil {
		return nil, ""
	}
	if subtle.ConstantTimeCompare([]byte(pass), []byte(e.password)) != 1 {
		return nil, ""
	}
	return e, user
}

// unauthorizedWithLog 在 401 的同时记一条日志:认证失败此前完全不可见,
// 排查「谁在用错密码」只能靠猜。
func (s *Server) unauthorizedWithLog(w http.ResponseWriter, r *http.Request) {
	s.logRequest(r, http.StatusUnauthorized, 0)
	s.unauthorized(w)
}

func (s *Server) unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="sail webdav", charset="UTF-8"`)
	http.Error(w, "需要认证", http.StatusUnauthorized)
}

// userCtxKey 是请求级「已认证用户名」的键,供访问日志归因。
type userCtxKey struct{}

func requestUser(ctx context.Context) string {
	if u, ok := ctx.Value(userCtxKey{}).(string); ok {
		return u
	}
	return "-"
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
func (s *Server) withDirCopyMove(fs *FileSystem, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "MOVE" || r.Method == "COPY" {
			if isDir, err := isDirOf(fs, r, r.URL.Path); err == nil && isDir {
				http.Error(w, "目录级 MOVE/COPY 在 P1 不支持(501),请退化为复制 + 删除", http.StatusNotImplemented)
				return
			}
			if dst := destinationPath(r); dst != "" {
				if isDir, err := isDirOf(fs, r, dst); err == nil && isDir {
					http.Error(w, "目标是目录时不做 MOVE/COPY(501),请退化为复制 + 删除", http.StatusNotImplemented)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isDirOf(fs *FileSystem, r *http.Request, name string) (bool, error) {
	fi, err := fs.Stat(r.Context(), name)
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

// slowRequestThreshold 超过即标记 SLOW,便于在日志里直接筛出慢请求。
const slowRequestThreshold = time.Second

func (s *Server) logRequest(r *http.Request, status int, elapsed time.Duration) {
	if s.logger == nil {
		return
	}
	// user= 是审计归因:已认证请求取自路由表命中项;OPTIONS 等未认证放行
	// 请求记为 "-"。cache=命中/未命中 用于判断目录缓存是否真的在起作用;
	// 上传被 413/507 拒时附上原因,省得再去翻响应体。
	line := fmt.Sprintf("%s %s %d %s user=%s", r.Method, r.URL.Path, status, elapsed.Round(time.Millisecond), requestUser(r.Context()))
	if st := stateOf(r.Context()); st != nil {
		hits, misses := st.cacheStats()
		if hits+misses > 0 {
			line += fmt.Sprintf(" cache=%d/%d", hits, misses)
		}
		if st.fatalErr() != nil {
			line += fmt.Sprintf(" rejected=%v", st.fatalErr())
		}
	}
	if elapsed >= slowRequestThreshold {
		line += " SLOW"
	}
	s.logger.Print(line)
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
	// 只登记能映射成明确状态码的错误(vfs 契约的五类);其余交给 webdav 的
	// 默认映射 —— 后端真实故障仍是 5xx,不会被这里吞掉。
	if !errors.Is(err, vfs.ErrTooLarge) && !errors.Is(err, vfs.ErrInsufficientStorage) &&
		!errors.Is(err, vfs.ErrNotExist) && !errors.Is(err, vfs.ErrExist) && !errors.Is(err, vfs.ErrNotSupported) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fatal == nil {
		s.fatal = err
	}
}

// fatalErr 返回本请求登记的上传拒绝原因(413/507),无则 nil。
func (st *requestState) fatalErr() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.fatal
}

// cacheStats 返回请求级缓存的命中/未命中数。
func (st *requestState) cacheStats() (hits, misses int) {
	if st.cache == nil {
		return 0, 0
	}
	return st.cache.stats()
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

// ReadFrom 让 io.Copy(GET 的响应体搬运)用上底层 ResponseWriter 的
// ReaderFrom(通常是 sendfile/大缓冲路径);不实现的话每次拷贝退化为
// 32KB 缓冲循环。注意转发前先补齐状态码,且不得递归调用自身。
func (w *guardWriter) ReadFrom(src io.Reader) (int64, error) {
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		w.state.mu.Lock()
		muted := w.state.muted
		w.state.mu.Unlock()
		if muted {
			return io.Copy(io.Discard, src) // 已被 413/507 接管:丢弃剩余数据
		}
		if w.status == 0 {
			w.WriteHeader(http.StatusOK)
		}
		return rf.ReadFrom(src)
	}
	return io.Copy(struct{ io.Writer }{w}, src)
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
	case errors.Is(cause, vfs.ErrNotExist):
		status = http.StatusNotFound
	case errors.Is(cause, vfs.ErrExist):
		status = http.StatusMethodNotAllowed
	case errors.Is(cause, vfs.ErrNotSupported):
		status = http.StatusNotImplemented
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
		if errors.Is(cause, vfs.ErrQuotaExceeded) {
			return `上传被拒绝:超出该用户的空间配额(507)。

可操作指引:
  1) 清理该用户空间内的旧文件释放空间;
  2) 或由运维调大配置文件中该用户的 quota,保存后热生效,无需重启。
`
		}
		return `上传被拒绝:服务端暂存盘空间不足,无法接收本次上传。

可操作指引:
  1) 清理 --staging-dir 指向的目录;
  2) 或将 --staging-dir 指向空间更大的磁盘后重启。
`
	case errors.Is(cause, vfs.ErrNotExist):
		return "资源不存在\n"
	case errors.Is(cause, vfs.ErrExist):
		return "目标已存在\n"
	case errors.Is(cause, vfs.ErrNotSupported):
		return "该操作在此路径上不受支持\n"
	default:
		return "操作失败\n"
	}
}
