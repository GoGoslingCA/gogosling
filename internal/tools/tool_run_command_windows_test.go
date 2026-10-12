//go:build windows

package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/sandbox"
)

// XP-02：带引号（含空格路径）的命令在 Windows 上原样交给 cmd.exe。
func TestRunCommandQuotedPathOnWindows(t *testing.T) {
	ws := newFakeWorkspace(t)
	rc := testRunContext(t, ws, nil)
	if err := os.MkdirAll(filepath.Join(ws.Root(), "Program Files"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, ws, "Program Files/notes.txt", "TODO: ship it\r\n")
	tool := NewRunCommand(sandbox.NewDirect(nil))

	for _, cmd := range []string{
		`findstr "TODO" "Program Files\notes.txt"`,
		`dir /B "Program Files"`,
		`type "Program Files\notes.txt" | findstr /C:"ship it"`,
	} {
		res, err := tool.Execute(context.Background(), rc, rawArgs(t, map[string]any{"command": cmd}))
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError || !strings.Contains(res.Content, "退出码: 0") {
			t.Fatalf("%s 应成功: %s", cmd, res.Content)
		}
	}
}
