package credentials

import (
	"encoding/base64"
	"errors"
	"strings"
)

// documentPrefix 标识带版本的数据库配置密文，区别于迁移前的 JSON。
const documentPrefix = "vela-enc-v1:"

// EncodeDocument 使用本机主密钥加密可能含代理认证信息的配置，binding 区分配置用途。
func (s *Store) EncodeDocument(binding string, plain []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, entries, err := s.load()
	if err != nil {
		return "", err
	}
	if key == nil {
		if err := s.save(key, entries); err != nil {
			return "", err
		}
		key, _, err = s.load()
		if err != nil {
			return "", err
		}
	}
	sealed, err := Encrypt(key, binding, plain)
	if err != nil {
		return "", err
	}
	return documentPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// DecodeDocument 解密配置；无前缀时兼容旧 JSON，由模块初始化迁移，失败不返回明文片段。
func (s *Store) DecodeDocument(binding, data string) ([]byte, error) {
	if !strings.HasPrefix(data, documentPrefix) {
		return []byte(data), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key, _, err := s.load()
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, errors.New("本地凭据主密钥不可用，无法解密配置")
	}
	sealed, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(data, documentPrefix))
	if err != nil {
		return nil, errors.New("加密配置格式不正确")
	}
	return Decrypt(key, binding, sealed)
}
