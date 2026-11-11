package main

import (
	"errors"

	"github.com/CodeZen-Lizhi/vela/internal/credentials"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/egoist/mygo"
)

// credentialEntry 把本地加密存储适配为供应商主密钥存储。
// 业务模块只依赖 providers.KeyStore，不直接接触桌面宿主。
type credentialEntry struct {
	// store 与访问密钥共用一个本地存储实例。
	store *credentials.Store
}

// Load 读取该账户保存的内容；尚未保存时按契约返回 providers.ErrNoMasterKey。
func (k credentialEntry) Load() ([]byte, error) {
	value, err := k.store.Get("provider-master-key")
	if errors.Is(err, credentials.ErrNotFound) {
		return nil, providers.ErrNoMasterKey
	}
	return value, err
}

// Save 把内容写入该账户，覆盖原有值。
func (k credentialEntry) Save(value []byte) error {
	return k.store.Set("provider-master-key", value)
}

// legacyCredentials 只读取已发布实现使用的旧访问密钥；隔离数据目录禁止访问日常钥匙串。
func legacyCredentials(isolated bool) credentials.LegacyRead {
	if isolated {
		return nil
	}
	return func(name string) ([]byte, error) {
		if name != "gateway-access-key" {
			return nil, credentials.ErrNotFound
		}
		value, err := mygo.Secrets.Get("gateway-access-key")
		if errors.Is(err, mygo.ErrSecretNotFound) {
			return nil, credentials.ErrNotFound
		}
		return value, err
	}
}
