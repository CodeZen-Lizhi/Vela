package providers

import (
	"encoding/json"
	"errors"

	"github.com/CodeZen-Lizhi/vela/internal/credentials"
)

// encodeDocument 加密含代理地址的供应商或 Key 文档；未接入桌面存储的测试沿用 JSON。
func (s *Store) encodeDocument(value any) ([]byte, error) {
	plain, err := json.Marshal(value)
	if err != nil || s.documents == nil {
		return plain, err
	}
	sealed, err := s.documents.EncodeDocument("provider-config", plain)
	return []byte(sealed), err
}

// decodeDocument 解密后解析配置；不把错误中的配置片段暴露给调用方。
func (s *Store) decodeDocument(data []byte, value any) error {
	if s.documents != nil {
		plain, err := s.documents.DecodeDocument("provider-config", string(data))
		if err != nil {
			return err
		}
		data = plain
	}
	return json.Unmarshal(data, value)
}

// protectDocuments 在接入本地存储时事务迁移旧 JSON，失败回滚，普通数据库读取不再暴露代理认证信息。
func (s *Store) protectDocuments(secrets *credentials.Store) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"providers", "provider_keys"} {
		rows, err := tx.Query("SELECT id, document FROM " + table)
		if err != nil {
			return err
		}
		updates := map[string]string{}
		for rows.Next() {
			var id, data string
			if err = rows.Scan(&id, &data); err != nil {
				break
			}
			var plain []byte
			plain, err = secrets.DecodeDocument("provider-config", data)
			if err != nil {
				break
			}
			if !json.Valid(plain) {
				err = errors.New("供应商配置格式不正确")
				break
			}
			if string(plain) != data {
				continue
			}
			updates[id], err = secrets.EncodeDocument("provider-config", plain)
			if err != nil {
				break
			}
		}
		rowsErr := rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if rowsErr != nil {
			return rowsErr
		}
		if closeErr != nil {
			return closeErr
		}
		for id, data := range updates {
			if _, err := tx.Exec("UPDATE "+table+" SET document=? WHERE id=?", data, id); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.documents = secrets
	return nil
}
