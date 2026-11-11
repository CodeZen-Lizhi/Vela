package providers

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// masterKeySize 是主密钥长度，对应 AES-256。
const masterKeySize = 32

// ErrNoMasterKey 表示保存位置中还没有主密钥。
var ErrNoMasterKey = errors.New("尚未生成凭据密钥")

// errMasterKeyMissing 是已有加密凭据却找不到主密钥时给用户的说明。
var errMasterKeyMissing = errors.New("系统钥匙串中找不到凭据密钥，无法读取或保存上游密钥；已保存的数据未被改动")

// KeyStore 保存加密凭据用的主密钥。正式实现由桌面入口提供（系统钥匙串），业务模块不直接依赖系统凭据存储。
type KeyStore interface {
	// Load 返回已保存的内容；尚未保存时返回 ErrNoMasterKey。
	Load() ([]byte, error)
	// Save 保存内容，覆盖原有值。
	Save(value []byte) error
}

// vault 用一把主密钥加解密凭据：主密钥放在 KeyStore，密文放在数据库。
// 系统钥匙串单条内容有长度上限，逐条读取还可能逐条向用户询问授权，因此只把主密钥交给钥匙串。
type vault struct {
	// mu 保护 key；store 是主密钥的保存位置。
	mu    sync.Mutex
	store KeyStore
	// key 是已加载的主密钥，在进程内缓存，避免每次操作都访问系统钥匙串。
	key []byte
}

// load 返回主密钥：优先使用缓存，否则从保存位置读取并校验长度；尚未生成时返回 ErrNoMasterKey。
func (v *vault) load() ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key != nil {
		return v.key, nil
	}
	raw, err := v.store.Load()
	if err != nil {
		if errors.Is(err, ErrNoMasterKey) {
			return nil, ErrNoMasterKey
		}
		return nil, fmt.Errorf("读取凭据密钥失败，已保存的数据未被改动: %w", err)
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != masterKeySize {
		return nil, errors.New("系统钥匙串中的凭据密钥格式不正确，已保存的数据未被改动")
	}
	v.key = key
	return key, nil
}

// generate 生成并保存新的主密钥。调用方必须先确认数据库中没有任何已加密的凭据，否则旧凭据将无法解密。
func (v *vault) generate() ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	key := make([]byte, masterKeySize)
	// crypto/rand.Read 不会返回错误，随机源不可用时直接终止进程。
	rand.Read(key)
	// 以十六进制文本保存，各平台的凭据存储都能原样读回。
	if err := v.store.Save([]byte(hex.EncodeToString(key))); err != nil {
		return nil, fmt.Errorf("保存凭据密钥失败: %w", err)
	}
	v.key = key
	return key, nil
}

// newAEAD 用主密钥创建 AES-256-GCM；随机数由标准库生成并放在密文开头。
func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

// seal 加密一项凭据。binding 标明密文所属的记录并参与认证，密文被挪到其他记录时解密会失败。
func seal(key []byte, binding string, plaintext []byte) ([]byte, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return aead.Seal(nil, nil, plaintext, []byte(binding)), nil
}

// open 解密 seal 的结果；主密钥不匹配、密文被改动或 binding 不一致时返回错误。
func open(key []byte, binding string, sealed []byte) ([]byte, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, nil, sealed, []byte(binding))
	if err != nil {
		return nil, errors.New("上游密钥解密失败：凭据密钥与已保存的数据不匹配；已保存的数据未被改动")
	}
	return plaintext, nil
}
