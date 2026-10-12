package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/server"
)

func TestLockConflictsWhileHeldAndTakesOverStaleFile(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireLock(dir)
	if err != nil {
		t.Fatalf("首次加锁失败: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, LockFileName))
	if err != nil || strings.TrimSpace(string(raw)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("锁文件应写入当前 pid，得到 %q / %v", raw, err)
	}

	_, err = acquireLock(dir)
	if !model.IsCode(err, model.CodeConflict) {
		t.Fatalf("锁被持有时应返回 conflict，得到 %v", err)
	}
	var me *model.Error
	if !asError(err, &me) || me.Details["pid"] != os.Getpid() || me.Details["data_dir"] != dir {
		t.Fatalf("冲突错误应带 pid 与 data_dir，得到 %+v", me)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("释放失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, LockFileName)); !os.IsNotExist(err) {
		t.Fatalf("释放后应删除锁文件，得到 %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("重复释放应无害: %v", err)
	}

	// 残留的锁文件（上一个进程崩溃，pid 早已不存在）不应阻止启动。
	if err := os.WriteFile(filepath.Join(dir, LockFileName), []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := acquireLock(dir)
	if err != nil {
		t.Fatalf("残留锁文件应被接管，得到 %v", err)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, LockFileName))
	if strings.TrimSpace(string(raw)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("接管后应写入当前 pid，得到 %q", raw)
	}
	_ = second.Release()
}

func asError(err error, target **model.Error) bool {
	for err != nil {
		if me, ok := err.(*model.Error); ok {
			*target = me
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestResourceLocatorPriority(t *testing.T) {
	root := t.TempDir()
	envDir := filepath.Join(root, "env-res")
	installRoot := filepath.Join(root, "install")
	repo := filepath.Join(root, "repo")
	for _, d := range []string{envDir, filepath.Join(installRoot, "bin"), filepath.Join(installRoot, "share", "gogosling", "extensions", "bundled"), filepath.Join(repo, "internal", "app"), filepath.Join(repo, "extensions", "bundled")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module "+modulePath+"\n\ngo 1.26.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	cases := []struct {
		name       string
		loc        resourceLocator
		wantDir    string
		wantSource string
	}{
		{"环境变量优先", resourceLocator{getenv: env(map[string]string{config.EnvResources: envDir}), exe: filepath.Join(installRoot, "bin", "gogoslingd"), cwd: repo}, envDir, resourcesFromEnv},
		{"环境变量指向不存在的目录则不再回退", resourceLocator{getenv: env(map[string]string{config.EnvResources: filepath.Join(root, "nope")}), exe: filepath.Join(installRoot, "bin", "gogoslingd")}, "", ""},
		{"安装布局", resourceLocator{getenv: env(nil), exe: filepath.Join(installRoot, "bin", "gogoslingd"), cwd: repo}, filepath.Join(installRoot, "share", "gogosling"), resourcesFromInstall},
		{"go-build 临时目录 → 仓库根", resourceLocator{getenv: env(nil), exe: filepath.Join(root, "go-build123", "b001", "app.test"), cwd: filepath.Join(repo, "internal", "app")}, repo, resourcesFromRepo},
		{"GOGOSLING_DEV → 仓库根", resourceLocator{getenv: env(map[string]string{config.EnvDev: "1"}), exe: filepath.Join(root, "bin", "gogoslingd"), cwd: repo}, repo, resourcesFromRepo},
		{"普通可执行文件不去找仓库", resourceLocator{getenv: env(nil), exe: filepath.Join(root, "bin", "gogoslingd"), cwd: repo}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, source := tc.loc.locate()
			if dir != tc.wantDir || source != tc.wantSource {
				t.Fatalf("locate() = %q/%q，期望 %q/%q", dir, source, tc.wantDir, tc.wantSource)
			}
		})
	}
	if got := bundledExtensionsDir(repo); got != filepath.Join(repo, "extensions", "bundled") {
		t.Fatalf("内置扩展目录错误: %q", got)
	}
	if bundledExtensionsDir(envDir) != "" || bundledExtensionsDir("") != "" {
		t.Fatal("没有 extensions/bundled 时应返回空串")
	}
	// 其他模块的 go.mod 不算本仓库。
	other := filepath.Join(root, "other")
	_ = os.MkdirAll(other, 0o700)
	_ = os.WriteFile(filepath.Join(other, "go.mod"), []byte("module example.com/x\n"), 0o600)
	if findModuleRoot(other) == other {
		t.Fatal("不应把别的模块当成仓库根")
	}
}

type mapSettings map[string]string

func (m mapSettings) GetSetting(_ context.Context, key string) (string, error) {
	if v, ok := m[key]; ok {
		return v, nil
	}
	return "", model.NotFound("setting", key)
}

func (m mapSettings) SetSetting(_ context.Context, key, value string) error {
	m[key] = value
	return nil
}

func TestLoadSettings(t *testing.T) {
	ctx := context.Background()
	noEnv := func(string) string { return "" }
	s, err := loadSettings(ctx, mapSettings{server.SettingTimezone: `"Asia/Shanghai"`, server.SettingLocale: `"zh"`},
		func(k string) string {
			if k == "LANG" {
				return "en_US.UTF-8"
			}
			return ""
		}, discardLogger())
	if err != nil || s.Timezone != "Asia/Shanghai" || s.EffectiveTimezone != "Asia/Shanghai" || s.Locale != "zh" {
		t.Fatalf("应解析 JSON 字符串设置且设置优先于环境，得到 %+v / %v", s, err)
	}
	s, _ = loadSettings(ctx, mapSettings{server.SettingTimezone: "Mars/Base", server.SettingLocale: "ja"}, func(k string) string {
		switch k {
		case "LANG":
			return "zh_CN.UTF-8"
		case "TZ":
			return "UTC"
		}
		return ""
	}, discardLogger())
	if s.Timezone != "" || s.EffectiveTimezone != "UTC" || s.Locale != "zh" {
		t.Fatalf("无效时区应忽略并取系统时区、不支持的语言应回落到 LANG，得到 %+v", s)
	}
	// "Local" 不是 IANA 名称（与 scheduler.LoadLocation 的规则一致）。
	s, _ = loadSettings(ctx, mapSettings{server.SettingTimezone: `"Local"`}, noEnv, discardLogger())
	if s.Timezone != "" {
		t.Fatalf("Local 不应被当作有效时区设置，得到 %+v", s)
	}
	s, _ = loadSettings(ctx, mapSettings{}, noEnv, discardLogger())
	if s.Timezone != "" || s.Locale != i18n.DefaultLocale {
		t.Fatalf("缺省时应使用默认语言，得到 %+v", s)
	}
}

// LAY-01 回归：全新数据目录、LANG=en_US.UTF-8 时，组合根交给内核模块的语言（app.loadSettings）
// 与 GET /settings 报告的 effective_locale 必须一致（以前前者是 en、后者是 zh）。
func TestEffectiveLocaleMatchesSettingsEndpoint(t *testing.T) {
	for _, tc := range []struct {
		lang, want string
	}{
		{"en_US.UTF-8", "en"},
		{"zh_CN.UTF-8", "zh"},
		{"fr_FR.UTF-8", "fr"},
		{"", i18n.DefaultLocale},
	} {
		t.Run(tc.want+"/"+tc.lang, func(t *testing.T) {
			lang := tc.lang
			d := startDaemonWith(t, "", WithGetenv(func(k string) string {
				switch k {
				case "LANG":
					return lang
				case "LC_ALL", "LC_MESSAGES":
					// 优先级高于 LANG；CI 的 macOS runner 会预设 LC_ALL，必须显式清空才能测 LANG 分支。
					return ""
				case "TZ":
					return "Asia/Shanghai"
				}
				return os.Getenv(k)
			}))
			var got map[string]any
			d.getJSON(t, "/api/v1/settings", &got)
			if got["effective_locale"] != tc.want {
				t.Errorf("effective_locale = %v，期望与内核一致的 %q", got["effective_locale"], tc.want)
			}
			if _, set := got["locale"]; set {
				t.Errorf("从未设置语言时不应返回 locale，得到 %v", got["locale"])
			}
			if got["effective_timezone"] != "Asia/Shanghai" {
				t.Errorf("effective_timezone 应取系统时区，得到 %v", got["effective_timezone"])
			}
		})
	}
}

func TestNewValidatesConfig(t *testing.T) {
	abs := t.TempDir()
	cases := []struct {
		cfg  config.Config
		code model.Code
	}{
		{config.Config{DataDir: "relative", ListenAddr: "127.0.0.1:0"}, model.CodeInvalid},
		{config.Config{DataDir: abs, ListenAddr: "0.0.0.0:46750"}, model.CodeInvalid},
		{config.Config{DataDir: abs, ListenAddr: "192.168.1.2:80"}, model.CodeInvalid},
		{config.Config{DataDir: abs, ListenAddr: "nonsense"}, model.CodeInvalid},
	}
	for _, tc := range cases {
		if _, err := New(tc.cfg, nil); !model.IsCode(err, tc.code) {
			t.Errorf("New(%+v) 应返回 %s，得到 %v", tc.cfg, tc.code, err)
		}
	}
	// 回环判定与 server 共用 config.IsLoopbackAddr：127.0.0.0/8 内的其他地址同样接受，
	// 不会出现"app.New 放行、server.New 才拒绝"的两套规则（LAY-07）。
	for _, addr := range []string{"127.0.0.1:0", "localhost:46750", "[::1]:0", "127.0.0.2:0"} {
		if _, err := New(config.Config{DataDir: abs, ListenAddr: addr}, nil); err != nil {
			t.Errorf("回环地址 %s 应被接受，得到 %v", addr, err)
		}
	}
}

func TestEnsureDataDirTightensPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 没有 POSIX 权限位")
	}
	dir := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureDataDir(dir, discardLogger()); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(dir)
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("数据目录权限应为 0700，得到 %v", fi.Mode().Perm())
	}
	fresh := filepath.Join(t.TempDir(), "a", "b")
	if err := ensureDataDir(fresh, discardLogger()); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(fresh); fi.Mode().Perm() != 0o700 {
		t.Fatalf("新建数据目录权限应为 0700，得到 %v", fi.Mode().Perm())
	}
}

func Test并行任务设置在重启时恢复且兼容旧配置(t *testing.T) {
	for _, tc := range []struct {
		saved string
		want  int
	}{{"", 2}, {"1", 1}, {`"4"`, 4}, {"0", 2}, {"9", 2}, {"invalid", 2}} {
		got, err := loadSettings(context.Background(), mapSettings{server.SettingMaxParallel: tc.saved}, func(string) string { return "" }, discardLogger())
		if err != nil || got.MaxParallel != tc.want {
			t.Fatalf("配置%q应恢复%d，实际%+v %v", tc.saved, tc.want, got, err)
		}
	}
}
