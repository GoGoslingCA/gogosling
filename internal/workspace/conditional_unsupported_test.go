//go:build !linux && !darwin

package workspace

import (
	"context"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func Test未支持平台明确拒绝条件修改(t *testing.T) {
	_, w := openTestWorkspace(t)
	ctx := context.Background()
	_, err := w.Snapshot(ctx, "a.txt", ConditionalTextLimit)
	assertCode(t, err, model.CodeUnavailable)
	expected := Snapshot{Identity: "unavailable", RootIdentity: "unavailable"}
	next := "不可写入"
	_, err = w.Apply(ctx, "operation", "a.txt", expected, &next)
	assertCode(t, err, model.CodeUnavailable)
	_, err = w.Recover(ctx, "operation", "a.txt", expected, &next)
	assertCode(t, err, model.CodeUnavailable)
	assertCode(t, w.Finalize(ctx, "operation", "a.txt"), model.CodeUnavailable)
	if exists, err := w.Exists(ctx, "a.txt"); err != nil || exists {
		t.Fatalf("不支持的平台不应偷偷写入: exists=%v err=%v", exists, err)
	}
}
