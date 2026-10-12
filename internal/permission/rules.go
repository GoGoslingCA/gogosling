package permission

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// Effect 是规则命中后的处理方式。
type Effect string

const (
	// EffectAllow 直接放行。
	EffectAllow Effect = "allow"
	// EffectAsk 需要用户审批。
	EffectAsk Effect = "ask"
	// EffectDeny 直接拒绝，不询问。
	EffectDeny Effect = "deny"
)

// Rule 是规则文件中的一条规则。所有非空条件必须同时满足才算命中。
//
// 匹配语义：
//   - Tool 是工具名通配（* 匹配任意字符串，? 匹配单个字符）；空等价于 *。
//   - Risk 为空时不限制风险等级。
//   - Path 是路径通配：* 与 ? 不跨越路径分隔符，** 匹配零个或多个路径段；
//     以 ~/ 开头的模式在加载时展开为用户主目录。模式同时与原始路径、绝对路径和工作区相对路径比较
//     （Windows 上绝对路径已展开 8.3 短名）。大小写：darwin / windows 上不区分大小写（与默认文件系统一致）；
//     effect 为 deny 的规则在所有平台上都不区分大小写（宁可多拒绝，不能被 .SSH 之类的写法绕过）。
//   - Command 是 RE2 正则，与调用中的命令字符串比较；调用没有命令时该规则不命中。
type Rule struct {
	// ID 用于审计与日志中引用这条规则；可为空。
	ID      string         `json:"id,omitempty"`
	Tool    string         `json:"tool"`
	Risk    model.ToolRisk `json:"risk,omitempty"`
	Path    string         `json:"path,omitempty"`
	Command string         `json:"command,omitempty"`
	Effect  Effect         `json:"effect"`
	Comment string         `json:"comment,omitempty"`
	// Origin 记录规则来源："builtin"（随程序分发）、"user"（手工编辑）、"approval:<id>"（ScopeAlways 写入）。
	Origin string `json:"origin,omitempty"`
}

// RuleFile 是规则文件 <dataDir>/security/rules.json 的根结构。
type RuleFile struct {
	Version int    `json:"version"`
	Rules   []Rule `json:"rules"`
}

// ruleFileVersion 是当前规则文件格式版本。
const ruleFileVersion = 1

// ruleFilePerm 与 ruleDirPerm 遵循数据目录内 0600 / 0700 的约定。
const (
	ruleFilePerm = 0o600
	ruleDirPerm  = 0o700
)

// DefaultRulesPath 返回数据目录下规则文件的标准位置。
func DefaultRulesPath(dataDir string) string {
	return filepath.Join(dataDir, "security", "rules.json")
}

//go:embed defaults/rules.json
var defaultRulesJSON []byte

// DefaultRules 返回随程序分发的默认规则（副本）。嵌入文件由本包维护并有测试覆盖，
// 解析失败只会发生在构建产物损坏时，此时返回 CodeInternal。
func DefaultRules() ([]Rule, error) {
	var rf RuleFile
	if err := json.Unmarshal(defaultRulesJSON, &rf); err != nil {
		return nil, model.Wrap(model.CodeInternal, "permission: 内置规则文件无效", err)
	}
	return rf.Rules, nil
}

// compiledRule 是预编译后的规则。
type compiledRule struct {
	Rule
	index   int
	cmd     *regexp.Regexp
	pathPat string
}

// ruleSet 是一组预编译规则，不可变，可并发读取。
type ruleSet struct {
	rules []compiledRule
}

// validRisks 列出合法的风险等级。
var validRisks = map[model.ToolRisk]bool{
	model.RiskRead: true, model.RiskWrite: true, model.RiskExecute: true,
	model.RiskNetwork: true, model.RiskDestructive: true,
}

// compileRules 校验并预编译规则；home 用于展开路径模式中的 ~。
func compileRules(rules []Rule, home string) (*ruleSet, error) {
	out := make([]compiledRule, 0, len(rules))
	for i, r := range rules {
		cr := compiledRule{Rule: r, index: i}
		label := r.ID
		if label == "" {
			label = fmt.Sprintf("#%d", i)
		}
		switch r.Effect {
		case EffectAllow, EffectAsk, EffectDeny:
		default:
			return nil, model.Errorf(model.CodeInvalid, "规则 %s 的 effect 无效: %q", label, r.Effect)
		}
		if r.Risk != "" && !validRisks[r.Risk] {
			return nil, model.Errorf(model.CodeInvalid, "规则 %s 的 risk 无效: %q", label, r.Risk)
		}
		if r.Path != "" {
			pat := expandHome(filepath.ToSlash(r.Path), home)
			for _, seg := range strings.Split(pat, "/") {
				if seg == "**" {
					continue
				}
				if _, err := path.Match(seg, ""); err != nil {
					return nil, model.Errorf(model.CodeInvalid, "规则 %s 的 path 模式无效: %q", label, r.Path)
				}
			}
			cr.pathPat = pat
		}
		if r.Command != "" {
			re, err := regexp.Compile(r.Command)
			if err != nil {
				return nil, model.Errorf(model.CodeInvalid, "规则 %s 的 command 正则无效: %v", label, err)
			}
			cr.cmd = re
		}
		if r.Tool == "" && r.Path == "" && r.Command == "" && r.Risk == "" {
			return nil, model.Errorf(model.CodeInvalid, "规则 %s 没有任何条件", label)
		}
		out = append(out, cr)
	}
	return &ruleSet{rules: out}, nil
}

// firstMatch 返回第一条 effect 相同且命中的规则；没有则返回 nil。
// foldFS 表示文件系统不区分大小写（caseInsensitivePaths）；deny 规则无论如何都不区分大小写。
func (rs *ruleSet) firstMatch(effect Effect, tool string, risk model.ToolRisk, f facts, foldFS bool) *compiledRule {
	fold := foldFS || effect == EffectDeny
	for i := range rs.rules {
		r := &rs.rules[i]
		if r.Effect != effect {
			continue
		}
		if r.matches(tool, risk, f, fold) {
			return r
		}
	}
	return nil
}

// matches 判断规则是否命中；fold 为 true 时路径条件不区分大小写。
func (r *compiledRule) matches(tool string, risk model.ToolRisk, f facts, fold bool) bool {
	if !wildcardMatch(r.Tool, tool) {
		return false
	}
	if r.Risk != "" && r.Risk != risk {
		return false
	}
	if r.pathPat != "" {
		hit := false
		for _, c := range f.paths {
			if matchGlobFold(r.pathPat, c.raw, fold) || (c.abs != "" && matchGlobFold(r.pathPat, c.abs, fold)) ||
				(c.rel != "" && matchGlobFold(r.pathPat, c.rel, fold)) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if r.cmd != nil {
		if f.command == "" || !r.cmd.MatchString(f.command) {
			return false
		}
	}
	return true
}

// wildcardMatch 实现工具名通配：* 匹配任意字符串（包括空串与 /），? 匹配单个字符，
// \ 让下一个字符按字面匹配。空模式等价于 *。
func wildcardMatch(pattern, s string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	p, n := 0, 0
	star, mark := -1, 0
	for n < len(s) {
		switch {
		case p < len(pattern) && pattern[p] == '\\' && p+1 < len(pattern):
			if pattern[p+1] == s[n] {
				p += 2
				n++
				continue
			}
			if star >= 0 {
				p = star + 1
				mark++
				n = mark
				continue
			}
			return false
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == s[n]):
			p++
			n++
		case p < len(pattern) && pattern[p] == '*':
			star = p
			mark = n
			p++
		case star >= 0:
			p = star + 1
			mark++
			n = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// matchGlob 实现带 ** 的路径通配（区分大小写）；pattern 与 name 都应使用 / 作为分隔符。
func matchGlob(pattern, name string) bool {
	return matchGlobFold(pattern, name, false)
}

// matchGlobFold 与 matchGlob 相同；fold 为 true 时模式与路径都先转小写再比较。
// 字符类同样被转小写（[A-Z] → [a-z]），因此语义仍然一致。
func matchGlobFold(pattern, name string, fold bool) bool {
	if fold {
		pattern, name = strings.ToLower(pattern), strings.ToLower(name)
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(filepath.ToSlash(name), "/"))
}

func matchSegments(p, n []string) bool {
	for len(p) > 0 {
		if p[0] == "**" {
			for len(p) > 0 && p[0] == "**" {
				p = p[1:]
			}
			if len(p) == 0 {
				return true
			}
			for i := 0; i <= len(n); i++ {
				if matchSegments(p, n[i:]) {
					return true
				}
			}
			return false
		}
		if len(n) == 0 {
			return false
		}
		ok, err := path.Match(p[0], n[0])
		if err != nil || !ok {
			return false
		}
		p, n = p[1:], n[1:]
	}
	return len(n) == 0
}

// expandHome 把以 ~ 开头的模式或路径展开为主目录；home 为空时原样返回。
func expandHome(p, home string) string {
	if home == "" {
		return p
	}
	if p == "~" {
		return filepath.ToSlash(home)
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.ToSlash(home) + "/" + strings.TrimLeft(p[2:], `/\`)
	}
	return p
}

// ensureRuleFile 在规则文件不存在时写入内置默认规则；返回是否新建。
func ensureRuleFile(p string) (bool, error) {
	if _, err := os.Stat(p); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, model.Wrap(model.CodeInternal, "读取规则文件失败", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), ruleDirPerm); err != nil {
		return false, model.Wrap(model.CodeInternal, "创建规则目录失败", err)
	}
	if err := writeFileAtomic(p, defaultRulesJSON); err != nil {
		return false, err
	}
	return true, nil
}

// loadRuleFile 读取并解析规则文件。
func loadRuleFile(p string) (*RuleFile, error) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, model.Wrap(model.CodeInternal, "读取规则文件失败", err)
	}
	var rf RuleFile
	if err := json.Unmarshal(raw, &rf); err != nil {
		return nil, model.Wrap(model.CodeInvalid, "规则文件不是合法的 JSON: "+p, err)
	}
	if rf.Version > ruleFileVersion {
		return nil, model.Errorf(model.CodeInvalid, "规则文件版本 %d 高于当前支持的 %d", rf.Version, ruleFileVersion)
	}
	return &rf, nil
}

// writeRuleFile 序列化并原子写入规则文件。
func writeRuleFile(p string, rf *RuleFile) error {
	rf.Version = ruleFileVersion
	raw, err := json.MarshalIndent(rf, "", "  ")
	if err != nil {
		return model.Wrap(model.CodeInternal, "序列化规则失败", err)
	}
	return writeFileAtomic(p, append(raw, '\n'))
}

// writeFileAtomic 先写同目录临时文件再重命名，避免半写入的规则文件被读到。
func writeFileAtomic(p string, data []byte) error {
	dir := filepath.Dir(p)
	tmp, err := os.CreateTemp(dir, ".rules-*.tmp")
	if err != nil {
		return model.Wrap(model.CodeInternal, "创建临时规则文件失败", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(ruleFilePerm); err != nil && !isWindowsChmodError(err) {
		_ = tmp.Close()
		cleanup()
		return model.Wrap(model.CodeInternal, "设置规则文件权限失败", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return model.Wrap(model.CodeInternal, "写入规则文件失败", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return model.Wrap(model.CodeInternal, "同步规则文件失败", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return model.Wrap(model.CodeInternal, "关闭规则文件失败", err)
	}
	if err := os.Rename(tmpName, p); err != nil {
		cleanup()
		return model.Wrap(model.CodeInternal, "替换规则文件失败", err)
	}
	return nil
}

// isWindowsChmodError 容忍 Windows 上不支持的权限位设置。
func isWindowsChmodError(err error) bool {
	return errors.Is(err, errors.ErrUnsupported)
}
