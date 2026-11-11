// Package credentials 在本机保存加密凭据；文件权限隔离其他系统用户，不隔离同一用户下的恶意程序。
package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// ErrNotFound 表示尚未保存该凭据；读取失败或密文损坏不归入此错误。
var ErrNotFound = errors.New("尚未保存凭据")

// LegacyRead 只用于迁移旧凭据；不存在时返回 ErrNotFound，其他失败必须保留供用户重试。
type LegacyRead func(name string) ([]byte, error)

// Store 串行读写本地凭据；一个应用实例持有一个 Store，密钥与密文分开保存。
type Store struct {
	// mu 保护读取、迁移和保存；dir 是仅当前用户可访问的凭据目录。
	mu  sync.Mutex
	dir string
	// legacy 在本地没有该条目时读取旧存储；迁移或确认不存在后持久记忆，不再访问旧存储。
	legacy LegacyRead
}

// New 创建专用凭据目录并收紧权限，不读取钥匙串或生成密钥。
func New(dataDir string, legacy LegacyRead) (*Store, error) {
	dir := filepath.Join(dataDir, "credentials")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("无法创建本地凭据目录")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, errors.New("无法限制本地凭据目录权限")
	}
	return &Store{dir: dir, legacy: legacy}, nil
}

// Encrypt 用 AES-256-GCM 加密，并把随机 nonce 放入结果；binding 防止密文被挪到另一条记录。
func Encrypt(key []byte, binding string, value []byte) ([]byte, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return aead.Seal(nil, nil, value, []byte(binding)), nil
}

// Decrypt 校验并解密；主密钥、绑定或密文不匹配时不返回部分明文。
func Decrypt(key []byte, binding string, value []byte) ([]byte, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nil, value, []byte(binding))
	if err != nil {
		return nil, errors.New("凭据解密失败，密钥或密文不匹配；已保存的数据未被改动")
	}
	return plain, nil
}

// newAEAD 使用标准库管理随机 nonce，主密钥固定为 32 字节，不接受其他 AES 密钥长度。
func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("本地凭据主密钥格式不正确")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

// readPrivate 读取凭据文件并收紧权限；拒绝符号链接和非普通文件，避免意外写入其他文件。
func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("凭据存储必须是普通文件")
	}
	if err := os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// load 重新读取磁盘状态；已有密文时主密钥缺失或损坏必须报错，不生成替代密钥。
func (s *Store) load() ([]byte, map[string][]byte, error) {
	key, keyErr := readPrivate(filepath.Join(s.dir, "master.key"))
	sealed, dataErr := readPrivate(filepath.Join(s.dir, "secrets.enc"))
	if dataErr != nil && !errors.Is(dataErr, os.ErrNotExist) {
		return nil, nil, errors.New("无法读取本地凭据文件，已保存的数据未被改动")
	}
	if keyErr != nil {
		if errors.Is(keyErr, os.ErrNotExist) && errors.Is(dataErr, os.ErrNotExist) {
			return nil, map[string][]byte{}, nil
		}
		return nil, nil, errors.New("本地凭据主密钥不可用，已保存的数据未被改动")
	}
	if len(key) != 32 {
		return nil, nil, errors.New("本地凭据主密钥格式不正确，已保存的数据未被改动")
	}
	entries := map[string][]byte{}
	if errors.Is(dataErr, os.ErrNotExist) {
		return nil, nil, errors.New("本地凭据密文文件缺失，拒绝覆盖；请恢复原文件")
	}
	plain, err := Decrypt(key, "vela-credentials-v1", sealed)
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(plain, &entries); err != nil || entries == nil {
		return nil, nil, errors.New("本地凭据文件格式不正确，已保存的数据未被改动")
	}
	return key, entries, nil
}

// writePrivate 先写入权限为 0600 的临时文件并同步，再原子替换；失败不截断旧文件。
func writePrivate(path string, value []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".credentials-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(value); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

// save 先持久化主密钥，再原子保存密文；失败后旧密文仍可读取，绝不覆盖已有主密钥。
func (s *Store) save(key []byte, entries map[string][]byte) error {
	initial := key == nil
	saved := false
	// 首次写入失败时清理本次新建的主密钥，让迁移可重试；已存在的主密钥从不删除。
	defer func() {
		if initial && !saved {
			_ = os.Remove(filepath.Join(s.dir, "master.key"))
		}
	}()
	if key == nil {
		key = make([]byte, 32)
		rand.Read(key)
		f, err := os.OpenFile(filepath.Join(s.dir, "master.key"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			initial = false
			return errors.New("无法创建本地凭据主密钥")
		}
		if _, err = f.Write(key); err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			return errors.New("无法保存本地凭据主密钥")
		}
	}
	plain, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	sealed, err := Encrypt(key, "vela-credentials-v1", plain)
	if err != nil {
		return err
	}
	if err := writePrivate(filepath.Join(s.dir, "secrets.enc"), sealed); err != nil {
		return errors.New("无法保存本地凭据，原凭据保持不变")
	}
	saved = true
	return nil
}

// Get 读取或一次性迁移指定凭据；nil 条目记录旧存储也不存在，避免更新后重复访问钥匙串。
func (s *Store) Get(name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, entries, err := s.load()
	if err != nil {
		return nil, err
	}
	value, known := entries[name]
	if !known {
		if s.legacy != nil {
			value, err = s.legacy(name)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return nil, errors.New("旧凭据迁移失败，请重试；原凭据未被改动")
			}
		}
		entries[name] = value
		if err := s.save(key, entries); err != nil {
			return nil, err
		}
	}
	if value == nil {
		return nil, ErrNotFound
	}
	return value, nil
}

// Set 加密保存指定凭据；先验证现有文件，密钥缺失或密文损坏时拒绝覆盖。
func (s *Store) Set(name string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, entries, err := s.load()
	if err != nil {
		return err
	}
	if name == "" || value == nil {
		return errors.New("凭据名称和内容不能为空")
	}
	entries[name] = value
	return s.save(key, entries)
}
