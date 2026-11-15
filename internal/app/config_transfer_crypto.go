package app

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/vela/internal/credentials"
)

// configurationFormat 与版本一起限定密码派生和密文认证范围；不接受原型快照或整库备份。
const configurationFormat = "vela-configuration"

// configurationLimit 是加密 JSON 文件大小上限，单位字节；原型的 5 MB 限制继续沿用。
const configurationLimit = 5_000_000

// configurationIterations 是 v1 固定的 PBKDF2-HMAC-SHA256 迭代数，不由外部文件指定计算成本。
const configurationIterations = 600_000

// configurationEnvelope 只保存版本、算法、随机盐和认证密文，所有资源及凭据均位于密文内。
type configurationEnvelope struct {
	// Format、Version 标明正式配置包及可接受的格式版本。
	Format  string `json:"format"`
	Version int    `json:"version"`
	// KDF 标明 v1 使用的固定派生参数；Salt 为每次导出随机生成的 16 字节盐。
	KDF  string `json:"kdf"`
	Salt []byte `json:"salt"`
	// Data 为现有凭据模块封装的 AES-256-GCM 随机 nonce 与认证密文。
	Data []byte `json:"data"`
}

// configurationPassword 拒绝空白或过短密码；密码本身不裁剪，允许有效密码内包含空格。
func configurationPassword(password string) error {
	if strings.TrimSpace(password) == "" || utf8.RuneCountInString(password) < 8 || len(password) > 1024 {
		return errors.New("配置文件密码需为 8 个以上字符，最长 1024 字节")
	}
	return nil
}

// encryptConfiguration 复用本机凭据的认证加密实现，用独立密码派生密钥生成可跨机读取的文件。
func encryptConfiguration(plain []byte, password string) (string, error) {
	if err := configurationPassword(password); err != nil {
		return "", err
	}
	envelope := configurationEnvelope{Format: configurationFormat, Version: 1, KDF: "pbkdf2-sha256-600000", Salt: make([]byte, 16)}
	rand.Read(envelope.Salt)
	key, err := pbkdf2.Key(sha256.New, password, envelope.Salt, configurationIterations, 32)
	if err != nil {
		return "", err
	}
	envelope.Data, err = credentials.Encrypt(key, "vela-configuration-v1", plain)
	clear(key)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return "", err
	}
	if len(data) > configurationLimit {
		return "", errors.New("配置文件超过 5 MB，未生成导出文件")
	}
	return string(data), nil
}

// decryptConfiguration 先限制体积与版本，再验证密码和密文；错误不回传配置片段或秘密。
func decryptConfiguration(content, password string) ([]byte, error) {
	if len(content) > configurationLimit {
		return nil, errors.New("配置文件超过 5 MB")
	}
	if err := configurationPassword(password); err != nil {
		return nil, err
	}
	var envelope configurationEnvelope
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return nil, errors.New("不是有效的 Vela 加密配置文件")
	}
	if envelope.Format != configurationFormat || envelope.Version != 1 || envelope.KDF != "pbkdf2-sha256-600000" || len(envelope.Salt) != 16 || len(envelope.Data) < 28 {
		return nil, errors.New("配置文件格式或版本不受支持，请使用 Vela 导出的加密配置")
	}
	key, err := pbkdf2.Key(sha256.New, password, envelope.Salt, configurationIterations, 32)
	if err != nil {
		return nil, err
	}
	plain, err := credentials.Decrypt(key, "vela-configuration-v1", envelope.Data)
	clear(key)
	if err != nil {
		return nil, errors.New("密码错误或配置文件已损坏，未修改任何配置")
	}
	return plain, nil
}
