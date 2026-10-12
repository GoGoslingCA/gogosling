package preview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"strings"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// Service 在多个窗口间共享解析槽。并发安全，无后台缓存或等待队列。
type Service struct {
	source  Source
	formats []Format
	gate    chan struct{}
	log     *slog.Logger
}

// New 注册默认格式；允许组合根追加新模块，重复扩展名由先注册项优先匹配。
func New(source Source, log *slog.Logger, extra ...Format) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{source: source, formats: append(extra, builtins()...), gate: make(chan struct{}, 1), log: log}
}

// Error 创建本地化、不会泄漏正文/绝对路径的领域错误。
func Error(reason string) error {
	code := model.CodeInvalid
	switch reason {
	case "unsupported", "converter_unavailable", "busy", "unavailable":
		code = model.CodeUnavailable
	case "changed":
		code = model.CodeConflict
	case "timeout":
		code = model.CodeTimeout
	case "cancelled":
		code = model.CodeCancelled
	}
	return &model.Error{Code: code, Message: i18n.T("en", "preview."+reason, nil), Details: map[string]any{"reason": "preview_" + reason}}
}

// Version 描述本次读取的大小及修改时间，用于预览与原文件之间的更新检测。
func Version(info fs.FileInfo) string {
	return fmt.Sprintf("%x-%x", info.Size(), info.ModTime().UnixNano())
}

// Open 验证并打开普通文件；媒体不经过解析队列，流由调用方及时关闭。
func (s *Service) Open(ctx context.Context, agentID, rel, version string) (File, fs.FileInfo, error) {
	f, err := s.source.Open(ctx, agentID, rel)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, Error("unavailable")
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, nil, Error("unsupported")
	}
	if info.Size() > MaxFile {
		f.Close()
		return nil, nil, Error("too_large")
	}
	if version != "" && version != Version(info) {
		f.Close()
		return nil, nil, Error("changed")
	}
	return f, info, nil
}

// Preview 在有界时限中识别、解析并检查文件是否变化。
func (s *Service) Preview(ctx context.Context, agentID, rel string) (out Document, err error) {
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	default:
		return out, Error("busy")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	started := time.Now()
	formatID := "unknown"
	stage := "read"
	defer func() {
		if recover() != nil {
			err = Error("corrupt")
		}
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				err = Error("timeout")
			} else {
				err = Error("cancelled")
			}
		}
		code := "ok"
		if err != nil {
			code = string(model.CodeOf(err))
		}
		s.log.Debug("content_preview", "request_id", ctx.Value(requestIDKey{}), "stage", stage, "format", formatID, "code", code, "duration_ms", time.Since(started).Milliseconds(), "size_bytes", out.Size)
	}()
	f, info, err := s.Open(ctx, agentID, rel, "")
	if err != nil {
		return out, err
	}
	defer f.Close()
	stage = "detect"
	format := s.detect(info.Name())
	formatID = format.ID
	out = Document{Format: format.ID, Kind: format.Kind, Name: info.Name(), Size: info.Size(), MIME: format.MIME, Version: Version(info)}
	if format.Parser != nil {
		if info.Size() > MaxStructured && format.ID != "pdf" {
			return out, Error("too_large")
		}
		stage = "parse"
		parsed, e := format.Parser.Parse(ctx, Input{Name: info.Name(), Size: info.Size(), Reader: contextReader{ctx, f}})
		if e != nil {
			return out, e
		}
		out.Text = parsed.Text
		out.Blocks = parsed.Blocks
		out.Slides = parsed.Slides
		out.Sheets = parsed.Sheets
		out.Entries = parsed.Entries
		out.Warnings = parsed.Warnings
		if parsed.Kind != "" {
			out.Kind = parsed.Kind
		}
	}
	after, e := f.Stat()
	if e != nil || !os.SameFile(info, after) || Version(info) != Version(after) {
		return Document{}, Error("changed")
	}
	encoded, e := json.Marshal(out)
	if e != nil || len(encoded) > MaxPart {
		return Document{}, Error("too_large")
	}
	return out, nil
}

// Detect 只返回格式元数据，供 HTTP 内容头与能力列表复用。
func (s *Service) Detect(name string) Format { return s.detect(name) }
func (s *Service) detect(name string) Format {
	ext := strings.ToLower(path.Ext(name))
	for _, f := range s.formats {
		for _, e := range f.Extensions {
			if e == ext {
				return f
			}
		}
	}
	return Format{ID: "unknown", Kind: "text", MIME: "text/plain", Parser: ParseFunc(parseText)}
}

type contextReader struct {
	ctx context.Context
	r   io.ReaderAt
}

func (r contextReader) ReadAt(p []byte, off int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.ReadAt(p, off)
}
func builtins() []Format {
	return []Format{
		{"markdown", []string{".md", ".markdown", ".mdx"}, "markdown", "text/markdown", ParseFunc(parseText)},
		{"html", []string{".html", ".htm", ".svg"}, "html", "text/html", ParseFunc(parseText)},
		{"csv", []string{".csv", ".tsv"}, "sheet", "text/csv", ParseFunc(parseDelimited)},
		{"word", []string{".docx", ".dotx", ".docm", ".dotm"}, "document", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ParseFunc(parseWord)},
		{"slides", []string{".pptx", ".potx", ".ppsx", ".pptm", ".ppsm", ".potm"}, "slides", "application/vnd.openxmlformats-officedocument.presentationml.presentation", ParseFunc(parseSlides)},
		{"sheets", []string{".xlsx", ".xltx", ".xlsm", ".xltm"}, "sheet", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ParseFunc(parseSheets)},
		{"opendocument", []string{".odt", ".ott", ".ods", ".ots", ".odp", ".otp"}, "document", "application/zip", ParseFunc(parseOpenDocument)},
		{"archive", []string{".zip", ".tar", ".gz", ".tgz"}, "archive", "application/octet-stream", ParseFunc(parseArchive)},
		{"pdf", []string{".pdf"}, "pdf", "application/pdf", ParseFunc(validatePDF)},
		{"image", []string{".png", ".jpg", ".jpeg", ".gif", ".bmp", ".webp", ".avif", ".ico"}, "image", "application/octet-stream", nil},
		{"audio", []string{".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac", ".opus"}, "audio", "application/octet-stream", nil},
		{"video", []string{".mp4", ".webm", ".mov", ".m4v", ".ogv"}, "video", "application/octet-stream", nil},
		{"legacy", []string{".doc", ".dot", ".ppt", ".pps", ".pot", ".xls", ".xlt", ".rtf"}, "conversion", "application/octet-stream", nil},
		{"unsupported", []string{".rar", ".7z", ".heic", ".heif", ".tif", ".tiff", ".pages", ".key", ".numbers"}, "unsupported", "application/octet-stream", nil},
	}
}
func validatePDF(ctx context.Context, in Input) (Document, error) {
	head := make([]byte, 5)
	_, err := in.Reader.ReadAt(head, 0)
	if err != nil || string(head) != "%PDF-" {
		return Document{}, Error("corrupt")
	}
	return Document{}, nil
}

type requestIDKey struct{}

// WithRequestID 由传输层绑定诊断标识，日志不携带文件路径或正文。
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// SafeError 丢弃底层错误中的路径/正文，仅保留稳定错误码与已知原因。
func SafeError(err error) error {
	if err == nil {
		return nil
	}
	code := model.CodeOf(err)
	reason := "unavailable"
	var me *model.Error
	if errors.As(err, &me) {
		if value, ok := me.Details["reason"].(string); ok && strings.HasPrefix(value, "preview_") {
			key := strings.TrimPrefix(value, "preview_")
			if _, exists := i18n.Lookup("en", "preview."+key); exists {
				reason = key
			}
		}
	}
	return &model.Error{Code: code, Message: i18n.T("en", "preview."+reason, nil), Details: map[string]any{"reason": "preview_" + reason}}
}
