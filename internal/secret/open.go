package secret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// 后端名；与 Store 实现的 Backend() 返回值一致，也是 BackendFileName 文件里记录的内容。
const (
	BackendKeyring = "keyring"
	BackendFile    = "file"
)

// BackendFileName 是数据目录下记录"凭据实际存放在哪个后端"的文件名（0600）。
const BackendFileName = "secret.backend"

// ErrBackendUnavailable 表示数据目录记录（或调用方显式指定）的后端当前不可用。
// OpenWith 此时不会静默改用另一个后端——那样已存的凭据会"消失"，新写入的又落到另一处；
// 调用方应把它报告为 unavailable，并提示用户显式选择后端。
var ErrBackendUnavailable = errors.New("secret: 凭据后端当前不可用")

// Options 控制 OpenWith 的后端选择。
type Options struct {
	// Keyring 是要使用的系统凭据存储；nil 表示 SystemKeyring()。测试注入假实现。
	Keyring Keyring
	// Backend 显式选择后端（BackendKeyring 或 BackendFile）；为空表示沿用数据目录的记录，
	// 没有记录（首次启动）时自动选择。显式值与记录不同即视为用户主动切换：改用新后端并更新记录，
	// 原后端里的凭据不迁移（日志警告）。
	Backend string
	// DisableKeyring 为 true 时等同于 Backend = BackendFile（无桌面环境的服务器、容器、测试）。
	DisableKeyring bool
}

// Open 按数据目录的记录打开凭据存储；首次启动时自动选择（系统凭据存储可用则用它，否则用加密文件）。
// 日志只记录后端与路径，不记录任何凭据值。logger 为 nil 时不输出日志。
func Open(ctx context.Context, dataDir string, logger *slog.Logger) (Store, error) {
	return OpenWith(ctx, dataDir, logger, Options{})
}

// OpenWith 打开凭据存储。选择规则：
//
//  1. 显式指定（Options.Backend / DisableKeyring）：使用该后端；系统凭据存储不可用时返回
//     ErrBackendUnavailable。
//  2. 数据目录有记录（<dataDir>/secret.backend）：使用记录的后端；它不可用时返回
//     ErrBackendUnavailable，而不是静默回退。
//  3. 都没有（首次启动）：系统凭据存储可用则用它，否则回退到加密文件并警告。
//
// 选定后（含首次与显式切换）把后端写入记录；记录内容无法识别且没有显式指定时也返回
// ErrBackendUnavailable。ctx 已结束时返回 ctx.Err()。
func OpenWith(ctx context.Context, dataDir string, logger *slog.Logger, opts Options) (Store, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	want := strings.TrimSpace(opts.Backend)
	if opts.DisableKeyring {
		want = BackendFile
	}
	if want != "" && want != BackendKeyring && want != BackendFile {
		return nil, fmt.Errorf("secret: 未知的凭据后端 %q（可选 %s、%s）", want, BackendKeyring, BackendFile)
	}
	recordPath := filepath.Join(dataDir, BackendFileName)
	recorded, err := readBackendRecord(recordPath)
	if err != nil && want == "" {
		return nil, err
	}
	kr := opts.Keyring
	if kr == nil {
		kr = SystemKeyring()
	}

	var store Store
	switch choice := firstNonEmpty(want, recorded); choice {
	case BackendKeyring:
		if err := probe(ctx, kr); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			source := "数据目录记录"
			if want != "" {
				source = "显式指定"
			}
			return nil, fmt.Errorf("%w: %s的后端是 %s，但系统凭据存储当前不可用（%v）；不会自动改用其他后端",
				ErrBackendUnavailable, source, BackendKeyring, err)
		}
		store = NewKeyringStore(kr)
	case BackendFile:
		fs, err := OpenFileStore(dataDir)
		if err != nil {
			return nil, err
		}
		store = fs
	default: // 首次启动：自动选择。
		if err := probe(ctx, kr); err == nil {
			store = NewKeyringStore(kr)
			if _, statErr := os.Stat(filepath.Join(dataDir, FileName)); statErr == nil {
				logger.Warn("数据目录里已有加密文件存储，但本次选择了系统凭据存储；文件中的凭据不会被读取",
					"path", filepath.Join(dataDir, FileName), "hint", "如需继续使用加密文件，请显式选择 file 后端")
			}
		} else {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			logger.Warn("系统凭据存储不可用，回退到加密文件；凭据的安全性将依赖数据目录的文件权限",
				"error", err.Error(), "path", filepath.Join(dataDir, FileName))
			fs, err := OpenFileStore(dataDir)
			if err != nil {
				return nil, err
			}
			store = fs
		}
	}

	name := BackendName(store)
	if name != recorded {
		if recorded != "" {
			logger.Warn("凭据后端已按显式选择切换；原后端中已保存的凭据不会迁移，需要重新录入",
				"from", recorded, "to", name)
		}
		if err := os.MkdirAll(dataDir, dirPerm); err != nil {
			return nil, fmt.Errorf("secret: 创建数据目录失败: %w", err)
		}
		if err := writeFileAtomic(recordPath, []byte(name+"\n"), filePerm); err != nil {
			return nil, fmt.Errorf("secret: 记录凭据后端失败: %w", err)
		}
	}
	if name == BackendFile {
		logger.Info("凭据存储就绪", "backend", name, "path", filepath.Join(dataDir, FileName))
	} else {
		logger.Info("凭据存储就绪", "backend", name, "service", ServiceName)
	}
	return store, nil
}

// RecordedBackend 返回数据目录记录的凭据后端（BackendKeyring / BackendFile）；没有记录时返回空串。
// 记录内容无法识别时返回 ErrBackendUnavailable。供诊断与错误提示使用。
func RecordedBackend(dataDir string) (string, error) {
	return readBackendRecord(filepath.Join(dataDir, BackendFileName))
}

// readBackendRecord 读取后端记录；文件不存在时返回空串。
func readBackendRecord(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("secret: 读取凭据后端记录失败: %w", err)
	}
	switch v := string(bytes.TrimSpace(raw)); v {
	case BackendKeyring, BackendFile:
		return v, nil
	default:
		if len(v) > 32 {
			v = v[:32] + "…"
		}
		return "", fmt.Errorf("%w: 凭据后端记录 %s 的内容 %q 无法识别，需要显式选择后端", ErrBackendUnavailable, path, v)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
