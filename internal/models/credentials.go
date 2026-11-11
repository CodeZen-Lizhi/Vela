package models

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/CodeZen-Lizhi/vela/internal/credentials"
)

// ProtectProxy 接入本机凭据存储并迁移已有全局代理；失败时保留数据库原值。
func (s *Store) ProtectProxy(secrets *credentials.Store) error {
	var data string
	err := s.db.QueryRow("SELECT document FROM settings WHERE name='proxy'").Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		s.credentials = secrets
		return nil
	}
	if err != nil {
		return err
	}
	plain, err := secrets.DecodeDocument("global-proxy", data)
	if err != nil {
		return err
	}
	var proxy Proxy
	if err := json.Unmarshal(plain, &proxy); err != nil {
		return errors.New("代理配置格式不正确")
	}
	if string(plain) != data {
		s.credentials = secrets
		return nil
	}
	sealed, err := secrets.EncodeDocument("global-proxy", plain)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec("UPDATE settings SET document=? WHERE name='proxy'", sealed); err != nil {
		return err
	}
	s.credentials = secrets
	return nil
}
