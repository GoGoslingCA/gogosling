//go:build linux

package sandbox

import (
	"encoding/binary"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

// installTestBwrapFilter 只在一次性测试子进程里安装过滤器，绝不影响运行其他测试的进程。
func installTestBwrapFilter(t *testing.T) {
	t.Helper()
	data, err := bwrapSocketFilter()
	if err != nil {
		t.Fatal(err)
	}
	filters := make([]syscall.SockFilter, len(data)/8)
	for i := range filters {
		chunk := data[i*8:]
		filters[i] = syscall.SockFilter{Code: binary.NativeEndian.Uint16(chunk), Jt: chunk[2], Jf: chunk[3], K: binary.NativeEndian.Uint32(chunk[4:])}
	}
	program := syscall.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	if _, _, err := syscall.Syscall6(syscall.SYS_PRCTL, 38, 1, 0, 0, 0, 0); err != 0 { // PR_SET_NO_NEW_PRIVS
		t.Fatalf("测试子进程无法设置 no_new_privs：%v", err)
	}
	if _, _, err := syscall.Syscall6(syscall.SYS_PRCTL, 22, 2, uintptr(unsafe.Pointer(&program)), 0, 0, 0); err != 0 { // PR_SET_SECCOMP / FILTER
		t.Fatalf("测试子进程无法安装 seccomp：%v", err)
	}
	runtime.KeepAlive(filters)
}

func TestBwrapSeccompKernelHelper(t *testing.T) {
	if os.Getenv("GOGOSLING_BWRAP_SECCOMP_TEST") != "child" {
		return
	}
	// PR_SET_SECCOMP 作用于当前 OS 线程，安装和全部断言必须留在同一线程。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	installTestBwrapFilter(t)
	if fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0); err != syscall.EPERM {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		t.Fatalf("路径 Unix socket 创建必须被拒绝：%v", err)
	}
	if pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0); err != syscall.EPERM {
		if err == nil {
			_ = syscall.Close(pair[0])
			_ = syscall.Close(pair[1])
		}
		t.Fatalf("可重新连接的 DGRAM pair 必须被拒绝：%v", err)
	}
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC|syscall.SOCK_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("匿名 STREAM pair 应允许，供事件循环唤醒使用：%v", err)
	}
	_ = syscall.Close(pair[0])
	_ = syscall.Close(pair[1])
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("IP socket 由网络 namespace 控制，过滤器不能误禁：%v", err)
	}
	_ = syscall.Close(fd)
	for _, number := range []uintptr{425, 426, 427} {
		if _, _, err := syscall.Syscall6(number, 0, 0, 0, 0, 0, 0); err != syscall.EPERM {
			t.Fatalf("io_uring syscall %d 必须被拒绝：%v", number, err)
		}
	}
}

func TestBwrapSeccompKernelPolicy(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-test.run=^TestBwrapSeccompKernelHelper$")
	command.Env = buildEnv(map[string]string{"GOGOSLING_BWRAP_SECCOMP_TEST": "child"}, os.LookupEnv)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("真实内核过滤器验证失败：%v\n%s", err, output)
	}
}
