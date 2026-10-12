//go:build windows

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSysProcAttrHidesWindowAndKeepsRawCmdLine(t *testing.T) {
	a := sysProcAttr(`cmd.exe /d /s /c "echo "a b""`)
	if a.CreationFlags&createNoWindow == 0 || a.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatalf("子进程应带 CREATE_NO_WINDOW 与 CREATE_NEW_PROCESS_GROUP，得到 %#x", a.CreationFlags)
	}
	if a.CmdLine != `cmd.exe /d /s /c "echo "a b""` {
		t.Fatalf("原样命令行不应被改写，得到 %q", a.CmdLine)
	}
}

// 带引号的命令文本经 RawCommandLine 原样交给 cmd.exe：引号不会被转义成 \"。
func TestDirectRunRawCommandLineKeepsQuotes(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "with space")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "marker.txt"), []byte("TODO: quoted\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewDirect(nil)
	command := `findstr "TODO" "with space\marker.txt"`
	res, err := r.Run(context.Background(), Spec{
		Command:        []string{"cmd.exe"},
		RawCommandLine: `cmd.exe /d /s /c "` + command + `"`,
		Dir:            dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "TODO: quoted") {
		t.Fatalf("带引号路径的命令应成功，得到 %+v", res)
	}
}
