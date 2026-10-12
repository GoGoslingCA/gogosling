package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/version"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestVersionFlagPrintsVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"-version"}, &out, &errOut, envOf(nil))
	if code != exitOK || !strings.Contains(out.String(), version.Version) {
		t.Fatalf("-version 应打印版本并返回 0，得到 %d / %q / %q", code, out.String(), errOut.String())
	}
}

func TestBadFlagsAreUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"-nope"}, {"-log-level", "loud"}, {"extra"}} {
		var out, errOut bytes.Buffer
		if code := run(context.Background(), args, &out, &errOut, envOf(map[string]string{"LANG": "zh_CN.UTF-8"})); code != exitUsage {
			t.Errorf("参数 %v 应返回 %d，得到 %d（%s）", args, exitUsage, code, errOut.String())
		}
	}
	var out, errOut bytes.Buffer
	run(context.Background(), []string{"-log-level", "loud"}, &out, &errOut, envOf(map[string]string{"LANG": "zh_CN.UTF-8"}))
	if !strings.Contains(errOut.String(), "无效的日志级别") {
		t.Fatalf("错误提示应按 LANG 使用中文，得到 %q", errOut.String())
	}
	errOut.Reset()
	run(context.Background(), []string{"-log-level", "loud"}, &out, &errOut, envOf(map[string]string{"LANG": "en_US.UTF-8"}))
	if !strings.Contains(errOut.String(), "invalid log level") {
		t.Fatalf("错误提示应按 LANG 使用英文，得到 %q", errOut.String())
	}
}

func TestNonLoopbackListenIsRejected(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
		file string
	}{
		{name: "命令行", args: []string{"-listen", "0.0.0.0:0"}},
		{name: "环境变量", env: map[string]string{config.EnvListen: "192.168.1.2:46750"}},
		{name: "config.json", file: `{"listen":"0.0.0.0:46750"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			home := t.TempDir()
			if c.file != "" {
				if err := os.WriteFile(filepath.Join(home, config.FileName), []byte(c.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			code := run(context.Background(), append([]string{"-home", home}, c.args...), &out, &errOut, envOf(c.env))
			if code != exitUsage {
				t.Fatalf("非回环监听地址应作为参数错误拒绝，得到 %d（%s）", code, errOut.String())
			}
			// LAY-12：在 config.Load 阶段就拒绝，不会先建立日志目录、打开数据库或启动任何模块。
			if _, err := os.Stat(filepath.Join(home, "logs")); !os.IsNotExist(err) {
				t.Fatalf("参数错误时不应初始化数据目录（logs 已存在: %v）", err)
			}
		})
	}
}

func TestLoadConfigPrecedence(t *testing.T) {
	envHome := t.TempDir()
	flagHome := t.TempDir()
	env := envOf(map[string]string{config.EnvHome: envHome, config.EnvListen: "127.0.0.1:1111"})

	cfg, err := loadConfig(flags{}, env)
	if err != nil || cfg.DataDir != envHome || cfg.ListenAddr != "127.0.0.1:1111" {
		t.Fatalf("应使用环境变量，得到 %+v / %v", cfg, err)
	}
	cfg, err = loadConfig(flags{home: flagHome, listen: "127.0.0.1:2222"}, env)
	if err != nil || cfg.DataDir != flagHome || cfg.ListenAddr != "127.0.0.1:2222" {
		t.Fatalf("-home / -listen 应覆盖环境变量，得到 %+v / %v", cfg, err)
	}
	cfg, err = loadConfig(flags{home: "relative/dir"}, envOf(nil))
	if err != nil || !filepath.IsAbs(cfg.DataDir) || cfg.ListenAddr != config.DefaultListenAddr {
		t.Fatalf("相对路径应转为绝对路径并使用默认监听地址，得到 %+v / %v", cfg, err)
	}
	// 数据目录下的 config.json 低于环境变量与命令行。
	if err := os.WriteFile(filepath.Join(flagHome, config.FileName), []byte(`{"listen":"127.0.0.1:3333"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadConfig(flags{home: flagHome}, envOf(nil))
	if err != nil || cfg.ListenAddr != "127.0.0.1:3333" {
		t.Fatalf("应读取数据目录下的 config.json，得到 %+v / %v", cfg, err)
	}
	cfg, err = loadConfig(flags{home: flagHome}, envOf(map[string]string{config.EnvListen: "127.0.0.1:4444"}))
	if err != nil || cfg.ListenAddr != "127.0.0.1:4444" {
		t.Fatalf("环境变量应覆盖 config.json，得到 %+v / %v", cfg, err)
	}
	cfg, err = loadConfig(flags{home: flagHome, listen: "127.0.0.2:5555"}, envOf(map[string]string{config.EnvListen: "127.0.0.1:4444"}))
	if err != nil || cfg.ListenAddr != "127.0.0.2:5555" {
		t.Fatalf("命令行应覆盖环境变量（127.0.0.0/8 均可），得到 %+v / %v", cfg, err)
	}
}

func TestCLILocaleFromEnv(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"LANG": "zh_CN.UTF-8"}, "zh"},
		{map[string]string{"LC_ALL": "en_US.UTF-8", "LANG": "zh_CN.UTF-8"}, "en"},
		{map[string]string{"LANG": "C"}, "en"},
		{nil, "en"},
	}
	for _, c := range cases {
		if got := cliLocale(envOf(c.env)); got != c.want {
			t.Errorf("环境 %v → %s，期望 %s", c.env, got, c.want)
		}
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"debug": slog.LevelDebug, "INFO": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError} {
		if got, err := parseLevel(in); err != nil || got != want {
			t.Errorf("parseLevel(%q) = %v / %v，期望 %v", in, got, err, want)
		}
	}
	if _, err := parseLevel("verbose"); err == nil {
		t.Error("未知级别应报错")
	}
}

func TestReportRunErrorMapsConflictToExitCode(t *testing.T) {
	var errOut bytes.Buffer
	conflict := &model.Error{Code: model.CodeConflict, Message: "x", Details: map[string]any{"pid": 4242, "data_dir": "/d"}}
	code := reportRunError(&errOut, "zh", "/d", errors.Join(errors.New("wrap"), conflict), slog.New(slog.DiscardHandler))
	if code != exitConflict || !strings.Contains(errOut.String(), "4242") || !strings.Contains(errOut.String(), "/d") {
		t.Fatalf("锁冲突应返回 %d 并提示 pid，得到 %d / %q", exitConflict, code, errOut.String())
	}
	errOut.Reset()
	if code := reportRunError(&errOut, "en", "/d", errors.New("boom"), slog.New(slog.DiscardHandler)); code != exitFailure {
		t.Fatalf("其他错误应返回 %d，得到 %d", exitFailure, code)
	}
}
