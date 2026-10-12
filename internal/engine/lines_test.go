package engine

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func readAll(t *testing.T, lr *lineReader) ([]string, error) {
	t.Helper()
	var out []string
	for {
		line, err := lr.ReadLine()
		if err != nil {
			return out, err
		}
		out = append(out, string(line))
	}
}

func TestLineReaderSplitsAndTrims(t *testing.T) {
	lr := newLineReader(strings.NewReader("a\r\nb\n\nc"), 1024, false)
	lines, err := readAll(t, lr)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("结束错误 = %v，期望 EOF", err)
	}
	want := []string{"a", "b", "", "c"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Errorf("行 = %q，期望 %q", lines, want)
	}
}

func TestLineReaderRejectsOversizedLine(t *testing.T) {
	lr := newLineReader(strings.NewReader(strings.Repeat("x", 100)+"\nok\n"), 50, false)
	if _, err := lr.ReadLine(); !errors.Is(err, errLineTooLong) {
		t.Fatalf("错误 = %v，期望 errLineTooLong", err)
	}
}

func TestLineReaderTruncatesWhenAsked(t *testing.T) {
	lr := newLineReader(strings.NewReader(strings.Repeat("x", 100)+"\nok\n"), 50, true)
	line, err := lr.ReadLine()
	if err != nil || len(line) != 50 {
		t.Fatalf("截断行长度 = %d (%v)，期望 50", len(line), err)
	}
	line, err = lr.ReadLine()
	if err != nil || string(line) != "ok" {
		t.Fatalf("截断后下一行 = %q (%v)，期望 ok", line, err)
	}
}

func TestLineReaderHandlesLinesLongerThanBuffer(t *testing.T) {
	long := strings.Repeat("y", lineReaderBufSize*3)
	lr := newLineReader(strings.NewReader(long+"\nz\n"), lineReaderBufSize*4, false)
	line, err := lr.ReadLine()
	if err != nil || len(line) != len(long) {
		t.Fatalf("长行长度 = %d (%v)，期望 %d", len(line), err, len(long))
	}
	line, err = lr.ReadLine()
	if err != nil || string(line) != "z" {
		t.Fatalf("长行之后 = %q (%v)，期望 z", line, err)
	}
}
