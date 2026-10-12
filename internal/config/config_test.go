package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// envOf 把 map 包装成 getenv，测试不读取真实环境。
func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows 的 os.UserHomeDir

	cfg, err := Load(envOf(nil), "", "")
	if err != nil {
		t.Fatalf("Load 返回错误: %v", err)
	}
	if want := filepath.Join(home, DefaultDirName); cfg.DataDir != want {
		t.Errorf("没有任何覆盖时 DataDir = %q，期望 %q", cfg.DataDir, want)
	}
	if cfg.ListenAddr != DefaultListenAddr {
		t.Errorf("ListenAddr = %q，期望默认值 %q", cfg.ListenAddr, DefaultListenAddr)
	}
}

func TestDataDirPriorityAndTrimming(t *testing.T) {
	flag, env := t.TempDir(), t.TempDir()

	got, err := DataDir(envOf(map[string]string{EnvHome: env}), "  "+flag+"  ")
	if err != nil || got != flag {
		t.Errorf("命令行参数应优先于环境变量并去掉空白：得到 (%q, %v)，期望 %q", got, err, flag)
	}
	got, err = DataDir(envOf(map[string]string{EnvHome: " " + env + "\t"}), "   ")
	if err != nil || got != env {
		t.Errorf("空白的命令行参数应视为未提供，环境变量应去掉空白：得到 (%q, %v)，期望 %q", got, err, env)
	}

	// 相对路径转成绝对路径。
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err = DataDir(envOf(map[string]string{EnvHome: "rel-home"}), "")
	if err != nil || got != filepath.Join(wd, "rel-home") {
		t.Errorf("相对路径应按当前目录转为绝对路径：得到 (%q, %v)", got, err)
	}
}

func TestLoadListenPriority(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, FileName), `{"listen": " 127.0.0.2:1 "}`)
	env := map[string]string{EnvHome: home}

	cfg, err := Load(envOf(env), "", "")
	if err != nil || cfg.ListenAddr != "127.0.0.2:1" {
		t.Fatalf("config.json 应覆盖默认值：得到 (%+v, %v)", cfg, err)
	}
	env[EnvListen] = "127.0.0.3:2"
	cfg, err = Load(envOf(env), "", "")
	if err != nil || cfg.ListenAddr != "127.0.0.3:2" {
		t.Fatalf("环境变量应覆盖 config.json：得到 (%+v, %v)", cfg, err)
	}
	cfg, err = Load(envOf(env), "", "[::1]:0")
	if err != nil || cfg.ListenAddr != "[::1]:0" {
		t.Fatalf("命令行参数应覆盖环境变量：得到 (%+v, %v)", cfg, err)
	}
	env[EnvListen] = "   "
	cfg, err = Load(envOf(env), "", "")
	if err != nil || cfg.ListenAddr != "127.0.0.2:1" {
		t.Fatalf("空白的环境变量应视为未设置：得到 (%+v, %v)", cfg, err)
	}
}

func TestLoadRejectsNonLoopbackListen(t *testing.T) {
	home := t.TempDir()
	for name, tc := range map[string]struct {
		file, env, flag string
	}{
		"命令行参数":       {flag: "0.0.0.0:46750"},
		"环境变量":        {env: "192.168.1.10:46750"},
		"config.json": {file: `{"listen":":46750"}`},
		"缺少端口":        {flag: "127.0.0.1"},
	} {
		path := filepath.Join(home, FileName)
		_ = os.Remove(path)
		if tc.file != "" {
			writeFile(t, path, tc.file)
		}
		_, err := Load(envOf(map[string]string{EnvHome: home, EnvListen: tc.env}), "", tc.flag)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s 给出非回环地址时应返回 ErrInvalid，得到 %v", name, err)
		}
	}
}

func TestLoadConfigFileErrors(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, FileName)
	env := envOf(map[string]string{EnvHome: home})

	for name, body := range map[string]string{
		"语法错误":    `{"listen":`,
		"未知字段":    `{"listen":"127.0.0.1:1","lisen":"x"}`,
		"多个 JSON": `{} {}`,
		"类型错误":    `{"listen": 1}`,
	} {
		writeFile(t, path, body)
		if _, err := Load(env, "", ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("config.json %s 时应返回 ErrInvalid，得到 %v", name, err)
		}
	}

	writeFile(t, path, "  \n")
	if cfg, err := Load(env, "", ""); err != nil || cfg.ListenAddr != DefaultListenAddr {
		t.Errorf("空的 config.json 应视为没有配置：得到 (%+v, %v)", cfg, err)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		if os.Getuid() != 0 {
			_, err := Load(env, "", "")
			if err == nil || errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), FileName) {
				t.Errorf("config.json 不可读时应返回带路径的 I/O 错误（而不是 ErrInvalid），得到 %v", err)
			}
		}
	}
}

func TestLoadNilGetenvUsesProcessEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	t.Setenv(EnvListen, "localhost:0")
	cfg, err := Load(nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != home || cfg.ListenAddr != "localhost:0" {
		t.Errorf("getenv 为 nil 时应读取进程环境，得到 %+v", cfg)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
