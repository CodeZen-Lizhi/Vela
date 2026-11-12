package records

import (
	"database/sql"
	"fmt"
)

// migrateMetrics 为请求摘要建立直接数值投影，避免百万行查询反复解析诊断 JSON。
func (s *Store) migrateMetrics() error {
	rows, err := s.db.Query(`PRAGMA table_info(request_records)`)
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue sql.NullString
		if err = rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			break
		}
		columns[name] = true
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changed := false
	for _, column := range []struct{ name, kind string }{{"input_context", "INTEGER NOT NULL DEFAULT 0"}, {"output_tokens", "INTEGER NOT NULL DEFAULT 0"}, {"cache_read", "INTEGER NOT NULL DEFAULT 0"}, {"cost", "REAL"}, {"usage_known", "INTEGER NOT NULL DEFAULT 0"}, {"duration_ms", "INTEGER NOT NULL DEFAULT 0"}} {
		if !columns[column.name] {
			changed = true
			if _, err = tx.Exec("ALTER TABLE request_records ADD COLUMN " + column.name + " " + column.kind); err != nil {
				return fmt.Errorf("升级请求摘要投影: %w", err)
			}
		}
	}
	if changed {
		_, err = tx.Exec(`UPDATE request_records SET input_context=COALESCE(json_extract(document,'$.usage.context'),0),output_tokens=COALESCE(json_extract(document,'$.usage.output'),0),cache_read=COALESCE(json_extract(document,'$.usage.cacheRead'),0),cost=json_extract(document,'$.cost'),usage_known=json_extract(document,'$.usage') IS NOT NULL,duration_ms=COALESCE(json_extract(document,'$.durationMillis'),0)`)
		if err != nil {
			return err
		}
	}
	// 改名后更新独立累计的最后名称；单条诊断仍保存请求发生时快照。
	_, err = tx.Exec(`CREATE TRIGGER IF NOT EXISTS request_provider_renamed AFTER UPDATE OF name ON providers BEGIN UPDATE request_totals SET provider_name=NEW.name WHERE provider_id=NEW.id; END;
 CREATE TRIGGER IF NOT EXISTS request_model_renamed AFTER UPDATE OF document ON models BEGIN UPDATE request_totals SET model_name=json_extract(NEW.document,'$.name') WHERE model_id=NEW.id; END;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// contextTokens 返回已知完整输入上下文，无用量仅用于投影占位，usage_known 独立区分。
func contextTokens(r Record) int64 {
	if r.Usage == nil {
		return 0
	}
	return r.Usage.Context
}

// outputTokens 返回已知输出计数，不将投影零值当作上游提供零用量。
func outputTokens(r Record) int64 {
	if r.Usage == nil {
		return 0
	}
	return r.Usage.Output
}

// cacheTokens 返回已知缓存读取计数，未知由 usage_known 标记。
func cacheTokens(r Record) int64 {
	if r.Usage == nil {
		return 0
	}
	return r.Usage.CacheRead
}
