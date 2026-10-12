package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		in      string
		escapes bool
		want    []string
		wantErr bool
	}{
		{"uv run --project worker gogosling-worker", true, []string{"uv", "run", "--project", "worker", "gogosling-worker"}, false},
		{`"/opt/my app/python" -m gogosling_worker`, true, []string{"/opt/my app/python", "-m", "gogosling_worker"}, false},
		{`'a b' c\ d`, true, []string{"a b", "c d"}, false},
		{`"say \"hi\"" x`, true, []string{`say "hi"`, "x"}, false},
		{"  spaced   out\t args ", true, []string{"spaced", "out", "args"}, false},
		{`C:\py\python.exe -m x`, false, []string{`C:\py\python.exe`, "-m", "x"}, false},
		{`"C:\Program Files\py\python.exe" -m x`, false, []string{`C:\Program Files\py\python.exe`, "-m", "x"}, false},
		{`"unterminated`, true, nil, true},
		{`trailing\`, true, nil, true},
		{"", true, nil, false},
		// 双引号内：只有 $ ` " \ 与换行前的反斜杠是转义，其余反斜杠原样保留（与 sh 一致）。
		{`python "/opt/a\b/run.py" --x`, true, []string{"python", `/opt/a\b/run.py`, "--x"}, false},
		{`"a\\b" "c\$d" "e\` + "`" + `f" "g\nh"`, true, []string{`a\b`, "c$d", "e`f", `g\nh`}, false},
		{"\"line\\\ncont\"", true, []string{"linecont"}, false},
		{`"ends with \`, true, nil, true},
		// 单引号内反斜杠是普通字符；引号外反斜杠转义任意字符，反斜杠-换行是续行。
		{`'a\b' a\b`, true, []string{`a\b`, "ab"}, false},
		{"a\\\nb c", true, []string{"ab", "c"}, false},
		{"x \\\n y", true, []string{"x", "y"}, false},
		// 相邻的引号段拼成一个参数；空引号产生空参数。
		{`a"b c"'d e'f`, true, []string{"ab cd ef"}, false},
		{`"" x ''`, true, []string{"", "x", ""}, false},
		// Windows：反斜杠一律是普通字符。
		{`"C:\dir\" x`, false, []string{`C:\dir\`, "x"}, false},
	}
	for _, c := range cases {
		got, err := splitCommand(c.in, c.escapes)
		if c.wantErr {
			if err == nil {
				t.Errorf("splitCommand(%q) 应报错", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitCommand(%q) 失败: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitCommand(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestLocateUsesEnvOverride(t *testing.T) {
	t.Setenv(EnvWorkerCmd, "  python3 -m gogosling_worker ")
	cmd, err := Locate()
	if err != nil {
		t.Fatalf("Locate 失败: %v", err)
	}
	if cmd.Path != "python3" || !reflect.DeepEqual(cmd.Args, []string{"-m", "gogosling_worker"}) {
		t.Errorf("命令 = %+v，期望 python3 -m gogosling_worker", cmd)
	}
}

func TestLocateRejectsMalformedEnv(t *testing.T) {
	t.Setenv(EnvWorkerCmd, `"broken`)
	if _, err := Locate(); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("错误码 = %v，期望 invalid", err)
	}
}

func TestLocateWithoutBundleIsUnavailable(t *testing.T) {
	t.Setenv(EnvWorkerCmd, "")
	// 测试二进制旁边不会有随安装包分发的运行环境。
	if _, err := Locate(); !model.IsCode(err, model.CodeUnavailable) {
		t.Errorf("错误码 = %v，期望 unavailable", err)
	}
}

func TestLocateBundledLayouts(t *testing.T) {
	cases := []struct {
		goos   string
		exeDir string
		python string
	}{
		{"linux", "bin", filepath.Join("worker", "bin", "python")},
		{"darwin", filepath.Join("Contents", "MacOS"), filepath.Join("Contents", "Resources", "worker", "bin", "python")},
		{"darwin", "bin", filepath.Join("worker", "bin", "python")},
		{"windows", "bin", filepath.Join("worker", "Scripts", "python.exe")},
	}
	for _, c := range cases {
		root := t.TempDir()
		exeDir := filepath.Join(root, c.exeDir)
		python := filepath.Join(root, c.python)
		if err := os.MkdirAll(exeDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, ok := locateBundled(exeDir, c.goos); ok {
			t.Errorf("%s: 运行环境不存在时不应找到", c.goos)
		}
		if err := os.MkdirAll(filepath.Dir(python), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(python, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		cmd, ok := locateBundled(exeDir, c.goos)
		if !ok {
			t.Errorf("%s/%s: 应找到 %s", c.goos, c.exeDir, python)
			continue
		}
		if cmd.Path != filepath.Clean(python) || !reflect.DeepEqual(cmd.Args, []string{"-m", workerModule}) {
			t.Errorf("%s: 命令 = %+v", c.goos, cmd)
		}
	}
}
