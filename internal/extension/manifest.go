package extension

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// ManifestFile 是插件目录里的清单文件名。
const ManifestFile = "extension.json"

const (
	// MaxExtensionNameLength 是插件与 MCP 服务显示名的最大长度。
	MaxExtensionNameLength = 64
	// MaxToolNameLength 是暴露给模型的工具名最大长度（主流模型接口的限制）。
	MaxToolNameLength = 64
	// MaxToolDescriptionLength 是单个工具描述的最大长度。
	MaxToolDescriptionLength = 4096
	// MaxPluginTools 是一个插件可声明的工具数上限。
	MaxPluginTools = 128
)

// toolNameRe 是模型接口接受的工具名格式。
var toolNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// defaultInputSchema 是未声明参数的工具使用的 schema。
var defaultInputSchema = json.RawMessage(`{"type":"object"}`)

// Permissions 是插件声明的最小授权；集成阶段由 sandbox 按它约束子进程，权限规则按它推导风险等级。
type Permissions struct {
	// Filesystem 表示需要读写工作区之外的文件。
	Filesystem bool `json:"filesystem"`
	// Network 表示需要访问网络。
	Network bool `json:"network"`
	// Shell 表示需要执行任意命令。
	Shell bool `json:"shell"`
}

// Risk 把声明的权限映射为工具风险等级：shell → execute，network → network，filesystem → write，否则 read。
// 只用于收紧审批：声明越多，审批越严。
func (p Permissions) Risk() model.ToolRisk {
	switch {
	case p.Shell:
		return model.RiskExecute
	case p.Network:
		return model.RiskNetwork
	case p.Filesystem:
		return model.RiskWrite
	default:
		return model.RiskRead
	}
}

// PluginTool 是插件清单里的一条工具声明。
type PluginTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// PluginManifest 是 extension.json 的内容。
type PluginManifest struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
	// Entry 是每次工具调用启动的程序：绝对路径、相对于插件目录的路径，或 PATH 中的命令名。
	Entry string `json:"entry"`
	// Args 是传给 Entry 的固定参数。
	Args        []string     `json:"args,omitempty"`
	Permissions Permissions  `json:"permissions"`
	Tools       []PluginTool `json:"tools"`
}

// ParsePluginManifest 解析并校验 extension.json；未知字段被忽略以便向前兼容。
func ParsePluginManifest(data []byte) (PluginManifest, error) {
	var m PluginManifest
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&m); err != nil {
		return PluginManifest{}, model.Wrap(model.CodeInvalid, ManifestFile+" 不是合法的 JSON", err)
	}
	if err := m.validate(); err != nil {
		return PluginManifest{}, err
	}
	return m, nil
}

func (m *PluginManifest) validate() error {
	m.Name = strings.TrimSpace(m.Name)
	if err := validateDisplayName(m.Name); err != nil {
		return err
	}
	m.Kind = strings.TrimSpace(m.Kind)
	if m.Kind != "" && m.Kind != string(model.ExtensionPlugin) {
		return model.Errorf(model.CodeInvalid, "kind 必须是 %q，收到 %q", model.ExtensionPlugin, m.Kind)
	}
	m.Kind = string(model.ExtensionPlugin)
	m.Version = strings.TrimSpace(m.Version)
	if len(m.Version) > 32 {
		return model.NewError(model.CodeInvalid, "version 不能超过 32 个字符")
	}
	if len(m.Description) > MaxToolDescriptionLength {
		return model.NewError(model.CodeInvalid, "description 过长")
	}
	m.Entry = strings.TrimSpace(m.Entry)
	if m.Entry == "" {
		return model.NewError(model.CodeInvalid, "entry 不能为空")
	}
	if strings.ContainsRune(m.Entry, 0) {
		return model.NewError(model.CodeInvalid, "entry 含有非法字符")
	}
	for _, a := range m.Args {
		if strings.ContainsRune(a, 0) {
			return model.NewError(model.CodeInvalid, "args 含有非法字符")
		}
	}
	if len(m.Tools) == 0 {
		return model.NewError(model.CodeInvalid, "插件至少要声明一个工具")
	}
	if len(m.Tools) > MaxPluginTools {
		return model.Errorf(model.CodeInvalid, "插件最多声明 %d 个工具", MaxPluginTools)
	}
	seen := make(map[string]struct{}, len(m.Tools))
	for i := range m.Tools {
		t := &m.Tools[i]
		t.Name = strings.TrimSpace(t.Name)
		if !toolNameRe.MatchString(t.Name) {
			return model.Errorf(model.CodeInvalid, "工具名 %q 不合法：只能是字母、数字、下划线与连字符，且不超过 %d 个字符", t.Name, MaxToolNameLength)
		}
		if _, dup := seen[t.Name]; dup {
			return model.Errorf(model.CodeInvalid, "工具名 %q 重复", t.Name)
		}
		seen[t.Name] = struct{}{}
		if len(t.Description) > MaxToolDescriptionLength {
			return model.Errorf(model.CodeInvalid, "工具 %q 的描述过长", t.Name)
		}
		schema := bytes.TrimSpace(t.InputSchema)
		if len(schema) == 0 || bytes.Equal(schema, []byte("null")) {
			t.InputSchema = defaultInputSchema
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(schema, &obj); err != nil {
			return model.Errorf(model.CodeInvalid, "工具 %q 的 input_schema 必须是 JSON 对象", t.Name)
		}
		t.InputSchema = json.RawMessage(schema)
	}
	return nil
}

// validateDisplayName 校验插件与 MCP 服务的显示名：非空、不超过上限、不含控制字符与路径分隔符。
func validateDisplayName(name string) error {
	if name == "" {
		return model.NewError(model.CodeInvalid, "name 不能为空")
	}
	if len(name) > MaxExtensionNameLength {
		return model.Errorf(model.CodeInvalid, "name 不能超过 %d 个字节", MaxExtensionNameLength)
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return model.Errorf(model.CodeInvalid, "name %q 含有非法字符", name)
		}
	}
	return nil
}
