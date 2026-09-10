package client

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// stubHTTPClient 实现 aws.HTTPClient,作为 xmlTimeNormalizer 的可控 base。
type stubHTTPClient struct {
	status int
	header http.Header
	body   string
	err    error
}

func (s *stubHTTPClient) Do(req *http.Request) (*http.Response, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &http.Response{
		StatusCode: s.status,
		Header:     s.header,
		Body:       io.NopCloser(strings.NewReader(s.body)),
	}, nil
}

func TestXMLTimeNormalizer(t *testing.T) {
	broken := `<Bucket><Name>b</Name><CreationDate>2022-08-12 16:15:54</CreationDate></Bucket>`
	fixed := `<Bucket><Name>b</Name><CreationDate>2022-08-12T16:15:54Z</CreationDate></Bucket>`

	t.Run("非 200 不改写", func(t *testing.T) {
		n := &xmlTimeNormalizer{base: &stubHTTPClient{status: 404, header: func() http.Header {
			h := http.Header{}
			h.Set("Content-Type", "application/xml")
			return h
		}(), body: broken}}
		r, err := n.Do(nil) // stub 忽略 req,直接返回配置的响应
		if err != nil {
			t.Fatalf("Do 报错: %v", err)
		}
		if r.StatusCode != 404 {
			t.Errorf("非 200 状态码被改动")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != broken {
			t.Errorf("非 200 响应体被改写")
		}
	})

	t.Run("非 XML Content-Type 不改写", func(t *testing.T) {
		n := &xmlTimeNormalizer{base: &stubHTTPClient{status: 200, header: func() http.Header {
			h := http.Header{}
			h.Set("Content-Type", "application/json")
			return h
		}(), body: broken}}
		r, err := n.Do(nil)
		if err != nil {
			t.Fatalf("Do 报错: %v", err)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != broken {
			t.Errorf("非 XML 响应体不应被改写")
		}
	})

	t.Run("XML 且 200 规范化时间并更新 ContentLength", func(t *testing.T) {
		n := &xmlTimeNormalizer{base: &stubHTTPClient{status: 200, header: func() http.Header {
			h := http.Header{}
			h.Set("Content-Type", "application/xml")
			return h
		}(), body: broken}}
		r, err := n.Do(nil)
		if err != nil {
			t.Fatalf("Do 报错: %v", err)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != fixed {
			t.Errorf("XML 时间未规范化:\n%s", string(body))
		}
		if r.ContentLength != int64(len(fixed)) {
			t.Errorf("ContentLength = %d,期望 %d", r.ContentLength, len(fixed))
		}
	})

	t.Run("空 Body 原样返回", func(t *testing.T) {
		n := &xmlTimeNormalizer{base: &stubHTTPClient{status: 200, header: func() http.Header {
			h := http.Header{}
			h.Set("Content-Type", "application/xml")
			return h
		}(), body: ""}}
		r, err := n.Do(nil)
		if err != nil {
			t.Fatalf("Do 报错: %v", err)
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) != 0 {
			t.Errorf("空 Body 被改动为 %q", string(body))
		}
	})

	t.Run("base 报错透传", func(t *testing.T) {
		baseErr := errors.New("boom")
		n := &xmlTimeNormalizer{base: &stubHTTPClient{err: baseErr}}
		if _, err := n.Do(nil); err != baseErr {
			t.Errorf("base 错误未透传,got %v", err)
		}
	})
}

func TestBrokenXMLTimeRe(t *testing.T) {
	got := brokenXMLTimeRe.ReplaceAllString("2022-08-12 16:15:54", "${1}T${2}Z")
	if got != "2022-08-12T16:15:54Z" {
		t.Errorf("正则替换 = %q,期望 2022-08-12T16:15:54Z", got)
	}
	// 不匹配已标准化的 ISO 时间
	iso := "2022-08-12T16:15:54Z"
	if out := brokenXMLTimeRe.ReplaceAllString(iso, "${1}T${2}Z"); out != iso {
		t.Errorf("已标准化的时间不应被误改: %q", out)
	}
}

// TestCoalesce 覆盖兜底选择逻辑(当前无调用点,保留以固化行为;若删除函数则删除本测试)。
func TestCoalesce(t *testing.T) {
	cases := []struct {
		a, b string
		want string
	}{
		{"x", "y", "x"},
		{"", "y", "y"},
		{"x", "", "x"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := coalesce(c.a, c.b); got != c.want {
			t.Errorf("coalesce(%q,%q) = %q,期望 %q", c.a, c.b, got, c.want)
		}
	}
}

var _ aws.HTTPClient = (*stubHTTPClient)(nil)
