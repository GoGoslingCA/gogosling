// Command gogosling 是 gogosling 的命令行客户端。
//
// 它通过本地 API（internal/client）与守护进程 gogoslingd 通信，自身不持有业务状态，
// 也不读写数据目录与数据库——唯一读取的是 daemon.json（发现守护进程的端点与令牌）。
// 守护进程是唯一的状态持有者，CLI 写入立即对正在运行的守护进程生效。
//
// daemon stop 是唯一会改动数据目录的命令：结束进程之前先用 daemon.json 里的令牌访问 /api/v1/version
// 并比对 started_at，确认句柄属于正在运行的那个实例；确认失败说明句柄是残留文件，只删除它、不发信号
// （防止 pid 被无关进程复用时误杀）。确认之后优先经 POST /api/v1/system/shutdown 优雅关闭，
// 10 秒内没有退出才发 SIGTERM（Windows 直接 taskkill /F），并在进程退出后删除残留的 daemon.json。
// 会话以 origin=cli 创建；Windows 传统控制台先开启 VT 处理，开启失败就不输出 ANSI 样式。
//
// 退出码：0 成功；1 失败；2 参数错误；3 守护进程未运行；130 对话中被 Ctrl-C 中止。
package main

import (
	"context"
	"os"
)

func main() {
	os.Exit(newCLI().run(context.Background(), os.Args[1:]))
}
