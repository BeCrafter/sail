package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/BeCrafter/sail/internal/fakes3"
)

// 进程内端到端:内存 S3(fakes3)+ 临时配置,跑真实的命令链路
// (rootCmd.Execute → cp/sync → uploader → S3),断言落库的 Content-Type。
// 与 scripts/e2e.sh 的同类断言互补:这里不需要凭证,CI 能拦住回归。

const ctE2EBucket = "e2e-bucket"

// ctE2E 搭好后端与配置,返回内存 S3;包级 flag 变量在每个用例结束时复位。
func ctE2E(t *testing.T) *fakes3.Server {
	t.Helper()
	srv := fakes3.New()
	t.Cleanup(srv.Close)

	cfgFile := filepath.Join(t.TempDir(), "config.yaml")
	cfg := fmt.Sprintf(`default-profile: e2e
profiles:
  e2e:
    endpoint: %s
    access-key: ak
    secret-key: sk
    region: us-east-1
    bucket: %s
    path-style: true
`, srv.URL(), ctE2EBucket)
	if err := os.WriteFile(cfgFile, []byte(cfg), 0o600); err != nil {
		t.Fatalf("写临时配置失败: %v", err)
	}

	resetCmdFlags() // 先清一次,保证从干净状态开始
	cfgPath = cfgFile
	t.Cleanup(resetCmdFlags)
	return srv
}

// resetCmdFlags 清掉测试会碰到的包级 flag 变量(范式同 sync_test.go)。
func resetCmdFlags() {
	cfgPath, profile, cfgBucket, cfgEndpoint = "", "", "", ""
	cpContentType, cpRecursive, cpDryRun = "", false, false
	syncContentType = ""
}

// sailRun 执行一次真实命令;每次执行前清 flag 变量,避免用例间串味。
// cobra 执行时会往根命令注入 completion/__complete/help 子命令,这里在执行后
// 移除新增项,保持全局命令树不变(否则会撞上 TestEveryTopLevelCommandHasGroup
// 这类遍历命令树的用例)。
func sailRun(t *testing.T, args ...string) {
	t.Helper()
	cfg, keep := cfgPath, profile
	resetCmdFlags()
	cfgPath, profile = cfg, keep

	before := rootCmd.Commands()
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	rootCmd.SetArgs(nil)
	for _, c := range rootCmd.Commands() {
		if !slices.Contains(before, c) {
			rootCmd.RemoveCommand(c)
		}
	}
	if err != nil {
		t.Fatalf("sail %v 失败: %v", args, err)
	}
}

func assertCT(t *testing.T, srv *fakes3.Server, key, want string) {
	t.Helper()
	obj, ok := srv.Get(ctE2EBucket, key)
	if !ok {
		t.Fatalf("%s 未落库", key)
	}
	if obj.ContentType != want {
		t.Errorf("%s 的 Content-Type = %q,期望 %q", key, obj.ContentType, want)
	}
}

func TestE2EContentType(t *testing.T) {
	srv := ctE2E(t)
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const png = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01"
	md := write("note.md", "# 标题\n")
	js := write("data.json", `{"a":1}`)

	t.Run("目标 key 扩展名命中", func(t *testing.T) {
		sailRun(t, "cp", md, "s3://"+ctE2EBucket+"/ct/note.md")
		assertCT(t, srv, "ct/note.md", "text/markdown; charset=utf-8")
	})

	t.Run("目标 key 优先于本地文件名", func(t *testing.T) {
		sailRun(t, "cp", js, "s3://"+ctE2EBucket+"/ct/renamed.md")
		assertCT(t, srv, "ct/renamed.md", "text/markdown; charset=utf-8")
	})

	t.Run("key 无扩展名回退本地文件名", func(t *testing.T) {
		sailRun(t, "cp", md, "s3://"+ctE2EBucket+"/ct/renamed")
		assertCT(t, srv, "ct/renamed", "text/markdown; charset=utf-8")
	})

	t.Run("--content-type 覆盖自动判定", func(t *testing.T) {
		sailRun(t, "cp", "--content-type", "text/x-pinned", md, "s3://"+ctE2EBucket+"/ct/pinned.md")
		assertCT(t, srv, "ct/pinned.md", "text/x-pinned")
	})

	t.Run("管道上传无扩展名 key 按内容探测", func(t *testing.T) {
		old := os.Stdin
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			w.WriteString(png)
			w.Close()
		}()
		os.Stdin = r
		defer func() { os.Stdin = old }()
		sailRun(t, "cp", "-", "s3://"+ctE2EBucket+"/ct/piped-png")
		assertCT(t, srv, "ct/piped-png", "image/png")
	})

	t.Run("边界:空文件兜底 octet-stream", func(t *testing.T) {
		blank := write("blank.zzz", "")
		sailRun(t, "cp", blank, "s3://"+ctE2EBucket+"/ct/blank.zzz")
		assertCT(t, srv, "ct/blank.zzz", "application/octet-stream")
	})

	t.Run("边界:空文件仍按扩展名判定", func(t *testing.T) {
		blank := write("blank.md", "")
		sailRun(t, "cp", blank, "s3://"+ctE2EBucket+"/ct/blank.md")
		assertCT(t, srv, "ct/blank.md", "text/markdown; charset=utf-8")
	})

	t.Run("s3→s3 复制保留源类型", func(t *testing.T) {
		sailRun(t, "cp", "s3://"+ctE2EBucket+"/ct/note.md", "s3://"+ctE2EBucket+"/ct/copied.bin")
		assertCT(t, srv, "ct/copied.bin", "text/markdown; charset=utf-8")
	})

	t.Run("sync 逐文件判定", func(t *testing.T) {
		src := filepath.Join(dir, "ctsync")
		if err := os.MkdirAll(src, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string]string{"doc.md": "# doc\n", "data.json": `{"a":1}`} {
			if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		sailRun(t, "sync", src, "s3://"+ctE2EBucket+"/ct/sync/")
		assertCT(t, srv, "ct/sync/doc.md", "text/markdown; charset=utf-8")
		assertCT(t, srv, "ct/sync/data.json", "application/json")
	})

	t.Run("sync --content-type 整批覆盖", func(t *testing.T) {
		src := filepath.Join(dir, "ctsync2")
		if err := os.MkdirAll(src, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, "a.md"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		sailRun(t, "sync", "--content-type", "text/x-dir", src, "s3://"+ctE2EBucket+"/ct/sync2/")
		assertCT(t, srv, "ct/sync2/a.md", "text/x-dir")
	})
}
