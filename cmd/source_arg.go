package cmd

import (
	"context"
	"strings"

	"github.com/BeCrafter/sail/internal/client"
	"github.com/BeCrafter/sail/internal/view"
)

// openSourceArg 打开 s3:// 或本地数据源(head/tail/wc/grep/checksum 共用)。
// s3:// 路径需要加载配置并构造客户端;本地路径无需配置。
func openSourceArg(ctx context.Context, arg string) (*view.Source, error) {
	if !strings.HasPrefix(arg, "s3://") {
		return view.OpenSource(ctx, arg, nil, "")
	}
	r, _, err := loadResolved()
	if err != nil {
		return nil, err
	}
	s3c, err := client.New(ctx, r)
	if err != nil {
		return nil, err
	}
	return view.OpenSource(ctx, arg, s3c, r.Bucket)
}
