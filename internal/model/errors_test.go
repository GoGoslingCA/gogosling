package model

import (
	"errors"
	"fmt"
	"testing"
)

func TestCodeOfWrappedError(t *testing.T) {
	base := NotFound("agent", "01X")
	wrapped := fmt.Errorf("加载失败: %w", base)

	if got := CodeOf(wrapped); got != CodeNotFound {
		t.Errorf("CodeOf = %q，期望 %q", got, CodeNotFound)
	}
	if !errors.Is(wrapped, &Error{Code: CodeNotFound}) {
		t.Error("errors.Is 按错误码匹配失败")
	}
	if !IsCode(wrapped, CodeNotFound) {
		t.Error("IsCode 返回 false")
	}
}

func TestCodeOfPlainErrorIsInternal(t *testing.T) {
	if got := CodeOf(errors.New("boom")); got != CodeInternal {
		t.Errorf("CodeOf = %q，期望 %q", got, CodeInternal)
	}
	if IsCode(nil, CodeInternal) {
		t.Error("IsCode(nil) 应为 false")
	}
}

func TestWrapNil(t *testing.T) {
	if Wrap(CodeInternal, "x", nil) != nil {
		t.Error("Wrap(nil) 应返回 nil")
	}
}
