package model

import (
	"crypto/rand"
	"time"
)

// crockford 是 ULID 使用的 Crockford Base32 字母表（不含 I、L、O、U）。
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID 生成一个 26 字符的 ULID：前 10 位编码毫秒时间戳，后 16 位为随机数。
//
// 选择 ULID 而不是 UUID，是为了让主键天然按创建时间排序，便于分页与日志对照。
func NewID() string {
	return newIDAt(time.Now())
}

func newIDAt(t time.Time) string {
	var raw [16]byte
	ms := uint64(t.UnixMilli())
	raw[0] = byte(ms >> 40)
	raw[1] = byte(ms >> 32)
	raw[2] = byte(ms >> 24)
	raw[3] = byte(ms >> 16)
	raw[4] = byte(ms >> 8)
	raw[5] = byte(ms)
	if _, err := rand.Read(raw[6:]); err != nil {
		// crypto/rand 在受支持的平台上不会失败；失败意味着系统随机源不可用，不应继续运行。
		panic("model: 系统随机源不可用: " + err.Error())
	}

	// 128 bit 编码为 26 个 5 bit 字符（最高位补 2 个零 bit）。
	var out [26]byte
	var acc uint64
	bits := 0
	pos := 25
	for i := 15; i >= 0; i-- {
		acc |= uint64(raw[i]) << bits
		bits += 8
		for bits >= 5 && pos >= 0 {
			out[pos] = crockford[acc&31]
			acc >>= 5
			bits -= 5
			pos--
		}
	}
	for pos >= 0 {
		out[pos] = crockford[acc&31]
		acc >>= 5
		pos--
	}
	return string(out[:])
}

// IsID 判断字符串是否是 NewID 生成的格式。
func IsID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'H' || c >= 'J' && c <= 'K' || c >= 'M' && c <= 'N' || c >= 'P' && c <= 'T' || c >= 'V' && c <= 'Z') {
			return false
		}
	}
	// 首字符限制为 0-7，保证时间戳不溢出 48 bit。
	return s[0] <= '7'
}
