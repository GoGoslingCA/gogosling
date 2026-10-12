package permission

import (
	"encoding/json"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// 本文件实现"事实提取"与内置防护：
//   - 从 Details 与调用参数中找出路径、命令、URL；
//   - 判断路径是否越出工作区；
//   - 识别危险命令（不可被规则文件覆盖的硬拒绝）。
//
// 这里不解析 shell 语法；对路径只做只读的规范化探测（Lstat / EvalSymlinks / Windows 长名，见 paths.go），
// 不读写文件内容。真正的隔离由 sandbox 与 workspace 包完成，本文件的目标是在判定阶段提前拦截与升级风险。

// pathFact 是一条路径及其规范化结果。
type pathFact struct {
	raw string
	// abs 是规范化后的绝对路径；无法锚定（相对路径且无工作区）时为空。
	abs string
	// rel 是工作区相对路径（仅当在工作区内）。
	rel string
	// outside 表示越出工作区或无法确认在工作区内。
	outside bool
	// inside 表示确认在工作区内。
	inside bool
}

// facts 是从一次调用中提取出来、用于判定的事实。
type facts struct {
	paths   []pathFact
	command string
	url     string
}

// outsidePaths 返回越界路径（优先返回绝对形式）。
func (f facts) outsidePaths() []string {
	var out []string
	for _, p := range f.paths {
		if !p.outside {
			continue
		}
		if p.abs != "" {
			out = append(out, p.abs)
		} else {
			out = append(out, p.raw)
		}
	}
	return out
}

// hasInsidePath 表示至少有一条路径被确认在工作区内。
func (f facts) hasInsidePath() bool {
	for _, p := range f.paths {
		if p.inside {
			return true
		}
	}
	return false
}

// rawPaths 返回原始路径列表。
func (f facts) rawPaths() []string {
	out := make([]string, 0, len(f.paths))
	for _, p := range f.paths {
		out = append(out, p.raw)
	}
	return out
}

// detailPathKeys 与 argPathKeys 是提取路径时查找的键；Details 优先于调用参数。
var (
	detailPathKeys     = []string{"path", "cwd"}
	detailPathListKeys = []string{"paths"}
	argPathKeys        = []string{"path", "file_path", "filepath", "file", "target_path", "source", "destination", "dest", "dir", "directory", "cwd"}
	argPathListKeys    = []string{"paths", "files"}
	commandKeys        = []string{"command", "cmd"}
	urlKeys            = []string{"url"}
)

// extractFacts 从 Details（调用方整理的结构化细节）与调用参数中提取事实。
// Details 中已有的事实优先；参数只作为兜底，避免调用方漏传时失去防护。
func extractFacts(details, args json.RawMessage, workspace, home string) facts {
	d := parseObject(details)
	a := parseObject(args)

	var raw []string
	raw = append(raw, collectStrings(d, detailPathKeys, detailPathListKeys)...)
	if len(raw) == 0 {
		raw = append(raw, collectStrings(a, argPathKeys, argPathListKeys)...)
	}
	cmd := firstCommand(d, commandKeys)
	if cmd == "" {
		cmd = firstCommand(a, commandKeys)
	}
	u := firstString(d, urlKeys)
	if u == "" {
		u = firstString(a, urlKeys)
	}

	f := facts{command: strings.TrimSpace(cmd), url: strings.TrimSpace(u)}
	for _, r := range raw {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		f.paths = append(f.paths, classifyPath(r, workspace, home))
	}
	return f
}

func parseObject(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

func collectStrings(m map[string]any, single, lists []string) []string {
	if m == nil {
		return nil
	}
	var out []string
	for _, k := range single {
		if s, ok := m[k].(string); ok && s != "" {
			out = append(out, s)
		}
	}
	for _, k := range lists {
		if arr, ok := m[k].([]any); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok && s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

func firstString(m map[string]any, keys []string) string {
	if m == nil {
		return ""
	}
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// firstCommand 接受字符串或字符串数组（argv 形式）。
func firstCommand(m map[string]any, keys []string) string {
	if m == nil {
		return ""
	}
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case []any:
			parts := make([]string, 0, len(v))
			for _, it := range v {
				if s, ok := it.(string); ok {
					parts = append(parts, s)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, " ")
			}
		}
	}
	return ""
}

// classifyPath 规范化路径并判断是否在工作区内。
//
// 规则：
//   - Windows 上先去掉 \\?\ 一类的设备路径前缀；路径与工作区的已存在部分都展开 8.3 短名（canonicalPath），
//     两者在同一种形式上比较。
//   - ~ 前缀用 home 展开；没有 home 时视为越界。
//   - 绝对路径（含以 / 或 \ 开头的根相对路径）：没有工作区时视为越界，否则按前缀判断。
//   - 相对路径：没有工作区时无法锚定，视为工作区内（由 workspace 包最终约束）；
//     否则拼接后判断，因此 ../x 这类逃逸会被识别。
func classifyPath(raw, workspace, home string) pathFact {
	pf := pathFact{raw: raw}
	if workspace != "" {
		workspace = canonicalPath(filepath.Clean(workspace))
	}
	p := raw
	if windowsPaths {
		p = stripVerbatimPrefix(p)
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home == "" {
			pf.outside = true
			return pf
		}
		p = expandHome(p, home)
	}
	rooted := filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`)
	switch {
	case rooted:
		pf.abs = canonicalPath(filepath.Clean(p))
		if workspace == "" {
			pf.outside = true
			return pf
		}
	case workspace == "":
		pf.inside = true
		return pf
	default:
		pf.abs = canonicalPath(filepath.Clean(filepath.Join(workspace, p)))
	}
	if rel, ok := relWithin(workspace, pf.abs); ok {
		pf.inside = true
		pf.rel = rel
	} else {
		pf.outside = true
	}
	return pf
}

// relWithin 判断 p 是否在 root 之下，并返回 / 分隔的相对路径。
func relWithin(root, p string) (string, bool) {
	if root == "" {
		return "", false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	if err != nil {
		return "", false
	}
	if rel == "." {
		return ".", true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// pathWithin 判断 p 是否在 root 之下（含 root 本身）。
func pathWithin(root, p string) bool {
	_, ok := relWithin(root, p)
	return ok
}

// guardHit 是内置防护命中的结果。
type guardHit struct {
	ID     string
	Reason string
}

// commandGuardPatterns 是对整条命令做正则匹配的防护；按顺序求值，首个命中生效。
var commandGuardPatterns = []struct {
	id     string
	re     *regexp.Regexp
	reason string
}{
	{"pipe-to-shell",
		regexp.MustCompile(`(?i)\b(curl|wget|fetch|iwr|invoke-webrequest|invoke-restmethod|downloadstring)\b[^|;&]*\|\s*(sudo\s+)?(bash|sh|zsh|ksh|dash|fish|python[0-9.]*|perl|ruby|node|pwsh|powershell|iex|invoke-expression)\b`),
		"piping remote content into an interpreter"},
	{"pipe-to-shell",
		regexp.MustCompile(`(?i)\b(bash|sh|zsh|ksh)\s+<\(\s*(curl|wget)\b`),
		"executing remote content via process substitution"},
	{"pipe-to-shell",
		regexp.MustCompile(`(?i)\b(iex|invoke-expression)\b[^;&|]*\b(new-object\s+net\.webclient|iwr|invoke-webrequest|invoke-restmethod|downloadstring)\b`),
		"executing remote content via Invoke-Expression"},
	{"redirect-raw-device",
		regexp.MustCompile(`>\s*/dev/(sd|hd|nvme|disk|rdisk|mmcblk|vd|xvd)`),
		"writing to a raw block device"},
	{"system-auth-files",
		regexp.MustCompile(`>>?\s*/etc/(passwd|shadow|sudoers|group)\b`),
		"overwriting system authentication files"},
	{"fork-bomb",
		regexp.MustCompile(`:\s*\(\s*\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;?\s*:`),
		"fork bomb"},
}

// kernelFileGuard 匹配对内核自身数据文件（令牌、密钥、数据库）的引用。
var kernelFileGuard = regexp.MustCompile(`(?i)\.gogosling[/\\](daemon\.json|daemon\.lock|secrets\.enc|gogosling\.db)`)

// kernelFileNames 是内核数据目录下不允许工具触碰的文件名。
var kernelFileNames = []string{"daemon.json", "daemon.lock", "secrets.enc", "gogosling.db", "gogosling.db-wal", "gogosling.db-shm"}

// wrapperCommands 是在识别真正命令前要跳过的前缀程序。
var wrapperCommands = map[string]bool{
	"sudo": true, "doas": true, "env": true, "nohup": true, "time": true, "nice": true, "ionice": true,
	"command": true, "exec": true, "busybox": true, "timeout": true, "stdbuf": true,
}

// criticalDirs 是递归删除或递归改权限时视为灾难性的目录（小写、/ 分隔、无尾部斜杠）。
var criticalDirs = map[string]bool{
	"/home": true, "/users": true, "/root": true, "/etc": true, "/usr": true, "/var": true, "/bin": true,
	"/sbin": true, "/lib": true, "/lib64": true, "/boot": true, "/opt": true, "/srv": true, "/system": true,
	"/library": true, "/applications": true, "/private": true, "/dev": true, "/proc": true, "/sys": true,
	"/mnt": true, "/media": true, "/volumes": true, "/usr/local": true,
	"c:/windows": true, "c:/users": true, "c:/program files": true, "c:/program files (x86)": true, "c:/programdata": true,
}

var driveRootRe = regexp.MustCompile(`^[A-Za-z]:$`)

// splitCommands 把一条 shell 命令粗略拆成若干简单命令（按 ; && || | 与换行）。
func splitCommands(cmd string) []string {
	r := strings.NewReplacer("&&", ";", "||", ";", "|", ";", "\n", ";", "\r", "")
	parts := strings.Split(r.Replace(cmd), ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// simpleCommand 是拆分后的一条命令：程序名与其余参数。
type simpleCommand struct {
	prog string
	args []string
}

// parseSimple 跳过包装程序与环境变量赋值，取出真正的程序名。
func parseSimple(s string) (simpleCommand, bool) {
	fields := strings.Fields(s)
	for len(fields) > 0 {
		tok := strings.Trim(fields[0], `"'`)
		low := strings.ToLower(progName(tok))
		switch {
		case wrapperCommands[low]:
			fields = fields[1:]
			// sudo -u user / env -i / nice -n 10 之类：跳过紧随的选项。
			for len(fields) > 0 && strings.HasPrefix(fields[0], "-") {
				opt := fields[0]
				fields = fields[1:]
				if (opt == "-u" || opt == "-g" || opt == "-n" || opt == "-C") && len(fields) > 0 {
					fields = fields[1:]
				}
			}
		case isAssignment(tok):
			fields = fields[1:]
		default:
			return simpleCommand{prog: low, args: fields[1:]}, true
		}
	}
	return simpleCommand{}, false
}

func isAssignment(tok string) bool {
	i := strings.IndexByte(tok, '=')
	if i <= 0 {
		return false
	}
	for _, c := range tok[:i] {
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// progName 取程序路径的最后一段并去掉 .exe 后缀。
func progName(tok string) string {
	base := path.Base(strings.ReplaceAll(tok, `\`, "/"))
	return strings.TrimSuffix(strings.ToLower(base), ".exe")
}

// isCriticalRoot 判断参数是否指向根目录、主目录、系统目录或上级目录逃逸。
func isCriticalRoot(arg, home string) bool {
	a := strings.Trim(strings.TrimSpace(arg), `"'`)
	if a == "" {
		return false
	}
	low := strings.ToLower(a)
	for _, pre := range []string{"${home}", "$home", "%userprofile%", "~"} {
		if strings.HasPrefix(low, pre) {
			rest := strings.Trim(low[len(pre):], `/\`)
			if rest == "" || rest == "*" {
				return true
			}
			if pre == "~" && !strings.HasPrefix(low, "~/") && !strings.HasPrefix(low, `~\`) {
				continue
			}
			// ~/Documents 这类具体子目录不算根目录。
			return false
		}
	}
	trimmed := strings.TrimRight(a, `/\`)
	if trimmed == "" {
		return true // "/"、"\"、"//"
	}
	if strings.HasSuffix(trimmed, "*") {
		inner := strings.TrimRight(strings.TrimSuffix(trimmed, "*"), `/\`)
		if inner == "" {
			return true // "/*"
		}
		trimmed = inner
	}
	if trimmed == ".." || strings.HasPrefix(trimmed, "../") || strings.HasPrefix(trimmed, `..\`) {
		return true
	}
	if driveRootRe.MatchString(trimmed) {
		return true
	}
	slash := strings.ToLower(filepath.ToSlash(trimmed))
	if criticalDirs[slash] {
		return true
	}
	if home != "" && strings.EqualFold(filepath.Clean(trimmed), filepath.Clean(home)) {
		return true
	}
	return false
}

// hasRecursiveFlag 识别 -r / -R / --recursive 以及合并写法（-rf、-fR）。
func hasRecursiveFlag(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--recursive" || a == "--no-preserve-root" {
			return true
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a[1:], "rR") {
			return true
		}
	}
	return false
}

// hasPowerShellRecurse 识别 -Recurse 及其缩写。
func hasPowerShellRecurse(args []string) bool {
	for _, a := range args {
		low := strings.ToLower(a)
		if low == "-r" || strings.HasPrefix(low, "-rec") {
			return true
		}
	}
	return false
}

func hasSwitch(args []string, sw string) bool {
	for _, a := range args {
		if strings.EqualFold(a, sw) {
			return true
		}
	}
	return false
}

// positional 返回不以 - 或 / 开头的参数（Windows 风格开关也视为选项）。
func positional(args []string, winStyle bool) []string {
	var out []string
	afterDashDash := false
	for _, a := range args {
		if a == "--" {
			afterDashDash = true
			continue
		}
		if !afterDashDash && (strings.HasPrefix(a, "-") || (winStyle && strings.HasPrefix(a, "/") && len(a) <= 3)) {
			continue
		}
		out = append(out, a)
	}
	return out
}

func anyCritical(args []string, home string) bool {
	for _, a := range args {
		if isCriticalRoot(a, home) {
			return true
		}
	}
	return false
}

func anyDevicePath(args []string) bool {
	for _, a := range args {
		low := strings.ToLower(strings.Trim(a, `"'`))
		if strings.HasPrefix(low, "/dev/") && low != "/dev/null" && low != "/dev/zero" && low != "/dev/stdout" && low != "/dev/stderr" {
			return true
		}
	}
	return false
}

// guardCommand 对命令做内置危险检查；home 用于识别主目录。未命中时返回 nil。
func guardCommand(cmd, home string) *guardHit {
	if strings.TrimSpace(cmd) == "" {
		return nil
	}
	for _, g := range commandGuardPatterns {
		if g.re.MatchString(cmd) {
			return &guardHit{ID: g.id, Reason: g.reason}
		}
	}
	if kernelFileGuard.MatchString(cmd) {
		return &guardHit{ID: "kernel-data-files", Reason: "touching gogosling's own token, secret or database files"}
	}
	for _, part := range splitCommands(cmd) {
		sc, ok := parseSimple(part)
		if !ok {
			continue
		}
		if hit := guardSimple(sc, home); hit != nil {
			return hit
		}
	}
	return nil
}

func guardSimple(sc simpleCommand, home string) *guardHit {
	switch sc.prog {
	case "rm":
		if hasSwitch(sc.args, "--no-preserve-root") {
			return &guardHit{ID: "rm-critical-path", Reason: "rm with --no-preserve-root"}
		}
		if (hasRecursiveFlag(sc.args) || hasPowerShellRecurse(sc.args)) && anyCritical(positional(sc.args, false), home) {
			return &guardHit{ID: "rm-critical-path", Reason: "recursive delete of a root, home or system directory"}
		}
	case "chmod", "chown", "chgrp":
		if hasRecursiveFlag(sc.args) && anyCritical(positional(sc.args, false), home) {
			return &guardHit{ID: "recursive-permission-change", Reason: "recursive permission change on a root, home or system directory"}
		}
	case "rd", "rmdir":
		if hasSwitch(sc.args, "/s") && anyCritical(positional(sc.args, true), home) {
			return &guardHit{ID: "rm-critical-path", Reason: "recursive delete of a drive root"}
		}
	case "del", "erase":
		if hasSwitch(sc.args, "/s") && anyCritical(positional(sc.args, true), home) {
			return &guardHit{ID: "rm-critical-path", Reason: "recursive delete of a drive root"}
		}
	case "remove-item", "ri":
		if hasPowerShellRecurse(sc.args) && anyCritical(positional(sc.args, false), home) {
			return &guardHit{ID: "rm-critical-path", Reason: "recursive delete of a root, home or system directory"}
		}
	case "mkfs", "mke2fs", "mkntfs", "mkswap", "newfs":
		return &guardHit{ID: "disk-format", Reason: "creating a filesystem"}
	case "dd":
		for _, a := range sc.args {
			low := strings.ToLower(strings.Trim(a, `"'`))
			if strings.HasPrefix(low, "of=/dev/") && low != "of=/dev/null" && low != "of=/dev/stdout" {
				return &guardHit{ID: "dd-raw-device", Reason: "dd writing to a raw device"}
			}
		}
	case "shutdown", "reboot", "halt", "poweroff", "stop-computer", "restart-computer":
		return &guardHit{ID: "system-power", Reason: "shutting down or rebooting the machine"}
	case "init":
		if len(sc.args) > 0 && (sc.args[0] == "0" || sc.args[0] == "6") {
			return &guardHit{ID: "system-power", Reason: "changing runlevel to halt or reboot"}
		}
	case "systemctl":
		for _, a := range sc.args {
			switch strings.ToLower(a) {
			case "poweroff", "reboot", "halt", "kexec":
				return &guardHit{ID: "system-power", Reason: "shutting down or rebooting the machine"}
			}
		}
	case "format":
		pos := positional(sc.args, true)
		if len(pos) > 0 && driveRootRe.MatchString(strings.TrimRight(pos[0], `\/`)) {
			return &guardHit{ID: "disk-format", Reason: "formatting a drive"}
		}
	case "diskpart":
		return &guardHit{ID: "disk-format", Reason: "disk partitioning tool"}
	case "fdisk", "sfdisk", "gdisk", "sgdisk", "parted", "wipefs":
		if anyDevicePath(sc.args) {
			return &guardHit{ID: "disk-format", Reason: "partitioning or wiping a device"}
		}
	case "diskutil":
		for _, a := range sc.args {
			switch strings.ToLower(a) {
			case "erasedisk", "erasevolume", "zerodisk", "secureerase", "partitiondisk", "reformat", "randomdisk":
				return &guardHit{ID: "disk-format", Reason: "erasing a disk or volume"}
			}
		}
	}
	if strings.HasPrefix(sc.prog, "mkfs.") {
		return &guardHit{ID: "disk-format", Reason: "creating a filesystem"}
	}
	return nil
}

// guardPaths 检查路径是否触碰内核数据目录中的敏感文件：
// 数据目录内、且不在当前工作区内的路径一律拒绝（数据库、令牌、密钥、其他智能体的工作区）。
//
// 每条路径按两种形式各判一次，任一命中即拒绝：
//   - 词法形式（Windows 上已去掉设备路径前缀并展开短名，见 classifyPath）；
//   - 已存在部分解析符号链接后的真实形式（resolveExisting），数据目录与工作区也取真实形式。
//
// "在数据目录内"总是不区分大小写地判断（硬拒绝，宁严勿松）；"在本工作区内"的豁免只在
// 文件系统不区分大小写时（foldFS，即 darwin / windows）才不区分大小写，否则大小写不同的兄弟目录
// （例如其他智能体的工作区 A 与 a）会被错误豁免。
func guardPaths(f facts, dataDir, workspace string, foldFS bool) *guardHit {
	if dataDir == "" {
		return nil
	}
	dataLex := canonicalPath(filepath.Clean(dataDir))
	dataReal := resolveExisting(dataLex)
	wsLex, wsReal := "", ""
	if workspace != "" {
		wsLex = canonicalPath(filepath.Clean(workspace))
		wsReal = resolveExisting(wsLex)
	}
	for _, p := range f.paths {
		if p.abs == "" {
			continue
		}
		if inKernelDir(p.abs, dataLex, wsLex, foldFS) || inKernelDir(resolveExisting(p.abs), dataReal, wsReal, foldFS) {
			return &guardHit{ID: "kernel-data-dir", Reason: "touching gogosling's data directory outside the agent workspace"}
		}
	}
	return nil
}

// inKernelDir 判断 p 在 dataDir 内且不在 workspace 内；三者应是同一种形式（都是词法形式或都是真实形式）。
func inKernelDir(p, dataDir, workspace string, foldFS bool) bool {
	if !pathWithinFold(dataDir, p, true) {
		return false
	}
	return workspace == "" || !pathWithinFold(workspace, p, foldFS)
}
