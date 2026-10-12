//go:build linux

package sandbox

import (
	"encoding/binary"
	"runtime"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 内核 audit 架构值与 syscall 编号来自 Linux UAPI / Go syscall 的各架构表。
// 不允许切换到兼容 ABI 绕过策略；尚未列出的架构明确拒绝构造隔离 Runner。
type bwrapSeccompArch struct {
	audit      uint32
	socket     uint32
	socketpair uint32
	socketcall uint32
	bigEndian  bool
	x32        bool
}

var bwrapSeccompArchitectures = map[string]bwrapSeccompArch{
	"amd64":   {audit: 0xc000003e, socket: 41, socketpair: 53, x32: true},
	"386":     {audit: 0x40000003, socket: 359, socketpair: 360, socketcall: 102},
	"arm":     {audit: 0x40000028, socket: 281, socketpair: 288, socketcall: 102},
	"arm64":   {audit: 0xc00000b7, socket: 198, socketpair: 199},
	"riscv64": {audit: 0xc00000f3, socket: 198, socketpair: 199},
	"loong64": {audit: 0xc0000102, socket: 198, socketpair: 199},
	"ppc64":   {audit: 0x80000015, socket: 326, socketpair: 333, socketcall: 102, bigEndian: true},
	"ppc64le": {audit: 0xc0000015, socket: 326, socketpair: 333, socketcall: 102},
	"s390x":   {audit: 0x80000016, socket: 359, socketpair: 360, socketcall: 102, bigEndian: true},
}

type bwrapBPF struct {
	code uint16
	jt   uint8
	jf   uint8
	k    uint32
}

const (
	bwrapBPFLoad      = 0x20
	bwrapBPFEqual     = 0x15
	bwrapBPFSet       = 0x45
	bwrapBPFAnd       = 0x54
	bwrapBPFReturn    = 0x06
	bwrapSeccompAllow = 0x7fff0000
	bwrapSeccompDeny  = 0x00050001 // SECCOMP_RET_ERRNO | EPERM
	bwrapSeccompKill  = 0x80000000
)

// bwrapSocketFilter 拒绝宿主路径 Unix socket，避免 /run/docker.sock 等绕过只读文件边界。
// 已连接的匿名 Unix STREAM socketpair 仍允许，供 Python asyncio 等自唤醒机制使用；
// 可重新连接的 DGRAM socketpair 不允许。该约束在 AllowNetwork=true 时也生效。
func bwrapSocketFilter() ([]byte, error) {
	arch, ok := bwrapSeccompArchitectures[runtime.GOARCH]
	if !ok {
		return nil, model.NewError(model.CodeUnavailable, bwrapText("socket_arch_unsupported", map[string]any{"arch": runtime.GOARCH}))
	}
	instructions := bwrapSocketInstructions(arch)
	data := make([]byte, len(instructions)*8)
	for i, instruction := range instructions {
		buffer := data[i*8:]
		binary.NativeEndian.PutUint16(buffer, instruction.code)
		buffer[2], buffer[3] = instruction.jt, instruction.jf
		binary.NativeEndian.PutUint32(buffer[4:], instruction.k)
	}
	return data, nil
}

func bwrapSocketInstructions(arch bwrapSeccompArch) []bwrapBPF {
	code := []bwrapBPF{
		{code: bwrapBPFLoad, k: 4}, // seccomp_data.arch
		{code: bwrapBPFEqual, k: arch.audit, jt: 1},
		{code: bwrapBPFReturn, k: bwrapSeccompKill},
		{code: bwrapBPFLoad, k: 0}, // seccomp_data.nr
	}
	if arch.x32 {
		code = append(code, bwrapBPF{code: bwrapBPFSet, k: 0x40000000, jf: 1}, bwrapBPF{code: bwrapBPFReturn, k: bwrapSeccompDeny})
	}
	// io_uring 的异步 SOCKET/CONNECT 操作不会再触发对应 syscall 过滤，必须关闭这些入口。
	// 已支持架构的 Linux UAPI 统一使用 425/426/427（golang.org/x/sys/unix 生成表亦一致）。
	for _, number := range []uint32{425, 426, 427} {
		code = append(code, bwrapBPF{code: bwrapBPFEqual, k: number, jf: 1}, bwrapBPF{code: bwrapBPFReturn, k: bwrapSeccompDeny})
	}
	socketJump := len(code)
	code = append(code, bwrapBPF{code: bwrapBPFEqual, k: arch.socket})
	pairJump := len(code)
	code = append(code, bwrapBPF{code: bwrapBPFEqual, k: arch.socketpair})
	if arch.socketcall != 0 {
		// 旧 socketcall 用指针封装参数，BPF 无法安全检查指针内容；因此拒绝旧调用入口。
		code = append(code, bwrapBPF{code: bwrapBPFEqual, k: arch.socketcall, jf: 1}, bwrapBPF{code: bwrapBPFReturn, k: bwrapSeccompDeny})
	}
	code = append(code, bwrapBPF{code: bwrapBPFReturn, k: bwrapSeccompAllow})
	domainOffset := uint32(16)
	if arch.bigEndian {
		domainOffset += 4
	}
	socketStart := len(code)
	code = append(code,
		bwrapBPF{code: bwrapBPFLoad, k: domainOffset},
		bwrapBPF{code: bwrapBPFEqual, k: 1, jf: 1}, // AF_UNIX
		bwrapBPF{code: bwrapBPFReturn, k: bwrapSeccompDeny},
		bwrapBPF{code: bwrapBPFReturn, k: bwrapSeccompAllow},
	)
	pairStart := len(code)
	code = append(code,
		bwrapBPF{code: bwrapBPFLoad, k: domainOffset},
		bwrapBPF{code: bwrapBPFEqual, k: 1, jf: 4},
		bwrapBPF{code: bwrapBPFLoad, k: domainOffset + 8}, // args[1]，socket 类型
		bwrapBPF{code: bwrapBPFAnd, k: 0xf},               // 忽略 CLOEXEC/NONBLOCK 标志
		bwrapBPF{code: bwrapBPFEqual, k: 1, jt: 1},        // SOCK_STREAM
		bwrapBPF{code: bwrapBPFReturn, k: bwrapSeccompDeny},
		bwrapBPF{code: bwrapBPFReturn, k: bwrapSeccompAllow},
	)
	code[socketJump].jt = uint8(socketStart - socketJump - 1)
	code[pairJump].jt = uint8(pairStart - pairJump - 1)
	return code
}
