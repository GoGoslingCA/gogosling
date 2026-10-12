//go:build !linux

package sandbox

import "github.com/GoGoslingCA/gogosling/internal/model"

func bwrapSocketFilter() ([]byte, error) {
	return nil, model.NewError(model.CodeUnavailable, bwrapText("socket_linux_only", nil))
}
