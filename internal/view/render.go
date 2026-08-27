package view

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"go.yaml.in/yaml/v3"
)

// Format 表示渲染目标的格式。
type Format int

const (
	FormatAuto Format = iota // 按扩展名→ContentType→嗅探→Binary 自动判定
	FormatText
	FormatJSON
	FormatYAML
	FormatCSV
	FormatXML
	FormatImage
	FormatBinary
)

// Options 控制渲染行为。
type Options struct {
	Force         bool  // 跳过大小限制
	MaxImageBytes int64 // 图片最大字节数,超出提示 --force
	Width         int   // 字符画列宽,0=自动探测
}

// Render 按 Format 分发到具体渲染器,输出到 stdout。
func Render(s *Source, f Format, opts *Options) error {
	if opts == nil {
		opts = &Options{}
	}
	if f == FormatAuto {
		f = DetectFormat(s)
	}
	switch f {
	case FormatText:
		return renderText(s)
	case FormatJSON:
		return renderJSON(s, opts)
	case FormatYAML:
		return renderYAML(s, opts)
	case FormatCSV:
		return renderCSV(s)
	case FormatXML:
		return renderXML(s, opts)
	case FormatImage:
		return renderImage(s, opts)
	default:
		return renderBinary(s)
	}
}

// ParseFormat 将 --as flag 的值解析为 Format。
func ParseFormat(name string) (Format, bool) {
	switch strings.ToLower(name) {
	case "", "auto":
		return FormatAuto, true
	case "text", "txt":
		return FormatText, true
	case "json":
		return FormatJSON, true
	case "yaml", "yml":
		return FormatYAML, true
	case "csv", "tsv":
		return FormatCSV, true
	case "xml", "svg":
		return FormatXML, true
	case "image", "img":
		return FormatImage, true
	case "binary", "bin":
		return FormatBinary, true
	}
	return 0, false
}

// DetectFormat 按优先级判定格式:扩展名 > ContentType(可能触发嗅探)> Binary。
func DetectFormat(s *Source) Format {
	if ext := strings.ToLower(filepath.Ext(s.Name)); ext != "" {
		if f, ok := extFormat(ext); ok {
			return f
		}
	}
	if s.ContentType == "" {
		_ = SniffContentType(s) // best effort
	}
	if f, ok := mimeFormat(s.ContentType); ok {
		return f
	}
	return FormatBinary
}

var extMap = map[string]Format{
	".txt": FormatText, ".log": FormatText, ".md": FormatText, ".markdown": FormatText,
	".conf": FormatText, ".ini": FormatText, ".properties": FormatText,
	".go": FormatText, ".py": FormatText, ".js": FormatText, ".ts": FormatText, ".jsx": FormatText, ".tsx": FormatText,
	".java": FormatText, ".c": FormatText, ".cpp": FormatText, ".h": FormatText, ".hpp": FormatText,
	".rs": FormatText, ".rb": FormatText, ".php": FormatText, ".sh": FormatText, ".bash": FormatText, ".zsh": FormatText,
	".toml": FormatText, ".sql": FormatText, ".html": FormatText, ".htm": FormatText,
	".css": FormatText, ".scss": FormatText, ".proto": FormatText, ".diff": FormatText, ".patch": FormatText,
	".json": FormatJSON, ".jsonl": FormatText, ".geojson": FormatJSON,
	".yaml": FormatYAML, ".yml": FormatYAML,
	".csv": FormatCSV, ".tsv": FormatCSV,
	".xml": FormatXML, ".svg": FormatXML, ".xsl": FormatXML, ".xslt": FormatXML, ".plist": FormatXML,
	".png": FormatImage, ".jpg": FormatImage, ".jpeg": FormatImage, ".gif": FormatImage, ".bmp": FormatImage, ".webp": FormatImage,
}

func extFormat(e string) (Format, bool) {
	f, ok := extMap[e]
	return f, ok
}

func mimeFormat(ct string) (Format, bool) {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch {
	case strings.HasPrefix(ct, "image/"):
		return FormatImage, true
	case ct == "text/csv", strings.HasPrefix(ct, "text/tab-separated-values"):
		return FormatCSV, true
	case ct == "application/json", ct == "text/json":
		return FormatJSON, true
	case ct == "application/yaml", ct == "text/yaml", ct == "application/x-yaml":
		return FormatYAML, true
	case strings.HasPrefix(ct, "application/xml"), strings.HasPrefix(ct, "text/xml"), ct == "application/svg+xml":
		return FormatXML, true
	case strings.HasPrefix(ct, "text/"):
		return FormatText, true
	}
	return 0, false
}

// readBounded 读取受 max 限制的全部内容;force 时无限制。Size 已知且超限则先报错。
func readBounded(s *Source, max int64, force bool) ([]byte, error) {
	if !force && s.Size > 0 && s.Size > max {
		return nil, fmt.Errorf("文件过大: %s 超过 %s 限制,使用 --force 跳过", humanBytes(s.Size), humanBytes(max))
	}
	var r io.Reader = s.Reader
	if !force {
		r = io.LimitReader(s.Reader, max+1)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("读取失败: %w", err)
	}
	if !force && int64(len(b)) > max {
		return nil, fmt.Errorf("文件过大: %s 超过 %s 限制,使用 --force 跳过", humanBytes(int64(len(b))), humanBytes(max))
	}
	return b, nil
}

func renderText(s *Source) error {
	scanner := bufio.NewScanner(s.Reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // 单行最大 4 MiB
	for scanner.Scan() {
		fmt.Println(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取失败: %w", err)
	}
	return nil
}

func renderJSON(s *Source, opts *Options) error {
	b, err := readBounded(s, 50<<20, opts.Force)
	if err != nil {
		return err
	}
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf("解析 JSON 失败: %w", err)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("格式化 JSON 失败: %w", err)
	}
	fmt.Println(string(out))
	return nil
}

func renderYAML(s *Source, opts *Options) error {
	b, err := readBounded(s, 50<<20, opts.Force)
	if err != nil {
		return err
	}
	var v interface{}
	if err := yaml.Unmarshal(b, &v); err != nil {
		return fmt.Errorf("解析 YAML 失败: %w", err)
	}
	out, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("格式化 YAML 失败: %w", err)
	}
	fmt.Print(string(out))
	return nil
}

func renderXML(s *Source, opts *Options) error {
	b, err := readBounded(s, 50<<20, opts.Force)
	if err != nil {
		return err
	}
	return prettyXML(b)
}

// prettyXML 基于 token 流重新缩进,容忍格式不规范的 XML/SVG。
func prettyXML(b []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("解析 XML 失败: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			fmt.Printf("%s<%s", indent(depth), t.Name.Local)
			for _, a := range t.Attr {
				fmt.Printf(" %s=%q", a.Name.Local, a.Value)
			}
			fmt.Println(">")
			depth++
		case xml.EndElement:
			depth--
			if depth < 0 {
				depth = 0
			}
			fmt.Printf("%s</%s>\n", indent(depth), t.Name.Local)
		case xml.CharData:
			if text := strings.TrimSpace(string(t)); text != "" {
				fmt.Printf("%s%s\n", indent(depth), text)
			}
		case xml.Comment:
			fmt.Printf("%s<!--%s-->\n", indent(depth), string(t))
		case xml.ProcInst:
			fmt.Printf("%s<?%s %s?>\n", indent(depth), t.Target, string(t.Inst))
		case xml.Directive:
			fmt.Printf("%s<!%s>\n", indent(depth), string(t))
		}
	}
	return nil
}

func indent(depth int) string { return strings.Repeat("  ", depth) }

func renderCSV(s *Source) error {
	r := csv.NewReader(s.Reader)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1 // 允许字段数不一致
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("解析 CSV 失败: %w", err)
		}
		fmt.Fprintln(w, strings.Join(rec, "\t"))
	}
	return w.Flush()
}

func renderBinary(s *Source) error {
	head := make([]byte, 256)
	n, err := io.ReadFull(s.Reader, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return fmt.Errorf("读取失败: %w", err)
	}
	head = head[:n]

	fmt.Printf("name: %s\n", s.Name)
	if s.Size >= 0 {
		fmt.Printf("size: %s\n", humanBytes(s.Size))
	} else {
		fmt.Println("size: 未知")
	}
	if s.ContentType != "" {
		fmt.Printf("type: %s\n", s.ContentType)
	}
	fmt.Println("--- hex dump (前 256 字节) ---")
	for i := 0; i < len(head); i += 16 {
		chunk := head[i:min(i+16, len(head))]
		hexParts := make([]string, 0, 16)
		for _, c := range chunk {
			hexParts = append(hexParts, fmt.Sprintf("%02x", c))
		}
		var ascii strings.Builder
		for _, c := range chunk {
			if c >= 32 && c < 127 {
				ascii.WriteByte(c)
			} else {
				ascii.WriteByte('.')
			}
		}
		fmt.Printf("%08x  %-48s  %s\n", i, strings.Join(hexParts, " "), ascii.String())
	}
	return nil
}

func humanBytes(b int64) string {
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
