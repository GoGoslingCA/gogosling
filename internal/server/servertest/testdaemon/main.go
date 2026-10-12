// Command testdaemon 是端到端测试用的迷你守护进程：用 servertest 装配真实的 server，
// 监听 127.0.0.1 的随机端口，写入 <data>/daemon.json（与 gogoslingd 相同的发现方式），
// 在标准输出打印一行 READY；标准输入关闭、收到中断信号或 POST /api/v1/system/shutdown 时优雅退出。
//
// internal/client 的测试通过 go build 构建并启动它：传输层的两个包按依赖规则不能互相导入，
// 跨进程驱动同时也覆盖了真实 TCP、daemon.json 发现与优雅关闭。不要在生产环境使用。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/identity"
	"github.com/GoGoslingCA/gogosling/internal/server"
	"github.com/GoGoslingCA/gogosling/internal/server/servertest"
	"github.com/GoGoslingCA/gogosling/internal/version"
)

func main() {
	os.Exit(run())
}

func run() int {
	dataDir := flag.String("data", "", "数据目录（必填，应为空的临时目录）")
	busCapacity := flag.Int("bus", servertest.DefaultBusCapacity, "事件总线回放缓冲容量")
	verbose := flag.Bool("v", false, "把日志写到标准错误")
	listen := flag.String("listen", "127.0.0.1:0", "监听地址（端口为 0 时随机；重启测试可复用上一次的端口）")
	flag.Parse()
	if *dataDir == "" {
		fmt.Fprintln(os.Stderr, "testdaemon: 缺少 -data")
		return 2
	}

	logger := slog.New(slog.DiscardHandler)
	if *verbose {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	// 父进程关闭标准输入（或自身退出）即视为停止信号，三个平台行为一致。
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		stop()
	}()

	env, err := servertest.NewEnv(ctx, *dataDir, servertest.Options{BusCapacity: *busCapacity, Logger: logger})
	if err != nil {
		fmt.Fprintln(os.Stderr, "testdaemon: 装配失败:", err)
		return 1
	}
	defer func() { _ = env.Close() }()

	startedAt := time.Now().UTC()
	srv, err := server.New(env.Deps, server.Options{
		ListenAddr: *listen,
		StartedAt:  startedAt.Format(time.RFC3339),
		// 与生产一致：POST /api/v1/system/shutdown 经鉴权后触发与收到中断信号相同的优雅关闭。
		Shutdown: stop,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "testdaemon: 创建服务失败:", err)
		return 1
	}
	addr, err := srv.Listen(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "testdaemon: 监听失败:", err)
		return 1
	}
	handle := identity.NewHandleFile(*dataDir)
	if err := handle.Write(ctx, identity.Handle{
		PID:       os.Getpid(),
		Endpoint:  "http://" + addr.String(),
		Token:     env.Token,
		Version:   version.Version,
		StartedAt: startedAt,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "testdaemon: 写入 daemon.json 失败:", err)
		return 1
	}
	defer func() { _ = handle.Remove(context.Background()) }()

	fmt.Println("READY")
	if err := srv.Serve(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "testdaemon: 服务异常结束:", err)
		return 1
	}
	return 0
}
