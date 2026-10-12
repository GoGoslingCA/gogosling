// Package preview 提供有界、只读的格式识别与内容解析；通过接口读取工作区，不访问模型、数据库或网络。
package preview

import (
	"context"
	"io"
	"io/fs"
)

// File 是宿主已验证路径的普通文件句柄；调用方必须关闭。
type File interface {
	io.ReaderAt
	io.ReadSeeker
	io.Closer
	Stat() (fs.FileInfo, error)
}

// Source 将文件权限与路径约束留在工作区适配器。
type Source interface {
	Open(ctx context.Context, agentID, path string) (File, error)
}

// Input 是解析器的只读输入，不提供绝对路径。
type Input struct {
	Name   string
	Size   int64
	Reader io.ReaderAt
}

// Parser 不得访问输入以外的文件或网络；阻塞读取与遍历必须尊重 ctx。
type Parser interface {
	Parse(ctx context.Context, input Input) (Document, error)
}

// ParseFunc 将格式模块的函数适配为 Parser。
type ParseFunc func(context.Context, Input) (Document, error)

// Parse 调用对应格式模块。
func (f ParseFunc) Parse(ctx context.Context, in Input) (Document, error) { return f(ctx, in) }

// Format 是只读的注册项；模块在组合时登记，不在请求中修改。
type Format struct {
	ID         string
	Extensions []string
	Kind       string
	MIME       string
	Parser     Parser
}

// Run 是不含可执行内容的段内文字。
type Run struct {
	Text   string `json:"text"`
	Bold   bool   `json:"bold,omitempty"`
	Italic bool   `json:"italic,omitempty"`
}

// Block 表示文段、标题、图片或表格。图片只允许包内经过类型检查的 data URL。
type Block struct {
	Kind  string     `json:"kind"`
	Text  string     `json:"text,omitempty"`
	Level int        `json:"level,omitempty"`
	Runs  []Run      `json:"runs,omitempty"`
	Rows  [][]string `json:"rows,omitempty"`
	Image string     `json:"image,omitempty"`
}

// Slide 按源文件的逻辑顺序列出内容；不执行动画、脚本或嵌入对象。
type Slide struct {
	Name   string  `json:"name"`
	Blocks []Block `json:"blocks"`
}

// Sheet 保留单元格坐标与缓存值，不计算公式或更新外部数据。
type Sheet struct {
	Name string     `json:"name"`
	Rows [][]string `json:"rows"`
}

// Entry 是压缩包内目录项；不会提取到磁盘。
type Entry struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Directory bool   `json:"directory"`
}

// Document 是所有展示器共享的、可序列化的纯数据描述。
type Document struct {
	Format   string   `json:"format"`
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Size     int64    `json:"size"`
	MIME     string   `json:"mime"`
	Version  string   `json:"version"`
	Text     string   `json:"text,omitempty"`
	Blocks   []Block  `json:"blocks,omitempty"`
	Slides   []Slide  `json:"slides,omitempty"`
	Sheets   []Sheet  `json:"sheets,omitempty"`
	Entries  []Entry  `json:"entries,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Limits 是默认全局预算；单次解析不能突破这些约束。
const (
	MaxText       = 2 << 20
	MaxFile       = 64 << 20
	MaxStructured = 32 << 20
	MaxExpanded   = 64 << 20
	MaxPart       = 8 << 20
	MaxEntries    = 2000
	MaxPages      = 200
	MaxCells      = 20000
	MaxColumns    = 200
)
