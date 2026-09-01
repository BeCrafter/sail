// Package version 提供 sail 的版本号。
//
// 版本号由构建流程在链接期注入,覆盖默认值:
//
//	go build -ldflags "-X github.com/BeCrafter/sail/internal/version.Version=v1.2.3"
//
// 未注入时(本地 go build / go run)为 "dev"。
package version

// Version 是 sail 的版本号。
var Version = "dev"
