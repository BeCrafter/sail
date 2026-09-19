package mimetype

import (
	"bytes"
	"mime"
	"path"
	"strings"
	"testing"
)

func TestLookupBuiltin(t *testing.T) {
	cases := []struct {
		name, alt, want string
	}{
		{"a.md", "", "text/markdown; charset=utf-8"},
		{"a.markdown", "", "text/markdown; charset=utf-8"},
		{"a.toml", "", "application/toml"},
		{"a.yaml", "", "text/yaml; charset=utf-8"},
		{"notes.yml", "", "text/yaml; charset=utf-8"},
		{"a.mjs", "", "text/javascript; charset=utf-8"},
		{"a.webmanifest", "", "application/manifest+json"},
		{"pic.avif", "", "image/avif"},
		{"photo.HEIC", "", "image/heic"}, // 扩展名大小写不敏感
		{"mod.wasm", "", "application/wasm"},
		// key 无扩展名时回退本地文件名
		{"noext", "b.md", "text/markdown; charset=utf-8"},
		// key 扩展名优先于本地文件名(用户改名意图优先)
		{"page.md", "source.png", "text/markdown; charset=utf-8"},
		// 目录名里的点不参与判定
		{"dir.d/file", "", ""},
		{"a.sailprobe", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := Lookup(c.name, c.alt); got != c.want {
			t.Errorf("Lookup(%q,%q) = %q,期望 %q", c.name, c.alt, got, c.want)
		}
	}
}

// 表外扩展名回退标准库:结果须与 mime.TypeByExtension 一致(值随宿主表而异,
// 故不写死,只校验回退这条链路)。
func TestLookupFallsBackToStdlib(t *testing.T) {
	for _, name := range []string{"a.exe", "a.m4b", "a.sailprobe"} {
		ext := path.Ext(name)
		if _, ok := builtin[ext]; ok {
			t.Fatalf("%s 已在内置表里,本用例失去意义", ext)
		}
		want := mime.TypeByExtension(ext)
		if got := Lookup(name, ""); got != want {
			t.Errorf("Lookup(%q) = %q,期望与标准库一致的 %q", name, got, want)
		}
	}
}

// 常见资源后缀覆盖:网页、代码、图片、视频、音频、字体、压缩包、文档各取代表。
func TestLookupCommonExtensions(t *testing.T) {
	cases := map[string]string{
		"a.html":  "text/html; charset=utf-8",
		"a.css":   "text/css; charset=utf-8",
		"a.mp4":   "video/mp4",
		"a.mkv":   "video/x-matroska",
		"a.mp3":   "audio/mpeg",
		"a.flac":  "audio/flac",
		"a.py":    "text/x-python; charset=utf-8",
		"a.go":    "text/x-go; charset=utf-8",
		"a.ts":    "text/typescript; charset=utf-8",
		"a.java":  "text/x-java-source; charset=utf-8",
		"a.sql":   "application/sql",
		"a.jpg":   "image/jpeg",
		"a.svg":   "image/svg+xml",
		"a.woff2": "font/woff2",
		"a.zip":   "application/zip",
		"a.docx":  "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"a.pdf":   "application/pdf",
	}
	for name, want := range cases {
		if got := Lookup(name, ""); got != want {
			t.Errorf("Lookup(%q) = %q,期望 %q", name, got, want)
		}
	}
}

// 表自身的语法约束,防止手写条目出现错字或不合规取值。
func TestBuiltinTableWellFormed(t *testing.T) {
	for ext, ct := range builtin {
		if !strings.HasPrefix(ext, ".") || ext != strings.ToLower(ext) || len(ext) < 2 {
			t.Errorf("键 %q 应为小写、带点前缀的扩展名", ext)
		}
		mt, params, err := mime.ParseMediaType(ct)
		if err != nil || !strings.Contains(mt, "/") {
			t.Errorf("%s: 值 %q 不是合法媒体类型(%v)", ext, ct, err)
			continue
		}
		if mt == OctetStream {
			t.Errorf("%s: 不应显式映射到兜底值 %s(留空即走兜底)", ext, OctetStream)
		}
		switch {
		case strings.HasPrefix(mt, "text/"):
			if params["charset"] == "" {
				t.Errorf("%s: 文本类型应带 charset,实际 %q", ext, ct)
			}
		case strings.HasPrefix(mt, "image/"), strings.HasPrefix(mt, "video/"),
			strings.HasPrefix(mt, "audio/"), strings.HasPrefix(mt, "font/"):
			if params["charset"] != "" {
				t.Errorf("%s: 二进制类型不应带 charset,实际 %q", ext, ct)
			}
		}
	}
}

func pngHead() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte{0}, 32)...)
}

func TestDetect(t *testing.T) {
	cases := []struct {
		desc, name, alt string
		head            []byte
		want            string
	}{
		{"扩展名命中优先于内容探测", "a.md", "", pngHead(), "text/markdown; charset=utf-8"},
		{"名字优先:txt 里是 JSON 仍按名字", "data.txt", "", []byte(`{"a":1}`), "text/plain; charset=utf-8"},
		{"无扩展名按内容探测 PNG", "noext", "", pngHead(), "image/png"},
		{"无扩展名探测出 JSON", "noext", "", []byte(`{"a":1}`), "application/json"},
		{"无扩展名探测出 CSV", "noext", "", []byte("a,b\n1,2\n"), "text/csv"},
		{"无扩展名探测出 HTML", "noext", "", []byte("<!DOCTYPE html><html></html>"), "text/html; charset=utf-8"},
		{"纯文本探测为 text/plain", "noext", "", []byte("hello"), "text/plain; charset=utf-8"},
		{"空内容不做探测", "empty", "", nil, OctetStream},
		{"空内容也不因文件名兜底成 text/plain", "empty", "", []byte{}, OctetStream},
		{"无扩展名且二进制未知", "bin", "", []byte{0x00, 0x01, 0x02, 0x03}, OctetStream},
	}
	for _, c := range cases {
		if got := Detect(c.name, c.alt, c.head); got != c.want {
			t.Errorf("%s: Detect(%q,%q,head %d 字节) = %q,期望 %q", c.desc, c.name, c.alt, len(c.head), got, c.want)
		}
	}
}

// 探测窗口 = 库的 3072 字节默认上限:magic 落在窗口之外时不应被识别。
func TestDetectTruncatesToSniffWindow(t *testing.T) {
	head := append(bytes.Repeat([]byte{0x00}, 4096), pngHead()...)
	if got := Detect("noext", "", head); got != OctetStream {
		t.Errorf("探测窗口之外的 magic 不应被识别,实际 %q", got)
	}
}

// IsGenericBinary 只认"没意见"的通用二进制值(含大小写与参数变体);
// 具体类型、空值与非法值都不算。
func TestIsGenericBinary(t *testing.T) {
	cases := map[string]bool{
		"application/octet-stream":                true,
		"binary/octet-stream":                     true,
		"Application/Octet-Stream":                true,
		"application/octet-stream; charset=utf-8": true,
		"application/pdf":                         false,
		"text/plain":                              false,
		"application/octet-streamish":             false,
		"":                                        false,
		"not a mime type":                         false,
	}
	for ct, want := range cases {
		if got := IsGenericBinary(ct); got != want {
			t.Errorf("IsGenericBinary(%q) = %v,期望 %v", ct, got, want)
		}
	}
}
