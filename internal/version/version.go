// Package version 提供构建期注入的版本信息。
//
// 发布构建通过 -ldflags "-X" 覆盖下面的变量（见仓库根目录 Makefile）；
// 未注入时保持开发态默认值。
package version

import "fmt"

// Version 是语义化版本号，例如 "0.1.0"。
var Version = "0.0.0-dev"

// Commit 是构建所基于的 Git 提交哈希。
var Commit = "unknown"

// Date 是构建时间（RFC 3339，UTC）。
var Date = "unknown"

// String 返回适合在命令行与日志中展示的单行版本描述。
func String() string {
	return fmt.Sprintf("gogosling %s (commit %s, built %s)", Version, Commit, Date)
}
