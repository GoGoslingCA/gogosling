package agent

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 模板目录布局：<root>/<id>/template.json（元数据）+ <root>/<id>/SOUL.md（系统提示）。
const (
	TemplateManifest = "template.json"
	TemplateSoul     = "SOUL.md"
)

// templateIDPattern 限制模板标识为小写字母、数字与连字符（它同时是目录名与 API 参数）。
var templateIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

//go:embed templates
var builtinFS embed.FS

// Template 是一份可用来创建智能体的预设：人格提示、工具集与自主等级。
type Template struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Description  string              `json:"description,omitempty"`
	SystemPrompt string              `json:"system_prompt"`
	Tools        []string            `json:"tools,omitempty"`
	Autonomy     model.AutonomyLevel `json:"autonomy"`
}

// manifest 是 template.json 的结构；SystemPrompt 来自同目录的 SOUL.md。
type manifest struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Tools       []string            `json:"tools"`
	Autonomy    model.AutonomyLevel `json:"autonomy"`
}

// Library 是一组按标识索引的模板；加载后只读，可并发使用。
type Library struct {
	byID map[string]Template
	ids  []string
}

// NewLibrary 由内存中的模板构造库（测试或程序化注册用）；标识重复时后者覆盖前者。
func NewLibrary(templates ...Template) *Library {
	lib := &Library{byID: make(map[string]Template, len(templates))}
	for _, t := range templates {
		if _, dup := lib.byID[t.ID]; !dup {
			lib.ids = append(lib.ids, t.ID)
		}
		lib.byID[t.ID] = t
	}
	sort.Strings(lib.ids)
	return lib
}

// LoadTemplates 从文件系统加载模板：根目录下每个子目录是一个模板，必须包含 template.json，
// SOUL.md 可选。清单里的 id 必须与目录名一致；任何一个模板不合法都会让整体加载失败，
// 以便在启动时暴露打包错误而不是静默跳过。
func LoadTemplates(fsys fs.FS) (*Library, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, model.Wrap(model.CodeInternal, "读取模板目录失败", err)
	}
	var templates []Template
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t, err := loadTemplate(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		templates = append(templates, t)
	}
	return NewLibrary(templates...), nil
}

func loadTemplate(fsys fs.FS, dir string) (Template, error) {
	raw, err := fs.ReadFile(fsys, path.Join(dir, TemplateManifest))
	if err != nil {
		return Template{}, model.Wrap(model.CodeInvalid, "模板 "+dir+" 缺少 "+TemplateManifest, err)
	}
	var m manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Template{}, model.Wrap(model.CodeInvalid, "模板 "+dir+" 的清单不合法", err)
	}
	if m.ID == "" {
		m.ID = dir
	}
	if m.ID != dir {
		return Template{}, model.Errorf(model.CodeInvalid, "模板 %s 的清单 id %q 与目录名不一致", dir, m.ID)
	}
	if !templateIDPattern.MatchString(m.ID) {
		return Template{}, model.Errorf(model.CodeInvalid, "模板标识 %q 只能包含小写字母、数字与连字符", m.ID)
	}
	if strings.TrimSpace(m.Name) == "" {
		return Template{}, model.Errorf(model.CodeInvalid, "模板 %s 缺少名称", dir)
	}
	if m.Autonomy == "" {
		m.Autonomy = model.DefaultAutonomy
	}
	if err := validateAutonomy(m.Autonomy); err != nil {
		return Template{}, fmt.Errorf("模板 %s: %w", dir, err)
	}
	var tools []string
	if m.Tools != nil {
		if tools, err = normalizeTools(m.Tools); err != nil {
			return Template{}, fmt.Errorf("模板 %s: %w", dir, err)
		}
	}
	soul, err := fs.ReadFile(fsys, path.Join(dir, TemplateSoul))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Template{}, model.Wrap(model.CodeInternal, "读取模板 "+dir+" 的 "+TemplateSoul+" 失败", err)
	}
	return Template{
		ID:           m.ID,
		Name:         strings.TrimSpace(m.Name),
		Description:  strings.TrimSpace(m.Description),
		SystemPrompt: normalizeSoul(soul),
		Tools:        tools,
		Autonomy:     m.Autonomy,
	}, nil
}

// bom 是 UTF-8 字节序标记；用字节构造以避免源码里出现裸 BOM。
var bom = string([]byte{0xEF, 0xBB, 0xBF})

// normalizeSoul 去掉 BOM、统一换行并修剪首尾空白，避免打包或检出时的平台差异进入提示词。
func normalizeSoul(raw []byte) string {
	text := strings.TrimPrefix(string(raw), bom)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.TrimSpace(text)
}

// BuiltinTemplates 加载随二进制分发的内置模板（internal/agent/templates）。
func BuiltinTemplates() (*Library, error) {
	sub, err := fs.Sub(builtinFS, "templates")
	if err != nil {
		return nil, model.Wrap(model.CodeInternal, "打开内置模板失败", err)
	}
	return LoadTemplates(sub)
}

// Get 按标识查找模板；返回的是副本，修改不影响库。
func (l *Library) Get(id string) (Template, bool) {
	t, ok := l.byID[id]
	if !ok {
		return Template{}, false
	}
	t.Tools = append([]string(nil), t.Tools...)
	return t, true
}

// List 按标识升序返回全部模板的副本。
func (l *Library) List() []Template {
	out := make([]Template, 0, len(l.ids))
	for _, id := range l.ids {
		t, _ := l.Get(id)
		out = append(out, t)
	}
	return out
}

// Len 返回模板数量。
func (l *Library) Len() int { return len(l.ids) }
