package engine

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// errLineTooLong 表示单行超过上限；对协议流而言这是致命错误。
var errLineTooLong = errors.New("engine: 单行超过长度上限")

// lineReaderBufSize 是底层缓冲区大小；大于默认的 4 KiB 以减少长消息的拷贝次数。
const lineReaderBufSize = 64 << 10

// lineReader 按 '\n' 切分字节流，去掉行尾的 "\r\n" 或 "\n"。
//
// 超过 max 字节的行：truncate 为 false 时返回 errLineTooLong（协议流），
// 为 true 时保留前 max 字节、丢弃其余部分后继续（stderr 日志流）。
type lineReader struct {
	r        *bufio.Reader
	max      int
	truncate bool
}

func newLineReader(r io.Reader, max int, truncate bool) *lineReader {
	if max <= 0 {
		max = lineReaderBufSize
	}
	return &lineReader{r: bufio.NewReaderSize(r, lineReaderBufSize), max: max, truncate: truncate}
}

// ReadLine 返回下一行（不含换行符）。流结束时若还有未以换行结尾的内容则先返回它，再返回 io.EOF。
func (l *lineReader) ReadLine() ([]byte, error) {
	var line []byte
	overflow := false
	for {
		chunk, err := l.r.ReadSlice('\n')
		if !overflow {
			line = append(line, chunk...)
			if len(line) > l.max {
				if !l.truncate {
					return nil, errLineTooLong
				}
				line = line[:l.max]
				overflow = true
			}
		}
		switch {
		case err == nil:
			return trimEOL(line), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(line) > 0 {
				return trimEOL(line), nil
			}
			return nil, io.EOF
		default:
			return nil, err
		}
	}
}

func trimEOL(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte{'\n'})
	return bytes.TrimSuffix(b, []byte{'\r'})
}
