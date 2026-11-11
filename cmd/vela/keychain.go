package main

import (
	"errors"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/egoist/mygo"
)

// keychainEntry 是系统凭据存储（macOS 为登录钥匙串）中的一个账户，用作供应商凭据主密钥的保存位置。
// 业务模块只依赖 providers.KeyStore，不直接接触桌面宿主。
type keychainEntry struct {
	// account 是凭据存储中的账户名，服务名沿用应用标识。
	account string
}

// Load 读取该账户保存的内容；尚未保存时按契约返回 providers.ErrNoMasterKey。
func (k keychainEntry) Load() ([]byte, error) {
	value, err := mygo.Secrets.Get(k.account)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return nil, providers.ErrNoMasterKey
	}
	return value, err
}

// Save 把内容写入该账户，覆盖原有值。
func (k keychainEntry) Save(value []byte) error {
	return mygo.Secrets.Set(k.account, value)
}
