package providers

import (
	"context"
	"encoding/json"
)

// removeLocalProxies 原子清理旧局部代理：供应商 custom 改 global，Key custom/provider 改 global，并删除所有局部 url。
// 明文和整文档加密均走既有编解码；保留代理对象、请求策略及文档中的其他未知字段，不解密已废弃的局部 URL。
func (s *Store) removeLocalProxies(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"providers", "provider_keys"} {
		rows, err := tx.QueryContext(ctx, "SELECT id,document FROM "+table)
		if err != nil {
			return err
		}
		updates := map[string]string{}
		for rows.Next() {
			var id, raw string
			if err = rows.Scan(&id, &raw); err != nil {
				break
			}
			var doc map[string]json.RawMessage
			if err = s.decodeDocument([]byte(raw), &doc); err != nil {
				break
			}
			parent := doc
			if table == "providers" {
				parent = map[string]json.RawMessage{}
				if err = json.Unmarshal(doc["request"], &parent); err != nil {
					break
				}
			}
			var proxy map[string]json.RawMessage
			if len(parent["proxy"]) == 0 {
				continue
			}
			if err = json.Unmarshal(parent["proxy"], &proxy); err != nil {
				break
			}
			var mode string
			if err = json.Unmarshal(proxy["mode"], &mode); err != nil {
				break
			}
			_, changed := proxy["url"]
			delete(proxy, "url")
			if mode == "custom" || (table == "provider_keys" && mode == "provider") {
				next := ProxyGlobal
				proxy["mode"], err = json.Marshal(next)
				if err != nil {
					break
				}
				changed = true
			}
			if !changed {
				continue
			}
			parent["proxy"], err = json.Marshal(proxy)
			if err != nil {
				break
			}
			if table == "providers" {
				doc["request"], err = json.Marshal(parent)
				if err != nil {
					break
				}
			}
			var encoded []byte
			encoded, err = s.encodeDocument(doc)
			if err != nil {
				break
			}
			updates[id] = string(encoded)
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
			if _, err = tx.ExecContext(ctx, "UPDATE "+table+" SET document=? WHERE id=?", data, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
