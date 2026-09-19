// Package fakes3 是一个内存版 S3 兼容端点,仅供测试使用(不参与二进制构建)。
// 它覆盖 sail serve 用到的子集:HeadObject / GetObject(Range) /
// PutObject / CopyObject / DeleteObject / DeleteObjects / ListObjectsV2
// (含 Delimiter 与分页) / multipart 三件套,并统计各类请求次数,
// 便于断言「列目录不发 GetObject」这类行为约束。
package fakes3

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Object 是一个朴素对象:1 key = 1 份完整数据。
type Object struct {
	Data        []byte
	ContentType string
	// Metadata 是用户元数据(x-amz-meta-*),键为去前缀后的小写名。
	// sail serve 的 payload 元数据判定依赖它,故 fake 端必须能往返。
	Metadata map[string]string
	ModTime  time.Time
}

// Counts 记录各类请求次数。
type Counts struct {
	Head        atomic.Int64
	Get         atomic.Int64
	Put         atomic.Int64
	List        atomic.Int64
	Delete      atomic.Int64
	DeleteBatch atomic.Int64
	Copy        atomic.Int64
	Multipart   atomic.Int64
}

// Server 是内存 S3 端点。
type Server struct {
	mu      sync.Mutex
	objects map[string]Object
	parts   map[string]map[int][]byte
	uploads map[string]Object // uploadID -> 建单时记录的内容类型等
	nextID  int

	Counts Counts
	ts     *httptest.Server

	// BatchDeleteStatus 是 FailBatchDelete 生效时返回的状态码;0 表示 500
	// (多数网关代答「不支持」的形态),测试可显式设为 501 等。
	BatchDeleteStatus atomic.Int64
	// FailBatchDeleteKeys 置真时,批量删除返回 200 但每个 key 都带 <Error>
	// ——模拟「端点可用、部分对象删不掉」的真实形态。
	FailBatchDeleteKeys atomic.Bool
	// FailBatchDelete 置真时,批量删除端点(?delete)返回 500——模拟不支持
	// DeleteObjects 的 S3 兼容服务(如某些自建网关),用于验证回退路径。
	FailBatchDelete atomic.Bool
	// DeleteDelay 是单对象删除的注入延迟,用于把并发回退与串行回退区分开:
	// 并发执行时总耗时应接近单个延迟而非 N 倍。
	DeleteDelay atomic.Int64
	// CopyDelay 是单对象复制的注入延迟,用途同 DeleteDelay。
	CopyDelay atomic.Int64
}

// New 启动一个内存 S3 端点,调用方负责 Close。
func New() *Server {
	s := &Server{
		objects: map[string]Object{},
		parts:   map[string]map[int][]byte{},
		uploads: map[string]Object{},
	}
	s.ts = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// URL 是端点地址,可直接作为 endpoint 配置。
func (s *Server) URL() string { return s.ts.URL }

// Close 关闭端点。
func (s *Server) Close() { s.ts.Close() }

// Put 直接写入一个对象(测试夹具用)。
func (s *Server) Put(bucket, key string, data []byte, contentType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[bucket+"/"+key] = Object{Data: data, ContentType: contentType, ModTime: time.Now().UTC()}
}

// PutWithMetadata 写入一个带用户元数据的对象(测试夹具用)。
func (s *Server) PutWithMetadata(bucket, key string, data []byte, contentType string, md map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[bucket+"/"+key] = Object{Data: data, ContentType: contentType, Metadata: md, ModTime: time.Now().UTC()}
}

// Get 读取一个对象(测试断言用)。
func (s *Server) Get(bucket, key string) (Object, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[bucket+"/"+key]
	return o, ok
}

// Keys 列出所有 key(测试断言用)。
func (s *Server) Keys(bucket string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.objects {
		if strings.HasPrefix(k, bucket+"/") {
			out = append(out, strings.TrimPrefix(k, bucket+"/"))
		}
	}
	sort.Strings(out)
	return out
}

func etagOf(data []byte) string {
	sum := md5.Sum(data)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// userMetadata 取出请求里的 x-amz-meta-* 用户元数据(键去前缀、小写)。
func userMetadata(h http.Header) map[string]string {
	var md map[string]string
	for name, vals := range h {
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, "x-amz-meta-") || len(vals) == 0 {
			continue
		}
		if md == nil {
			md = map[string]string{}
		}
		md[strings.TrimPrefix(lower, "x-amz-meta-")] = vals[0]
	}
	return md
}

// writeMetadata 把用户元数据回写成 x-amz-meta-* 响应头。
func writeMetadata(h http.Header, md map[string]string) {
	for k, v := range md {
		h.Set("X-Amz-Meta-"+k, v)
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(path, "/")
	q := r.URL.Query()

	switch {
	case r.Method == http.MethodGet && q.Get("list-type") == "2":
		s.Counts.List.Add(1)
		s.listObjects(w, r, bucket)
	case r.Method == http.MethodPost && q.Has("delete"):
		s.Counts.DeleteBatch.Add(1)
		if s.FailBatchDelete.Load() {
			code := int(s.BatchDeleteStatus.Load())
			if code == 0 {
				code = http.StatusInternalServerError
			}
			http.Error(w, http.StatusText(code), code)
			return
		}
		if s.FailBatchDeleteKeys.Load() {
			s.deleteObjectsAllFail(w, r, bucket)
			return
		}
		s.deleteObjects(w, r, bucket)
	case r.Method == http.MethodPost && q.Has("uploads"):
		s.createMultipart(w, r, bucket, key)
	case r.Method == http.MethodPost && q.Has("uploadId"):
		s.completeMultipart(w, r, bucket, key, q.Get("uploadId"))
	case r.Method == http.MethodPut && q.Has("uploadId"):
		s.uploadPart(w, r, bucket, key, q.Get("uploadId"), q.Get("partNumber"))
	case r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source") != "":
		s.Counts.Copy.Add(1)
		s.copyObject(w, r, bucket, key)
	case r.Method == http.MethodPut:
		s.Counts.Put.Add(1)
		s.putObject(w, r, bucket, key)
	case r.Method == http.MethodHead:
		s.Counts.Head.Add(1)
		s.headObject(w, bucket, key)
	case r.Method == http.MethodGet:
		s.Counts.Get.Add(1)
		s.getObject(w, r, bucket, key)
	case r.Method == http.MethodDelete:
		s.Counts.Delete.Add(1)
		if d := s.DeleteDelay.Load(); d > 0 {
			time.Sleep(time.Duration(d))
		}
		s.mu.Lock()
		delete(s.objects, bucket+"/"+key)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unsupported", http.StatusNotImplemented)
	}
}

func (s *Server) headObject(w http.ResponseWriter, bucket, key string) {
	s.mu.Lock()
	o, ok := s.objects[bucket+"/"+key]
	s.mu.Unlock()
	if !ok {
		s.notFound(w, key)
		return
	}
	h := w.Header()
	h.Set("ETag", etagOf(o.Data))
	h.Set("Content-Length", strconv.Itoa(len(o.Data)))
	h.Set("Last-Modified", o.ModTime.Format(http.TimeFormat))
	if o.ContentType != "" {
		h.Set("Content-Type", o.ContentType)
	}
	writeMetadata(h, o.Metadata)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) getObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	s.mu.Lock()
	o, ok := s.objects[bucket+"/"+key]
	s.mu.Unlock()
	if !ok {
		s.notFound(w, key)
		return
	}
	h := w.Header()
	h.Set("ETag", etagOf(o.Data))
	h.Set("Last-Modified", o.ModTime.Format(http.TimeFormat))
	if o.ContentType != "" {
		h.Set("Content-Type", o.ContentType)
	}
	writeMetadata(h, o.Metadata)
	body := o.Data
	if rng := r.Header.Get("Range"); rng != "" {
		start, end, err := parseRange(rng, int64(len(o.Data)))
		if err != nil {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		body = o.Data[start : end+1]
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(o.Data)))
		h.Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

func parseRange(spec string, size int64) (int64, int64, error) {
	spec = strings.TrimPrefix(spec, "bytes=")
	startStr, endStr, ok := strings.Cut(spec, "-")
	if !ok {
		return 0, 0, fmt.Errorf("bad range %q", spec)
	}
	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, fmt.Errorf("bad range %q", spec)
	}
	end := size - 1
	if endStr != "" {
		if end, err = strconv.ParseInt(endStr, 10, 64); err != nil {
			return 0, 0, fmt.Errorf("bad range %q", spec)
		}
	}
	if end >= size {
		end = size - 1
	}
	if end < start {
		return 0, 0, fmt.Errorf("bad range %q", spec)
	}
	return start, end, nil
}

func (s *Server) putObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	s.objects[bucket+"/"+key] = Object{
		Data:        data,
		ContentType: r.Header.Get("Content-Type"),
		Metadata:    userMetadata(r.Header),
		ModTime:     time.Now().UTC(),
	}
	s.mu.Unlock()
	w.Header().Set("ETag", etagOf(data))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) copyObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	if d := s.CopyDelay.Load(); d > 0 {
		time.Sleep(time.Duration(d))
	}
	src := strings.TrimPrefix(r.Header.Get("X-Amz-Copy-Source"), "/")
	// SDK 会把源 key 做 URL 编码,这里解回来。
	if dec, err := urlUnescape(src); err == nil {
		src = dec
	}
	srcBucket, srcKey, _ := strings.Cut(src, "/")
	s.mu.Lock()
	o, ok := s.objects[srcBucket+"/"+srcKey]
	if ok {
		o.ModTime = time.Now().UTC()
		// CopyObject 默认 COPY 源对象的元数据与服务端属性。
		if md := userMetadata(r.Header); r.Header.Get("X-Amz-Metadata-Directive") == "REPLACE" {
			o.Metadata = md
		}
		s.objects[bucket+"/"+key] = o
	}
	s.mu.Unlock()
	if !ok {
		s.notFound(w, src)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	writeXML(w, copyResult{ETag: etagOf(o.Data), LastModified: o.ModTime.Format("2006-01-02T15:04:05.000Z")})
}

func (s *Server) deleteObjects(w http.ResponseWriter, r *http.Request, bucket string) {
	var in deleteRequest
	if err := xml.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	for _, o := range in.Objects {
		delete(s.objects, bucket+"/"+o.Key)
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	writeXML(w, deleteResult{})
}

// deleteObjectsAllFail 返回「端点可用但每个 key 都失败」的形态(200 + 逐 key
// <Error>),用于验证删除器不把它误判成「批量端点不支持」。
func (s *Server) deleteObjectsAllFail(w http.ResponseWriter, r *http.Request, bucket string) {
	var in deleteRequest
	if err := xml.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out := deleteResult{}
	for _, o := range in.Objects {
		out.Errors = append(out.Errors, deleteError{Key: o.Key, Code: "AccessDenied", Message: "injected failure"})
	}
	w.Header().Set("Content-Type", "application/xml")
	writeXML(w, out)
}

func (s *Server) createMultipart(w http.ResponseWriter, r *http.Request, bucket, key string) {
	s.Counts.Multipart.Add(1)
	s.mu.Lock()
	s.nextID++
	id := "upload-" + strconv.Itoa(s.nextID)
	s.parts[id] = map[int][]byte{}
	// CreateMultipartUpload 时就带上了 Content-Type 与用户元数据,Complete 时要保留下来。
	s.uploads[id] = Object{ContentType: r.Header.Get("Content-Type"), Metadata: userMetadata(r.Header)}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	writeXML(w, initiateResult{Bucket: bucket, Key: key, UploadID: id})
}

func (s *Server) uploadPart(w http.ResponseWriter, r *http.Request, bucket, key, id, partNo string) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	n, _ := strconv.Atoi(partNo)
	s.mu.Lock()
	if _, ok := s.parts[id]; ok {
		s.parts[id][n] = data
	}
	s.mu.Unlock()
	w.Header().Set("ETag", etagOf(data))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) completeMultipart(w http.ResponseWriter, r *http.Request, bucket, key, id string) {
	var in completeRequest
	if err := xml.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	stored := s.parts[id]
	meta := s.uploads[id]
	delete(s.parts, id)
	delete(s.uploads, id)
	var all []byte
	var md5cat []byte
	for _, p := range in.Parts {
		data, ok := stored[p.PartNumber]
		if !ok {
			s.mu.Unlock()
			http.Error(w, "missing part", http.StatusBadRequest)
			return
		}
		all = append(all, data...)
		sum := md5.Sum(data)
		md5cat = append(md5cat, sum[:]...)
	}
	total := md5.Sum(md5cat)
	etag := fmt.Sprintf(`"%s-%d"`, hex.EncodeToString(total[:]), len(in.Parts))
	s.objects[bucket+"/"+key] = Object{Data: all, ContentType: meta.ContentType, Metadata: meta.Metadata, ModTime: time.Now().UTC()}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	writeXML(w, completeResult{Bucket: bucket, Key: key, ETag: etag, Location: "/" + bucket + "/" + key})
}

func (s *Server) listObjects(w http.ResponseWriter, r *http.Request, bucket string) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	delim := q.Get("delimiter")
	maxKeys := 1000
	if v := q.Get("max-keys"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxKeys = n
		}
	}
	start := 0
	if tok := q.Get("continuation-token"); tok != "" {
		if raw, err := base64.StdEncoding.DecodeString(tok); err == nil {
			start, _ = strconv.Atoi(string(raw))
		}
	}

	type entry struct {
		key      string
		size     int64
		etag     string
		modTime  time.Time
		isPrefix bool
	}
	s.mu.Lock()
	seen := map[string]bool{}
	var entries []entry
	for full, o := range s.objects {
		b, k, _ := strings.Cut(full, "/")
		if b != bucket || !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := strings.TrimPrefix(k, prefix)
		if delim != "" {
			if idx := strings.Index(rest, delim); idx >= 0 {
				cp := prefix + rest[:idx+len(delim)]
				if !seen[cp] {
					seen[cp] = true
					entries = append(entries, entry{key: cp, isPrefix: true})
				}
				continue
			}
		}
		entries = append(entries, entry{key: k, size: int64(len(o.Data)), etag: etagOf(o.Data), modTime: o.ModTime})
	}
	s.mu.Unlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })

	end := start + maxKeys
	if end > len(entries) {
		end = len(entries)
	}
	if start > len(entries) {
		start = len(entries)
	}
	page := entries[start:end]
	truncated := end < len(entries)

	res := listResult{
		Xmlns:       "http://s3.amazonaws.com/doc/2006-03-01/",
		Name:        bucket,
		Prefix:      prefix,
		Delimiter:   delim,
		MaxKeys:     maxKeys,
		KeyCount:    len(page),
		IsTruncated: truncated,
	}
	if truncated {
		res.NextContinuationToken = base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(end)))
	}
	for _, e := range page {
		if e.isPrefix {
			res.CommonPrefixes = append(res.CommonPrefixes, commonPrefix{Prefix: e.key})
			continue
		}
		res.Contents = append(res.Contents, listContent{
			Key:          e.key,
			LastModified: e.modTime.Format("2006-01-02T15:04:05.000Z"),
			ETag:         e.etag,
			Size:         e.size,
		})
	}
	w.Header().Set("Content-Type", "application/xml")
	writeXML(w, res)
}

func (s *Server) notFound(w http.ResponseWriter, key string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusNotFound)
	io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>`+key+`</Message></Error>`)
}

func writeXML(w http.ResponseWriter, v any) {
	io.WriteString(w, xml.Header)
	enc := xml.NewEncoder(w)
	_ = enc.Encode(v)
	_ = enc.Flush()
}

func urlUnescape(s string) (string, error) {
	return url.PathUnescape(s)
}

type listResult struct {
	XMLName               xml.Name       `xml:"ListBucketResult"`
	Xmlns                 string         `xml:"xmlns,attr"`
	Name                  string         `xml:"Name"`
	Prefix                string         `xml:"Prefix"`
	Delimiter             string         `xml:"Delimiter,omitempty"`
	MaxKeys               int            `xml:"MaxKeys"`
	KeyCount              int            `xml:"KeyCount"`
	IsTruncated           bool           `xml:"IsTruncated"`
	NextContinuationToken string         `xml:"NextContinuationToken,omitempty"`
	Contents              []listContent  `xml:"Contents"`
	CommonPrefixes        []commonPrefix `xml:"CommonPrefixes"`
}

type listContent struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
}

type commonPrefix struct {
	Prefix string `xml:"Prefix"`
}

type copyResult struct {
	XMLName      xml.Name `xml:"CopyObjectResult"`
	ETag         string   `xml:"ETag"`
	LastModified string   `xml:"LastModified"`
}

type deleteRequest struct {
	XMLName xml.Name `xml:"Delete"`
	Objects []struct {
		Key string `xml:"Key"`
	} `xml:"Object"`
}

type deleteResult struct {
	XMLName xml.Name      `xml:"DeleteResult"`
	Errors  []deleteError `xml:"Error,omitempty"`
}

type deleteError struct {
	Key     string `xml:"Key"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

type initiateResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

type completeRequest struct {
	XMLName xml.Name `xml:"CompleteMultipartUpload"`
	Parts   []struct {
		PartNumber int    `xml:"PartNumber"`
		ETag       string `xml:"ETag"`
	} `xml:"Part"`
}

type completeResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	Location string   `xml:"Location"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
}
