package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// EnvWorkerCmd 是覆盖工作进程命令的环境变量（整条命令，按 shell 词法拆分）。
const EnvWorkerCmd = "GOGOSLING_WORKER_CMD"

// workerModule 是随安装包分发的运行环境里工作进程的 Python 模块名（python -m <module>）。
const workerModule = "gogosling_worker"

// Command 描述如何启动工作进程。
type Command struct {
	// Path 是可执行文件；Args 是参数（不含 Path 本身）。
	Path string
	Args []string
	// Dir 是工作目录，为空时继承守护进程的。
	Dir string
	// Env 是在环境变量白名单与协议变量之后追加的显式变量，例如打包运行环境需要的 PYTHONHOME。
	// 这里的值会原样交给子进程，因此不要放密钥。
	Env map[string]string
}

// Locate 按协议 §6 的顺序找到工作进程命令：
//  1. 环境变量 GOGOSLING_WORKER_CMD；
//  2. 随安装包分发的运行环境（<可执行文件所在目录>/../worker/bin/python，macOS 为 ../Resources/worker/bin/python）；
//  3. 都没有则返回 CodeUnavailable；守护进程应照常启动并让 engine 报告 unavailable。
func Locate() (Command, error) {
	if raw := strings.TrimSpace(os.Getenv(EnvWorkerCmd)); raw != "" {
		words, err := splitCommand(raw, runtime.GOOS != "windows")
		if err != nil {
			return Command{}, model.Wrap(model.CodeInvalid, EnvWorkerCmd+" 无法解析", err)
		}
		if len(words) == 0 {
			return Command{}, model.NewError(model.CodeInvalid, EnvWorkerCmd+" 为空")
		}
		return Command{Path: words[0], Args: words[1:]}, nil
	}

	exe, err := os.Executable()
	if err != nil {
		return Command{}, model.Wrap(model.CodeUnavailable, "无法确定守护进程可执行文件位置", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if cmd, ok := locateBundled(filepath.Dir(exe), runtime.GOOS); ok {
		return cmd, nil
	}
	return Command{}, model.NewError(model.CodeUnavailable, "未找到工作进程运行环境")
}

// bundledCandidates 列出随安装包分发的 Python 解释器可能的位置，按优先级排序。
func bundledCandidates(exeDir, goos string) []string {
	switch goos {
	case "windows":
		return []string{
			filepath.Join(exeDir, "..", "worker", "Scripts", "python.exe"),
			filepath.Join(exeDir, "..", "worker", "bin", "python.exe"),
		}
	case "darwin":
		return []string{
			filepath.Join(exeDir, "..", "Resources", "worker", "bin", "python"),
			filepath.Join(exeDir, "..", "worker", "bin", "python"),
		}
	default:
		return []string{filepath.Join(exeDir, "..", "worker", "bin", "python")}
	}
}

func locateBundled(exeDir, goos string) (Command, bool) {
	for _, p := range bundledCandidates(exeDir, goos) {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return Command{Path: filepath.Clean(p), Args: []string{"-m", workerModule}}, true
		}
	}
	return Command{}, false
}

// lexState 是 splitCommand 的词法状态。
type lexState int

const (
	lexPlain  lexState = iota // 引号之外
	lexSingle                 // 单引号内
	lexDouble                 // 双引号内
)

// splitCommand 按 POSIX shell 的引号与转义规则把一条命令拆成参数（只做拆分，不做变量展开、
// 命令替换与通配符展开）：
//   - 引号之外：空白分隔参数；反斜杠使下一个字符失去特殊含义，反斜杠-换行是续行，整体删除；
//   - 单引号内：一切原样，直到下一个单引号，反斜杠也是普通字符；
//   - 双引号内：反斜杠只在后面是 $ ` " \ 或换行时转义（反斜杠-换行删除），否则反斜杠原样保留，
//     例如 "/opt/a\b" 得到 /opt/a\b；
//   - 相邻的引号段与普通字符拼成同一个参数；单独出现的一对空引号产生一个空参数。
//
// backslashEscapes 为 false 时（Windows）反斜杠一律是普通字符，以免破坏路径；引号规则不变。
func splitCommand(s string, backslashEscapes bool) ([]string, error) {
	var (
		words  []string
		cur    strings.Builder
		inWord bool
		state  = lexPlain
	)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch state {
		case lexSingle:
			if r == '\'' {
				state = lexPlain
			} else {
				cur.WriteRune(r)
			}
		case lexDouble:
			switch {
			case r == '"':
				state = lexPlain
			case r == '\\' && backslashEscapes && i+1 < len(runes):
				switch next := runes[i+1]; next {
				case '$', '`', '"', '\\':
					cur.WriteRune(next)
					i++
				case '\n':
					i++
				default:
					cur.WriteRune(r)
				}
			default:
				// 包括输入末尾的反斜杠：随后会因为引号未闭合而报错。
				cur.WriteRune(r)
			}
		default:
			switch {
			case r == '\\' && backslashEscapes:
				if i+1 >= len(runes) {
					return nil, model.NewError(model.CodeInvalid, "命令以未完成的转义结尾")
				}
				i++
				if runes[i] == '\n' {
					// 续行：不产生任何字符，也不开始新参数。
					continue
				}
				cur.WriteRune(runes[i])
				inWord = true
			case r == '\'':
				state = lexSingle
				inWord = true
			case r == '"':
				state = lexDouble
				inWord = true
			case r == ' ' || r == '\t' || r == '\n' || r == '\r':
				if inWord {
					words = append(words, cur.String())
					cur.Reset()
					inWord = false
				}
			default:
				cur.WriteRune(r)
				inWord = true
			}
		}
	}
	if state != lexPlain {
		return nil, model.NewError(model.CodeInvalid, "命令中的引号未闭合")
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}
