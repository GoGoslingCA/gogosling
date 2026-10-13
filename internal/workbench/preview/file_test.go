package preview

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func Test已打开文件与界面共享解析槽取消后保留调用方句柄(t *testing.T) {
	name := filepath.Join(t.TempDir(), "note.shared")
	if err := os.WriteFile(name, []byte("text"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	entered := make(chan struct{})
	var once sync.Once
	svc := New(fileSource{path: name}, nil, Format{ID: "test", Extensions: []string{".shared"}, Kind: "text", Parser: ParseFunc(func(ctx context.Context, _ Input) (Document, error) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return Document{}, ctx.Err()
	})})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := svc.PreviewFile(ctx, f); done <- err }()
	<-entered
	if _, err := svc.Preview(context.Background(), "agent", "note.shared"); err == nil {
		t.Fatal("工具解析期间界面不得另开解析槽")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("取消必须返回错误")
		}
	case <-time.After(time.Second):
		t.Fatal("取消后应及时释放解析槽")
	}
	if _, err := f.Stat(); err != nil {
		t.Fatalf("调用方仍拥有文件句柄：%v", err)
	}
	if len(svc.gate) != 0 {
		t.Fatal("取消后不得遗留解析槽占用")
	}
}
