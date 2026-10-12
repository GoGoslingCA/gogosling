package observability

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

const (
	// LogDirName 是数据目录下存放日志的子目录名。
	LogDirName = "logs"
	// LogFileName 是守护进程当前日志文件名；轮转出的备份形如 gogoslingd-<时间戳>.log[.gz]。
	LogFileName = "gogoslingd.log"
	// DefaultMaxSizeBytes 是单个日志文件的默认上限（20 MiB）。
	DefaultMaxSizeBytes int64 = 20 << 20
	// DefaultMaxBackups 是默认保留的轮转备份数。
	DefaultMaxBackups = 5
)

// Config 是日志初始化参数；零值表示使用默认值。
type Config struct {
	// DataDir 是数据根目录；日志写到 <DataDir>/logs/gogoslingd.log。为空时只输出到 Stderr。
	DataDir string
	// Level 是最低输出级别；nil 表示 Info。
	Level slog.Leveler
	// MaxSizeBytes 是单个日志文件的大小上限；0 表示 DefaultMaxSizeBytes。
	MaxSizeBytes int64
	// MaxBackups 是保留的轮转备份数；0 表示 DefaultMaxBackups。
	MaxBackups int
	// Compress 为 true 时在后台把轮转出的备份压缩为 .gz。
	Compress bool
	// Stderr 是终端输出目标；nil 表示 os.Stderr，io.Discard 表示不输出到终端。
	Stderr io.Writer
	// AddSource 为 true 时在每条记录里带上调用位置（source）。
	AddSource bool
}

// LogPath 返回数据目录下的当前日志文件路径。
func LogPath(dataDir string) string {
	return filepath.Join(dataDir, LogDirName, LogFileName)
}

// Setup 初始化守护进程日志：同时输出到 stderr（文本格式）与数据目录下的日志文件（JSON 格式），
// 两路都经过脱敏与键名规范化。返回的 close 关闭日志文件并等待后台压缩结束，进程退出前必须调用。
//
// 文件以 0600 创建、目录 0700。时间字段统一为 UTC。
func Setup(cfg Config) (*slog.Logger, func() error, error) {
	level := cfg.Level
	if level == nil {
		level = slog.LevelInfo
	}
	stderr := cfg.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	opts := &slog.HandlerOptions{Level: level, AddSource: cfg.AddSource, ReplaceAttr: utcTime}

	handlers := []slog.Handler{slog.NewTextHandler(stderr, opts)}
	closeFn := func() error { return nil }
	if cfg.DataDir != "" {
		maxSize := cfg.MaxSizeBytes
		if maxSize <= 0 {
			maxSize = DefaultMaxSizeBytes
		}
		maxBackups := cfg.MaxBackups
		if maxBackups <= 0 {
			maxBackups = DefaultMaxBackups
		}
		file, err := openRotatingFile(LogPath(cfg.DataDir), maxSize, maxBackups, cfg.Compress)
		if err != nil {
			return nil, nil, err
		}
		handlers = append(handlers, slog.NewJSONHandler(file, opts))
		closeFn = file.Close
	}
	logger := slog.New(NewRedactingHandler(NewMultiHandler(handlers...)))
	return logger, closeFn, nil
}

// New 返回只写到 w 的 JSON 格式脱敏 Logger，供命令行工具与测试使用。level 为 nil 表示 Info。
func New(w io.Writer, level slog.Leveler) *slog.Logger {
	if level == nil {
		level = slog.LevelInfo
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: utcTime})
	return slog.New(NewRedactingHandler(h))
}

// utcTime 把顶层的 time 字段转为 UTC，保证日志文件里的时间与数据库一致。
func utcTime(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime {
		a.Value = slog.TimeValue(a.Value.Time().UTC())
	}
	return a
}

// errClosed 在日志已关闭后再次写入时由 rotatingFile 返回。
var errClosed = errors.New("observability: 日志文件已关闭")
