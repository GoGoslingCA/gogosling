package app

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/sandbox"
)

type previewConversionRecorder struct {
	sandbox.Runner
	t *testing.T
}

func (r previewConversionRecorder) Run(ctx context.Context, s sandbox.Spec) (sandbox.Result, error) {
	v, e := r.Runner.Run(ctx, s)
	if e != nil || v.ExitCode != 0 {
		r.t.Logf("转换失败诊断 exit=%d timeout=%v stderr=%s", v.ExitCode, v.TimedOut, v.Stderr)
	}
	return v, e
}
func Test本地Office转换可选验收(t *testing.T) {
	binary := os.Getenv("GOGOSLING_TEST_OFFICE_BIN")
	if binary == "" {
		t.Skip("仅在显式提供本地转换器时运行，不作为默认依赖")
	}
	converter := newPreviewConverter(binary)
	if converter == nil {
		t.Fatal("本机没有受限转换能力")
	}
	c := converter.(*officeConverter)
	c.runner = previewConversionRecorder{c.runner, t}
	started := time.Now()
	data, err := c.Convert(context.Background(), "fixture.rtf", strings.NewReader(`{\rtf1\ansi\deff0 {\fonttbl {\f0 Helvetica;}}\f0\fs28 Local preview\par Read-only conversion.}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "%PDF-") {
		t.Fatal("转换必须返回真实PDF")
	}
	t.Logf("真实转换通过 bytes=%d elapsed=%s", len(data), time.Since(started))
}

type conversionCancelRunner struct{ started chan string }

func (r conversionCancelRunner) Run(ctx context.Context, s sandbox.Spec) (sandbox.Result, error) {
	r.started <- s.Dir
	<-ctx.Done()
	return sandbox.Result{}, ctx.Err()
}
func Test取消转换会释放单并发槽和临时材料(t *testing.T) {
	runner := conversionCancelRunner{started: make(chan string, 1)}
	c := &officeConverter{binary: "/test/soffice", gate: make(chan struct{}, 1), runner: runner}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := c.Convert(ctx, "file.rtf", strings.NewReader("fixture")); done <- e }()
	var dir string
	select {
	case dir = <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("转换未开始")
	}
	if _, e := os.Stat(dir); e != nil {
		t.Fatal("转换期间应有临时工作目录")
	}
	cancel()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("取消应返回取消原因")
		}
	case <-time.After(time.Second):
		t.Fatal("取消没有释放资源")
	}
	if _, e := os.Stat(dir); !os.IsNotExist(e) {
		t.Fatal("临时材料未清理")
	}
	if len(c.gate) != 0 {
		t.Fatal("转换并发槽未释放")
	}
}
