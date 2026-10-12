package secret

import (
	"context"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// ServiceName 是在系统凭据存储中使用的 service 名。
const ServiceName = "gogosling"

// probeName 是 Open 用来确认系统凭据存储可用的临时条目名。
const probeName = "__probe__"

// Keyring 是系统凭据存储的最小接口，与 go-keyring 的函数签名一致；测试注入假实现。
// Get 找不到时必须返回 keyring.ErrNotFound。
type Keyring interface {
	Set(service, user, password string) error
	Get(service, user string) (string, error)
	Delete(service, user string) error
}

type systemKeyring struct{}

func (systemKeyring) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}

func (systemKeyring) Get(service, user string) (string, error) {
	return keyring.Get(service, user)
}

func (systemKeyring) Delete(service, user string) error {
	return keyring.Delete(service, user)
}

// SystemKeyring 返回访问当前操作系统凭据存储的 Keyring。
func SystemKeyring() Keyring { return systemKeyring{} }

// KeyringStore 把凭据保存在系统凭据存储中；可并发使用。
type KeyringStore struct {
	kr      Keyring
	service string
}

// NewKeyringStore 用给定的 Keyring 创建存储，service 名为 ServiceName。
func NewKeyringStore(kr Keyring) *KeyringStore {
	return &KeyringStore{kr: kr, service: ServiceName}
}

// Backend 返回 "keyring"。
func (s *KeyringStore) Backend() string { return BackendKeyring }

// Get 实现 Store。
func (s *KeyringStore) Get(ctx context.Context, name string) (string, error) {
	if err := checkCall(ctx, name); err != nil {
		return "", err
	}
	v, err := s.kr.Get(s.service, name)
	if err != nil {
		return "", mapKeyringErr(err)
	}
	return v, nil
}

// Set 实现 Store。
func (s *KeyringStore) Set(ctx context.Context, name, value string) error {
	if err := checkCall(ctx, name); err != nil {
		return err
	}
	if err := s.kr.Set(s.service, name, value); err != nil {
		return mapKeyringErr(err)
	}
	return nil
}

// Delete 实现 Store；不存在时不报错。
func (s *KeyringStore) Delete(ctx context.Context, name string) error {
	if err := checkCall(ctx, name); err != nil {
		return err
	}
	if err := s.kr.Delete(s.service, name); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return mapKeyringErr(err)
	}
	return nil
}

// Has 实现 Store。
func (s *KeyringStore) Has(ctx context.Context, name string) (bool, error) {
	_, err := s.Get(ctx, name)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// mapKeyringErr 把 go-keyring 的错误翻译为本包的哨兵错误；其余错误带上下文返回，不含任何值。
func mapKeyringErr(err error) error {
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, keyring.ErrSetDataTooBig):
		return ErrTooLarge
	default:
		return fmt.Errorf("secret: 系统凭据存储操作失败: %w", err)
	}
}

// probe 写入、读回并删除一条探测记录，确认系统凭据存储真正可用（而不只是库能编译）。
func probe(ctx context.Context, kr Keyring) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	const want = "ok"
	if err := kr.Set(ServiceName, probeName, want); err != nil {
		return fmt.Errorf("写入探测记录失败: %w", err)
	}
	got, err := kr.Get(ServiceName, probeName)
	if err != nil {
		_ = kr.Delete(ServiceName, probeName)
		return fmt.Errorf("读取探测记录失败: %w", err)
	}
	if err := kr.Delete(ServiceName, probeName); err != nil {
		return fmt.Errorf("删除探测记录失败: %w", err)
	}
	if got != want {
		return errors.New("探测记录读回的内容不一致")
	}
	return nil
}
