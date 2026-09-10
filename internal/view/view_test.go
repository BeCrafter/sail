package view

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSuffixRange(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    int64
		wantErr bool
	}{
		{name: "正常后缀窗口", in: "bytes=-10", want: 10},
		{name: "零字节窗口", in: "bytes=-0", want: 0},
		{name: "非后缀 Range 报错", in: "bytes=10-20", wantErr: true},
		{name: "非数字报错", in: "bytes=-abc", wantErr: true},
		{name: "空串报错", in: "", wantErr: true},
		{name: "负数报错", in: "bytes=--5", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseSuffixRange(c.in)
			if c.wantErr {
				if err == nil {
					t.Errorf("parseSuffixRange(%q) 期望报错,实际 %d", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSuffixRange(%q) 报错: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("parseSuffixRange(%q) = %d,期望 %d", c.in, got, c.want)
			}
		})
	}
}

func TestParseFormat(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Format
		ok   bool
	}{
		{"空串=auto", "", FormatAuto, true},
		{"auto", "auto", FormatAuto, true},
		{"大小写不敏感", "JSON", FormatJSON, true},
		{"txt 别名", "txt", FormatText, true},
		{"yml 别名", "yml", FormatYAML, true},
		{"tsv 别名", "tsv", FormatCSV, true},
		{"svg 别名", "svg", FormatXML, true},
		{"img 别名", "img", FormatImage, true},
		{"bin 别名", "bin", FormatBinary, true},
		{"未知值", "pdf", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseFormat(c.in)
			if ok != c.ok || (c.ok && got != c.want) {
				t.Errorf("ParseFormat(%q) = %v,%v,期望 %v,%v", c.in, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestMimeFormat(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Format
		ok   bool
	}{
		{"csv 带 charset", "text/csv; charset=utf-8", FormatCSV, true},
		{"tsv 变体", "text/tab-separated-values", FormatCSV, true},
		{"纯文本", "text/plain", FormatText, true},
		{"json", "application/json", FormatJSON, true},
		{"yaml", "application/x-yaml", FormatYAML, true},
		{"xml", "text/xml", FormatXML, true},
		{"image 前缀", "image/png", FormatImage, true},
		{"svg 当前命中 image(固化现状)", "image/svg+xml", FormatImage, true},
		{"未知类型", "application/octet-stream", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := mimeFormat(c.in)
			if ok != c.ok || (c.ok && got != c.want) {
				t.Errorf("mimeFormat(%q) = %v,%v,期望 %v,%v", c.in, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestDetectFormat(t *testing.T) {
	// 扩展名优先
	if got := DetectFormat(&Source{Name: "a.json"}); got != FormatJSON {
		t.Errorf("a.json = %v,期望 JSON", got)
	}
	// 扩展名未命中,ContentType 命中(含参数剥离)
	if got := DetectFormat(&Source{Name: "noext", ContentType: "text/csv; charset=utf-8"}); got != FormatCSV {
		t.Errorf("text/csv = %v,期望 CSV", got)
	}
	// ContentType 空则嗅探(用 PNG 魔数:DetectContentType 可靠判为 image/png)
	pngBytes := encodeTestPNG()
	s := &Source{Name: "noext", Reader: io.NopCloser(bytes.NewReader(pngBytes))}
	if got := DetectFormat(s); got != FormatImage {
		t.Errorf("嗅探 PNG = %v,期望 FormatImage(嗅探得 image/png)", got)
	}
	// 扩展名优先于 ContentType(同文件扩展名胜出)
	if got := DetectFormat(&Source{Name: "x.txt", ContentType: "image/png"}); got != FormatText {
		t.Errorf("x.txt + image/png = %v,期望 Text(扩展名优先)", got)
	}
	// 全部未知 -> Binary
	if got := DetectFormat(&Source{Name: "noext", ContentType: "application/octet-stream"}); got != FormatBinary {
		t.Errorf("未知 = %v,期望 Binary", got)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{5 * 1024 * 1024, "5.0 MiB"},
		{2 << 30, "2.0 GiB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q,期望 %q", c.in, got, c.want)
		}
	}
}

func TestAnsiFG(t *testing.T) {
	if got := string(ansiFG(1, 2, 3)); got != "\x1b[38;2;1;2;3m" {
		t.Errorf("ansiFG = %q", got)
	}
	if got := string(ansiBG(10, 20, 30)); got != "\x1b[48;2;10;20;30m" {
		t.Errorf("ansiBG = %q", got)
	}
}

func TestRGBA(t *testing.T) {
	img := newTestRGBA(1, 1, 0xAB, 0xCD, 0xEF)
	r, g, b := rgba(img, 0, 0)
	if r != 0xAB || g != 0xCD || b != 0xEF {
		t.Errorf("rgba = (%d,%d,%d),期望 (171,205,239)", r, g, b)
	}
}

func TestSniffContentType(t *testing.T) {
	// 已有 ContentType:短路不读 Reader
	body := "hello"
	s := &Source{Reader: io.NopCloser(strings.NewReader(body)), ContentType: "text/plain"}
	if err := SniffContentType(s); err != nil {
		t.Fatalf("已声明 ContentType 短路仍报错: %v", err)
	}
	if s.ContentType != "text/plain" {
		t.Errorf("ContentType 被改写成 %q", s.ContentType)
	}

	// 嗅探后数据不丢失(关键不变量)
	payload := `<html><body>x</body></html>`
	s = &Source{Reader: io.NopCloser(strings.NewReader(payload))}
	if err := SniffContentType(s); err != nil {
		t.Fatalf("SniffContentType 报错: %v", err)
	}
	if s.ContentType != "text/html; charset=utf-8" {
		t.Errorf("嗅探 ContentType = %q,期望 text/html", s.ContentType)
	}
	all, err := io.ReadAll(s.Reader)
	if err != nil {
		t.Fatalf("读取拼接 Reader 报错: %v", err)
	}
	if string(all) != payload {
		t.Errorf("嗅探后数据丢失: got %d 字节,期望 %d", len(all), len(payload))
	}
}

func TestReadBounded(t *testing.T) {
	// 未超限通过
	s := &Source{Reader: io.NopCloser(strings.NewReader("12345")), Size: 5}
	b, err := readBounded(s, 10, false)
	if err != nil || string(b) != "12345" {
		t.Errorf("未超限应通过,got %q err=%v", b, err)
	}

	// Size 已知且超限:提前报错
	s = &Source{Reader: io.NopCloser(strings.NewReader(strings.Repeat("a", 100))), Size: 100}
	if _, err := readBounded(s, 10, false); err == nil {
		t.Errorf("Size 超限应报错")
	}

	// force 放行超限
	s = &Source{Reader: io.NopCloser(strings.NewReader(strings.Repeat("a", 100))), Size: 100}
	if _, err := readBounded(s, 10, true); err != nil {
		t.Errorf("force 应放行超限,报错: %v", err)
	}
}

func TestOpenLocalRejectsDir(t *testing.T) {
	dir := t.TempDir()
	if _, err := openLocal(dir); err == nil {
		t.Errorf("目录应被拒绝")
	}
}

func TestOpenLocalRangeWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.txt")
	content := "0123456789" // 10 字节
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写文件: %v", err)
	}

	// bytes=-4:尾部 4 字节,Size 为窗口长度
	s, err := openLocalRange(path, "bytes=-4")
	if err != nil {
		t.Fatalf("openLocalRange 报错: %v", err)
	}
	defer s.Close()
	if s.Size != 4 {
		t.Errorf("窗口 Size = %d,期望 4", s.Size)
	}
	b, err := io.ReadAll(s.Reader)
	if err != nil {
		t.Fatalf("读取报错: %v", err)
	}
	if string(b) != "6789" {
		t.Errorf("窗口内容 = %q,期望 6789", string(b))
	}

	// bytes=-100:超过文件大小,截断为全量
	s2, err := openLocalRange(path, "bytes=-100")
	if err != nil {
		t.Fatalf("openLocalRange 报错: %v", err)
	}
	defer s2.Close()
	if s2.Size != 10 {
		t.Errorf("超大窗口 Size = %d,期望 10(全量)", s2.Size)
	}
}

func TestOpenLocalRangeInvalidRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.txt")
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	if _, err := openLocalRange(path, "bytes=0-5"); err == nil {
		t.Errorf("非后缀 Range 应报错")
	}
}

func TestOpenLocal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	s, err := openLocal(path)
	if err != nil {
		t.Fatalf("openLocal 报错: %v", err)
	}
	defer s.Close()
	if s.Size != 5 || s.IsS3 {
		t.Errorf("openLocal 元信息错误: Size=%d IsS3=%v", s.Size, s.IsS3)
	}
	if s.Name != "hello.txt" {
		t.Errorf("Name = %q,期望 hello.txt", s.Name)
	}
}

// newTestRGBA 构造一个像素可预期的 image.RGBA 便于 rgba() 断言。
func newTestRGBA(w, h, r, g, b int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*img.Stride + x*4)
			img.Pix[i] = uint8(r)
			img.Pix[i+1] = uint8(g)
			img.Pix[i+2] = uint8(b)
			img.Pix[i+3] = 255
		}
	}
	return img
}

// encodeTestPNG 编码一张 1x1 PNG 字节流,供嗅探路径构造 Source。
func encodeTestPNG() []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, newTestRGBA(1, 1, 1, 2, 3))
	return buf.Bytes()
}
