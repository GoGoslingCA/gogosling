package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
)

// lineReader 在独立 goroutine 中按行读取标准输入，让对话循环可以同时等待输入、事件与 Ctrl-C。
// 整个进程只创建一个，所有读取（REPL、审批询问、密钥输入）都经由它，避免多个读者抢同一个 stdin。
type lineReader struct {
	ch   chan lineResult
	once sync.Once
	r    io.Reader
}

type lineResult struct {
	line string
	err  error
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{ch: make(chan lineResult), r: r}
}

func (l *lineReader) start() {
	l.once.Do(func() {
		go func() {
			br := bufio.NewReader(l.r)
			for {
				line, err := br.ReadString('\n')
				if line != "" {
					l.ch <- lineResult{line: strings.TrimRight(line, "\r\n")}
				}
				if err != nil {
					l.ch <- lineResult{err: err}
					close(l.ch)
					return
				}
			}
		}()
	})
}

// next 返回下一行；输入结束返回 io.EOF，ctx 结束返回 ctx.Err()。
func (l *lineReader) next(ctx context.Context) (string, error) {
	l.start()
	select {
	case res, ok := <-l.ch:
		if !ok {
			return "", io.EOF
		}
		return res.line, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// readLine 读取一行用户输入。
func (c *cli) readLine(ctx context.Context) (string, error) {
	if c.lines == nil {
		c.lines = newLineReader(c.stdin)
	}
	return c.lines.next(ctx)
}

// readSecret 提示并读取一行不回显的输入（API Key、通道凭据）。标准输入不是终端时（管道、测试）
// 直接读一行。返回值去掉首尾空白；输入结束视为空。
func (c *cli) readSecret(ctx context.Context, prompt string) (string, error) {
	c.printf("%s", prompt)
	restore := func() {}
	if c.stdinTTY {
		restore = disableEcho()
	}
	line, err := c.readLine(ctx)
	restore()
	if c.stdinTTY {
		c.println("")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// notifyInterrupts 订阅 Ctrl-C；返回的函数取消订阅。
func notifyInterrupts() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt)
	return ch, func() { signal.Stop(ch) }
}
