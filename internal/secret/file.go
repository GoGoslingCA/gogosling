package secret

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const (
	// FileName 是加密回退存储的文件名。
	FileName = "secrets.enc"
	// KeyFileName 是加密密钥的文件名；丢失它就无法再解密 FileName。
	KeyFileName = "secret.key"

	filePerm       = 0o600
	dirPerm        = 0o700
	keySize        = 32 // AES-256
	envelopeVer    = 1
	additionalData = "gogosling.secrets.v1"
)

// envelope 是 secrets.enc 的外层结构；[]byte 字段由 encoding/json 以 base64 编码。
type envelope struct {
	Version    int    `json:"version"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// FileStore 把全部凭据加密保存在一个文件里；可并发使用。
type FileStore struct {
	path    string
	keyPath string
	aead    cipher.AEAD
	mu      sync.Mutex
}

// OpenFileStore 打开（必要时创建）dataDir 下的加密文件存储。
// 密钥文件不存在时生成；若 secrets.enc 已存在而密钥文件缺失，返回 ErrKeyMissing 而不是生成新密钥。
func OpenFileStore(dataDir string) (*FileStore, error) {
	if err := os.MkdirAll(dataDir, dirPerm); err != nil {
		return nil, fmt.Errorf("secret: 创建数据目录失败: %w", err)
	}
	s := &FileStore{path: filepath.Join(dataDir, FileName), keyPath: filepath.Join(dataDir, KeyFileName)}
	key, err := loadOrCreateKey(s.keyPath, s.path)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret: 初始化加密失败: %w", err)
	}
	s.aead, err = cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: 初始化加密失败: %w", err)
	}
	// 启动时就验证已有文件能解密，把密钥不匹配的问题暴露在启动阶段而不是第一次取凭据时。
	if _, err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Backend 返回 "file"。
func (s *FileStore) Backend() string { return BackendFile }

// Path 返回加密文件路径。
func (s *FileStore) Path() string { return s.path }

// Get 实现 Store。
func (s *FileStore) Get(ctx context.Context, name string) (string, error) {
	if err := checkCall(ctx, name); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return "", err
	}
	v, ok := m[name]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

// Set 实现 Store。
func (s *FileStore) Set(ctx context.Context, name, value string) error {
	if err := checkCall(ctx, name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return err
	}
	m[name] = value
	return s.save(m)
}

// Delete 实现 Store；不存在时不报错也不重写文件。
func (s *FileStore) Delete(ctx context.Context, name string) error {
	if err := checkCall(ctx, name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := m[name]; !ok {
		return nil
	}
	delete(m, name)
	return s.save(m)
}

// Has 实现 Store。
func (s *FileStore) Has(ctx context.Context, name string) (bool, error) {
	if err := checkCall(ctx, name); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return false, err
	}
	_, ok := m[name]
	return ok, nil
}

// load 读取并解密整个文件；文件不存在时返回空集合。调用方持有 mu。
func (s *FileStore) load() (map[string]string, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("secret: 读取加密文件失败: %w", err)
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if env.Version != envelopeVer || len(env.Nonce) != s.aead.NonceSize() {
		return nil, ErrCorrupt
	}
	plain, err := s.aead.Open(nil, env.Nonce, env.Ciphertext, []byte(additionalData))
	if err != nil {
		return nil, ErrCorrupt
	}
	m := map[string]string{}
	if err := json.Unmarshal(plain, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return m, nil
}

// save 用新的随机 nonce 加密整个集合并原子写回。调用方持有 mu。
func (s *FileStore) save(m map[string]string) error {
	plain, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("secret: 序列化失败: %w", err)
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("secret: 系统随机源不可用: %w", err)
	}
	env := envelope{Version: envelopeVer, Nonce: nonce, Ciphertext: s.aead.Seal(nil, nonce, plain, []byte(additionalData))}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("secret: 序列化失败: %w", err)
	}
	return writeFileAtomic(s.path, data, filePerm)
}

// loadOrCreateKey 读取密钥文件；不存在时生成。encPath 存在而密钥不存在视为错误。
func loadOrCreateKey(keyPath, encPath string) ([]byte, error) {
	raw, err := os.ReadFile(keyPath)
	switch {
	case err == nil:
		key, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if decErr != nil || len(key) != keySize {
			return nil, fmt.Errorf("secret: 密钥文件 %s 格式不正确", keyPath)
		}
		tightenPerm(keyPath)
		return key, nil
	case errors.Is(err, fs.ErrNotExist):
		if _, statErr := os.Stat(encPath); statErr == nil {
			return nil, fmt.Errorf("%w: %s", ErrKeyMissing, keyPath)
		}
		key := make([]byte, keySize)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("secret: 系统随机源不可用: %w", err)
		}
		encoded := base64.StdEncoding.EncodeToString(key) + "\n"
		if err := writeFileAtomic(keyPath, []byte(encoded), filePerm); err != nil {
			return nil, err
		}
		return key, nil
	default:
		return nil, fmt.Errorf("secret: 读取密钥文件失败: %w", err)
	}
}

// tightenPerm 在 POSIX 上把被放宽的密钥文件权限收回 0600；Windows 没有这些权限位。
func tightenPerm(path string) {
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o077 == 0 {
		return
	}
	_ = os.Chmod(path, filePerm)
}

// writeFileAtomic 先写同目录下的临时文件并 fsync，再重命名覆盖目标；失败时清理临时文件。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Errorf("secret: 系统随机源不可用: %w", err)
	}
	tmp := path + ".tmp-" + hex.EncodeToString(suffix[:])
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("secret: 创建临时文件失败: %w", err)
	}
	cleanup := func(e error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return e
	}
	if _, err := f.Write(data); err != nil {
		return cleanup(fmt.Errorf("secret: 写入临时文件失败: %w", err))
	}
	if err := f.Sync(); err != nil {
		return cleanup(fmt.Errorf("secret: 同步临时文件失败: %w", err))
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("secret: 关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("secret: 替换文件失败: %w", err)
	}
	return nil
}
