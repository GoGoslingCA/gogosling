//go:build windows

package extension

import "os"

// Windows 的 Root 拒绝保留设备路径与目录外的管道路径。
func openSkillResource(root *os.Root, rel string) (*os.File, error) { return root.Open(rel) }
