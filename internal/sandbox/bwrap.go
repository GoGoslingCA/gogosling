package sandbox

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// bwrapText 沿用沙箱现有的中文默认值；用户文案集中于 i18n，底层 OS 诊断保持原样。
func bwrapText(key string, args map[string]any) string {
	return i18n.T(i18n.LocaleZH, "sandbox."+key, args)
}

const (
	bwrapBinary       = "bwrap"
	bwrapProbeTimeout = 2 * time.Second
)

// BwrapProfile 描述 Linux 隔离边界；读取宿主文件，写入仅限显式目录和私有 /tmp。
type BwrapProfile struct {
	Dir          string
	WritableDirs []string
	AllowNetwork bool
}

type bwrapDirectory struct {
	path string
	info os.FileInfo
}

type bwrapProfile struct {
	dir          string
	writable     []bwrapDirectory
	allowNetwork bool
}

// BuildBwrapArgs 生成 bubblewrap 的策略参数（不含辅助程序、环境变量和最终命令）。
// 可写目录必须已存在、不是根目录或特殊挂载目录，且路径中不含符号链接。
// Run 额外固定可写目录的文件描述符，避免目录在校验后被替换导致绑定到其他路径。
func BuildBwrapArgs(p BwrapProfile) ([]string, error) {
	profile, err := validateBwrapProfile(p)
	if err != nil {
		return nil, err
	}
	return bwrapArgs(profile, nil), nil
}

func validateBwrapProfile(p BwrapProfile) (bwrapProfile, error) {
	dir, _, err := bwrapExistingDir(p.Dir)
	if err != nil {
		return bwrapProfile{}, err
	}
	profile := bwrapProfile{dir: dir, allowNetwork: p.AllowNetwork}
	seen := make(map[string]bool, len(p.WritableDirs))
	for _, path := range p.WritableDirs {
		real, info, err := bwrapExistingDir(path)
		if err != nil {
			return bwrapProfile{}, err
		}
		// 不静默将符号链接指向的其他目录变成可写边界；调用方需明确提供真实目录。
		if real != filepath.Clean(path) {
			return bwrapProfile{}, model.NewError(model.CodeInvalid, bwrapText("writable_symlink", map[string]any{"path": strconv.Quote(path)}))
		}
		if real == "/" || real == "/tmp" || bwrapSpecialPath(real) {
			return bwrapProfile{}, model.NewError(model.CodeInvalid, bwrapText("unsafe_writable_boundary", map[string]any{"path": strconv.Quote(path)}))
		}
		if !seen[real] {
			seen[real] = true
			profile.writable = append(profile.writable, bwrapDirectory{path: real, info: info})
		}
	}
	return profile, nil
}

func bwrapExistingDir(path string) (string, os.FileInfo, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return "", nil, model.NewError(model.CodeInvalid, bwrapText("directory_absolute", map[string]any{"path": strconv.Quote(path)}))
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", nil, model.Wrap(model.CodeInvalid, bwrapText("directory_unavailable", nil), err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", nil, model.Wrap(model.CodeInvalid, bwrapText("directory_unavailable", nil), err)
	}
	if !info.IsDir() {
		return "", nil, model.NewError(model.CodeInvalid, bwrapText("not_directory", nil))
	}
	return real, info, nil
}

func bwrapSpecialPath(path string) bool {
	for _, prefix := range []string{"/proc", "/sys", "/dev"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// bwrapArgs 的 mounts 顺序很重要：先只读宿主根，再覆盖 proc/dev/tmp，最后添加明确可写绑定。
func bwrapArgs(p bwrapProfile, fds []int) []string {
	args := []string{
		"--die-with-parent", "--new-session",
		"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--cap-drop", "ALL",
	}
	if !p.allowNetwork {
		args = append(args, "--unshare-net")
	}
	args = append(args,
		"--ro-bind", "/", "/",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
	)
	for i, dir := range p.writable {
		if fds == nil {
			args = append(args, "--bind", dir.path, dir.path)
		} else {
			args = append(args, "--bind-fd", strconv.Itoa(fds[i]), dir.path)
		}
	}
	return append(args, "--chdir", p.dir, "--clearenv")
}

// Bwrap 用 bubblewrap 的命名空间与挂载隔离执行命令；可以并发使用。
// 构造时确认隔离机制可执行，Run 时的任何失败均不会重试为 Direct。
type Bwrap struct {
	binary     string
	helperInfo os.FileInfo
	helperHash [sha256.Size]byte
	logger     *slog.Logger
	lookup     func(string) (string, bool)
}

// NewBwrap 创建 Linux Runner，并用相同策略实际执行 /bin/true 探测内核能力。
// 非 Linux、辅助程序不存在、命名空间受宿主限制或探测失败均返回 CodeUnavailable。
func NewBwrap(logger *slog.Logger) (*Bwrap, error) {
	if runtime.GOOS != "linux" {
		return nil, model.NewError(model.CodeUnavailable, bwrapText("linux_only", nil))
	}
	path, err := exec.LookPath(bwrapBinary)
	if err != nil {
		return nil, model.Wrap(model.CodeUnavailable, bwrapText("helper_missing", nil), err)
	}
	return newBwrap(path, logger)
}

func newBwrap(path string, logger *slog.Logger) (*Bwrap, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, model.Wrap(model.CodeUnavailable, bwrapText("helper_path_unavailable", nil), err)
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return nil, model.Wrap(model.CodeUnavailable, bwrapText("helper_path_unavailable", nil), err)
	}
	file, info, digest, err := openBwrapHelper(real)
	if err != nil {
		return nil, err
	}
	_ = file.Close()
	runner := &Bwrap{binary: real, helperInfo: info, helperHash: digest, logger: logger, lookup: os.LookupEnv}
	res, err := runner.Run(context.Background(), Spec{Command: []string{"/bin/true"}, Dir: "/", Timeout: bwrapProbeTimeout, MaxOutputBytes: 4096})
	if err != nil {
		return nil, model.Wrap(model.CodeUnavailable, bwrapText("probe_execution_failed", nil), err)
	}
	if res.TimedOut || res.ExitCode != 0 {
		return nil, model.Wrap(model.CodeUnavailable, bwrapText("probe_failed", nil), &bwrapProbeError{exitCode: res.ExitCode, timedOut: res.TimedOut, stderr: strings.TrimSpace(res.Stderr)})
	}
	return runner, nil
}

func openBwrapHelper(path string) (*os.File, os.FileInfo, [sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, [sha256.Size]byte{}, model.Wrap(model.CodeUnavailable, bwrapText("helper_unavailable", nil), err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		_ = file.Close()
		return nil, nil, [sha256.Size]byte{}, model.NewError(model.CodeUnavailable, bwrapText("helper_not_executable", nil))
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		return nil, nil, [sha256.Size]byte{}, model.Wrap(model.CodeUnavailable, bwrapText("helper_identity_read_failed", nil), err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, nil, [sha256.Size]byte{}, model.Wrap(model.CodeUnavailable, bwrapText("helper_identity_pin_failed", nil), err)
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return file, info, digest, nil
}

// bwrapProbeError 保留固定探测命令的失败原因，便于诊断宿主限制；不含用户命令或其环境值。
type bwrapProbeError struct {
	exitCode int
	timedOut bool
	stderr   string
}

func (e *bwrapProbeError) Error() string {
	return fmt.Sprintf("exit=%d timeout=%t: %s", e.exitCode, e.timedOut, e.stderr)
}

// Mechanism 实现 Isolator。
func (b *Bwrap) Mechanism() Mechanism { return MechanismBwrap }

// Run 实现 Runner；未列入白名单的宿主环境不传给辅助程序。
// 显式环境通过 --setenv 配置目标命令，不影响辅助程序的初始动态加载；目标命令隔离完成后才 exec。
func (b *Bwrap) Run(ctx context.Context, spec Spec) (Result, error) {
	spec, err := normalize(spec)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, ctxError(err)
	}
	profile, err := validateBwrapProfile(BwrapProfile{Dir: spec.Dir, WritableDirs: spec.WritableDirs, AllowNetwork: spec.AllowNetwork})
	if err != nil {
		return Result{}, err
	}
	for _, dir := range profile.writable {
		relative, err := filepath.Rel(dir.path, b.binary)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return Result{}, model.NewError(model.CodeInvalid, bwrapText("writable_helper", nil))
		}
	}
	helper, info, digest, err := openBwrapHelper(b.binary)
	if err != nil {
		return Result{}, err
	}
	defer helper.Close()
	if b.helperInfo == nil || !os.SameFile(b.helperInfo, info) || info.Mode() != b.helperInfo.Mode() || !info.ModTime().Equal(b.helperInfo.ModTime()) || digest != b.helperHash {
		return Result{}, model.NewError(model.CodeUnavailable, bwrapText("helper_identity_changed", nil))
	}
	filter, err := bwrapSocketFilter()
	if err != nil {
		return Result{}, err
	}
	var dirs []*os.File
	defer func() {
		for _, dir := range dirs {
			_ = dir.Close()
		}
	}()
	fds := make([]int, 0, len(profile.writable))
	for _, dir := range profile.writable {
		file, err := os.Open(dir.path)
		if err != nil {
			return Result{}, model.Wrap(model.CodeInvalid, bwrapText("writable_pin_failed", nil), err)
		}
		dirs = append(dirs, file)
		info, err := file.Stat()
		if err != nil || !os.SameFile(dir.info, info) {
			return Result{}, model.NewError(model.CodeInvalid, bwrapText("writable_changed", nil))
		}
		// fd 3 固定 helper，4 是策略管道，5 是 seccomp 管道；目录从 fd 6 起。
		fds = append(fds, len(dirs)+5)
	}
	args := bwrapArgs(profile, fds)
	// 通过此绑定消费并关闭 helper FD，不能将宿主挂载上的 FD 泄露给最终命令。
	args = append(args, "--ro-bind-fd", "3", b.binary, "--seccomp", "5")
	env := make(map[string]string, len(spec.Env)+3)
	for key, value := range spec.Env {
		env[key] = value
	}
	for _, key := range []string{"TMPDIR", "TEMP", "TMP"} {
		env[key] = "/tmp"
	}
	for _, variable := range buildEnv(env, b.lookup) {
		key, value, _ := strings.Cut(variable, "=")
		args = append(args, "--setenv", key, value)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return Result{}, model.Wrap(model.CodeInternal, bwrapText("args_pipe_failed", nil), err)
	}
	defer reader.Close()
	defer writer.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer writer.Close()
		// 参数走匿名管道，避免代理配置等环境值进入进程参数列表或临时文件。
		_, _ = io.WriteString(writer, strings.Join(args, "\x00")+"\x00")
	}()
	filterReader, filterWriter, err := os.Pipe()
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		<-done
		return Result{}, model.Wrap(model.CodeInternal, bwrapText("filter_pipe_failed", nil), err)
	}
	defer filterReader.Close()
	defer filterWriter.Close()
	filterDone := make(chan struct{})
	go func() {
		defer close(filterDone)
		defer filterWriter.Close()
		_, _ = filterWriter.Write(filter)
	}()
	files := append([]*os.File{helper, reader, filterReader}, dirs...)
	// bubblewrap 的 --args FD 只解析策略选项；最终命令必须在主 argv 中给出。
	argv := append([]string{"/proc/self/fd/3", "--args", "4", "--"}, spec.Command...)
	result, runErr := runProcess(ctx, spec, argv, buildEnv(nil, b.lookup), b.logger, files...)
	// 启动失败或取消时，关闭读取端让尚在写入大量参数的 goroutine 退出。
	_ = reader.Close()
	_ = writer.Close()
	_ = filterReader.Close()
	_ = filterWriter.Close()
	<-done
	<-filterDone
	return result, runErr
}
