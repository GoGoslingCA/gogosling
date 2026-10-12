package secret

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrNotFound 表示该名称下没有凭据。
	ErrNotFound = errors.New("secret: 凭据不存在")
	// ErrInvalidName 表示凭据名称不符合 ValidateName 的规则。
	ErrInvalidName = errors.New("secret: 凭据名称不合法")
	// ErrTooLarge 表示系统凭据存储拒绝保存过大的值（macOS 约 3 KB、Windows 2.5 KB）。
	ErrTooLarge = errors.New("secret: 凭据过大，系统凭据存储无法保存")
	// ErrCorrupt 表示加密文件无法解密：密钥文件与文件不匹配，或文件被篡改、损坏。
	ErrCorrupt = errors.New("secret: 加密文件无法解密（密钥不匹配或文件损坏）")
	// ErrKeyMissing 表示 secrets.enc 存在但 secret.key 丢失；此时不会生成新密钥覆盖，避免悄悄丢数据。
	ErrKeyMissing = errors.New("secret: 密钥文件缺失，无法解密已有的加密文件")
)

// Store 以名称存取凭据。实现必须可并发使用。
//
// 约定：Get 找不到返回 ErrNotFound；Delete 对不存在的名称不报错；
// 所有方法在 name 不合法时返回 ErrInvalidName；ctx 取消时返回 ctx.Err()。
type Store interface {
	Get(ctx context.Context, name string) (string, error)
	Set(ctx context.Context, name, value string) error
	Delete(ctx context.Context, name string) error
	Has(ctx context.Context, name string) (bool, error)
}

// MaxNameLen 是凭据名称的最大长度。
const MaxNameLen = 128

// ValidateName 校验凭据名称：1–128 个字符，以字母或数字开头，只含字母、数字与 . _ : -。
// 这个集合在三个平台的凭据存储里都能作为账户名使用。
func ValidateName(name string) error {
	if name == "" || len(name) > MaxNameLen {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if i == 0 && !alnum {
			return fmt.Errorf("%w: %q", ErrInvalidName, name)
		}
		if !alnum && c != '.' && c != '_' && c != ':' && c != '-' {
			return fmt.Errorf("%w: %q", ErrInvalidName, name)
		}
	}
	return nil
}

// BackendName 返回 Store 的后端名（"keyring" 或 "file"），用于诊断信息；未知实现返回 "unknown"。
func BackendName(s Store) string {
	if b, ok := s.(interface{ Backend() string }); ok {
		return b.Backend()
	}
	return "unknown"
}

// checkCall 是各方法共用的前置检查。
func checkCall(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ValidateName(name)
}
