package engine

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
)

// process 是一个已启动的工作进程及其三条管道。
//
// 管道用 os.Pipe 自建并以 *os.File 交给 exec，这样 cmd.Wait 不会替我们关闭读端，
// 读循环可以在进程退出后把尾部消息读完；进程退出由独立 goroutine 等待并通过 exited 通知。
type process struct {
	cmd    *exec.Cmd
	stdin  *os.File
	stdout *os.File
	stderr *os.File

	exited  chan struct{}
	exitErr error

	killOnce  sync.Once
	closeOnce sync.Once
}

// spawn 启动命令。env 是完整的环境变量列表（不继承守护进程环境）。
func spawn(command Command, env []string) (*process, error) {
	if command.Path == "" {
		return nil, errors.New("engine: 工作进程命令为空")
	}
	cmd := exec.Command(command.Path, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env = env
	configureCmd(cmd)

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		closeAll(stdinR, stdinW)
		return nil, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		closeAll(stdinR, stdinW, stdoutR, stdoutW)
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinR, stdoutW, stderrW

	if err := cmd.Start(); err != nil {
		closeAll(stdinR, stdinW, stdoutR, stdoutW, stderrR, stderrW)
		return nil, err
	}
	// 子进程已持有自己那一端，父进程关闭它们，否则读端永远等不到 EOF。
	closeAll(stdinR, stdoutW, stderrW)

	p := &process{cmd: cmd, stdin: stdinW, stdout: stdoutR, stderr: stderrR, exited: make(chan struct{})}
	go func() {
		p.exitErr = cmd.Wait()
		close(p.exited)
	}()
	return p, nil
}

func (p *process) pid() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// kill 强制结束进程（POSIX 上连同它的进程组）；可重复调用。
func (p *process) kill() {
	p.killOnce.Do(func() {
		if p.cmd.Process != nil {
			_ = killTree(p.cmd.Process)
		}
	})
}

// closePipes 关闭父进程持有的三条管道端；可重复调用。关闭读端会让仍在阻塞读取的 goroutine 返回错误。
func (p *process) closePipes() {
	p.closeOnce.Do(func() {
		closeAll(p.stdin, p.stdout, p.stderr)
	})
}

// exitDescription 描述退出方式，仅在 exited 关闭后有意义。
func (p *process) exitDescription() string {
	select {
	case <-p.exited:
	default:
		return "仍在运行"
	}
	if p.exitErr == nil {
		return "exit status 0"
	}
	var ee *exec.ExitError
	if errors.As(p.exitErr, &ee) {
		return ee.String()
	}
	return fmt.Sprintf("wait 失败: %v", p.exitErr)
}

func closeAll(files ...*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}
