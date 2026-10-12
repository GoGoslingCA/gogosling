// Package config 是进程级配置的唯一解析处：数据根目录、本地 API 监听地址、
// 子进程环境变量白名单、回环地址判定，以及进程级环境变量的名称。
//
// 优先级（由低到高）：内置默认值 < <数据目录>/config.json（可选）< 环境变量 < 命令行参数。
// 数据目录本身只由 命令行 > GOGOSLING_HOME > ~/.gogosling 决定（config.json 位于数据目录内，
// 不能反过来决定数据目录）。gogoslingd、CLI 与桌面壳都按这一规则找到同一个数据目录。
//
// 监听地址必须是回环地址（IsLoopbackAddr）：Load 对非回环地址返回 ErrInvalid，
// 本版本不提供绑定其他网卡的开关；组合根在创建 App 时还会再校验一次。
//
// 边界：本包是叶子包，只依赖标准库，不得导入其他 internal 包。
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// 进程级环境变量的名称（全仓唯一定义）。
const (
	// EnvHome 覆盖数据根目录。
	EnvHome = "GOGOSLING_HOME"
	// EnvListen 覆盖本地 API 监听地址。
	EnvListen = "GOGOSLING_LISTEN"
	// EnvSecretBackend 显式选择凭据后端："keyring" 或 "file"；为空表示沿用数据目录记录的后端
	// （首次启动时自动选择）。显式值会覆盖并更新记录。
	EnvSecretBackend = "GOGOSLING_SECRET_BACKEND"
	// EnvAllowedOrigins 是额外允许的 Origin（逗号分隔，开发用）。
	EnvAllowedOrigins = "GOGOSLING_ALLOWED_ORIGINS"
	// EnvResources 指向随发行包分发的只读资源根目录（其下有 extensions/bundled）。
	EnvResources = "GOGOSLING_RESOURCES"
	// EnvDev 非空时允许从源码仓库定位资源（开发用）。
	EnvDev = "GOGOSLING_DEV"
)

// DefaultListenAddr 是本地 API 的默认监听地址，只绑定回环接口。
const DefaultListenAddr = "127.0.0.1:46750"

// DefaultDirName 是用户主目录下默认数据目录的名称。
const DefaultDirName = ".gogosling"

// FileName 是数据目录下可选的进程配置文件名。
const FileName = "config.json"

// ErrInvalid 表示配置值不合法（例如非回环监听地址、config.json 格式错误）。
// 调用方可据此把它当作参数错误报告（gogoslingd 以退出码 2 退出）。
var ErrInvalid = errors.New("config: 配置无效")

// Config 是守护进程与 CLI 共享的进程级配置。
type Config struct {
	// DataDir 是数据根目录的绝对路径。
	DataDir string

	// ListenAddr 是本地 API 的监听地址（已校验为回环地址）。
	ListenAddr string
}

// fileConfig 是 config.json 的结构；未知字段视为错误，防止拼写错误被静默忽略。
type fileConfig struct {
	// Listen 是本地 API 的监听地址，与 GOGOSLING_LISTEN 同义（环境变量优先）。
	Listen string `json:"listen"`
}

// Load 解析当前进程的配置：默认值 < config.json < 环境变量 < 命令行参数。
//
// getenv 为 nil 时使用 os.Getenv；flagHome、flagListen 是命令行参数（空串表示未提供）。
// 所有值都先去掉首尾空白。config.json 不存在时忽略；存在但无法解析、含未知字段，
// 或最终的监听地址不是回环地址时返回包装了 ErrInvalid 的错误。
func Load(getenv func(string) string, flagHome, flagListen string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	dataDir, err := DataDir(getenv, flagHome)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{DataDir: dataDir, ListenAddr: DefaultListenAddr}

	fc, err := readFile(filepath.Join(dataDir, FileName))
	if err != nil {
		return Config{}, err
	}
	for _, v := range []string{fc.Listen, getenv(EnvListen), flagListen} {
		if v = strings.TrimSpace(v); v != "" {
			cfg.ListenAddr = v
		}
	}
	if !IsLoopbackAddr(cfg.ListenAddr) {
		return Config{}, fmt.Errorf("%w: 本地 API 只能监听回环地址（127.0.0.0/8、::1 或 localhost），得到 %q", ErrInvalid, cfg.ListenAddr)
	}
	return cfg, nil
}

// DataDir 返回数据根目录的绝对路径：flagHome > GOGOSLING_HOME > <用户主目录>/.gogosling。
//
// 这是数据目录规则的唯一实现；只需要找 daemon.json 的调用方（CLI）用它而不必读取 config.json。
// getenv 为 nil 时使用 os.Getenv。
func DataDir(getenv func(string) string, flagHome string) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	home := strings.TrimSpace(flagHome)
	if home == "" {
		home = strings.TrimSpace(getenv(EnvHome))
	}
	if home == "" {
		u, err := os.UserHomeDir()
		if err == nil && u == "" {
			err = errors.New("主目录为空")
		}
		if err != nil {
			return "", fmt.Errorf("config: 无法确定用户主目录，请设置环境变量 %s: %w", EnvHome, err)
		}
		home = filepath.Join(u, DefaultDirName)
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return "", fmt.Errorf("config: 无法解析数据目录 %q: %w", home, err)
	}
	return abs, nil
}

// readFile 读取可选的 config.json；文件不存在时返回零值。
func readFile(path string) (fileConfig, error) {
	var fc fileConfig
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fc, nil
	}
	if err != nil {
		return fc, fmt.Errorf("config: 读取 %s 失败: %w", path, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return fc, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fc); err != nil {
		return fc, fmt.Errorf("%w: %s 格式错误: %v", ErrInvalid, path, err)
	}
	if dec.More() {
		return fc, fmt.Errorf("%w: %s 只能包含一个 JSON 对象", ErrInvalid, path)
	}
	return fc, nil
}
