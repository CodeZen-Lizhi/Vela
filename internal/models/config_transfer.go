package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// ValidateConfiguration 校验搬运模型的完整规则和唯一身份，不依赖本机目录或写入数据库。
func ValidateConfiguration(values []Model) error {
	ids, names := map[string]bool{}, map[string]bool{}
	for _, m := range values {
		name, err := normalizeModelID(m.ModelID)
		if err != nil || name != m.ModelID || m.ID == "" || len(m.ID) > 256 || ids[m.ID] || names[m.ModelID] {
			return errors.New("模型配置包含无效或重复的模型身份")
		}
		ids[m.ID], names[m.ModelID] = true, true
		if strings.TrimSpace(m.Name) == "" || utf8.RuneCountInString(m.Name) > 80 || m.Context < 0 || !slices.Contains([]string{"chat", "image", "video", "vector"}, m.Kind) {
			return errors.New("模型配置的名称、类型或上下文不合法")
		}
		for _, p := range []Pricing{m.Pricing, m.SourcePricing} {
			if err := validatePricing(p); err != nil {
				return fmt.Errorf("模型价格规则不合法: %w", err)
			}
		}
		for _, group := range m.Overrides {
			if !slices.Contains(groups, group) {
				return errors.New("模型配置包含未知的价格保护分组")
			}
		}
	}
	return nil
}

// WithConfigurationLock 串行化配置导入与目录同步、模型编辑；调用方必须在开启数据库事务前进入此锁。
// 锁序固定为模型锁、供应商锁、数据库事务，避免单连接 SQLite 与后台同步相互等待。
func (s *Store) WithConfigurationLock(apply func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return apply()
}

// ExportConfiguration 在调用方事务内读取完整模型，保留价格来源与本地修改保护规则。
func (s *Store) ExportConfiguration(ctx context.Context, tx *sql.Tx) ([]Model, error) {
	rows, err := tx.QueryContext(ctx, "SELECT document FROM models ORDER BY model_id,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Model{}
	for rows.Next() {
		var data string
		var m Model
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &m); err != nil {
			return nil, errors.New("模型配置损坏，未导出文件")
		}
		values = append(values, m)
	}
	return values, rows.Err()
}

// ImportConfiguration 按统一模型名匹配并保留本机稳定身份，返回来源身份到本机身份的映射。
// 调用方持有模型锁并管理整体事务；覆盖递增本机修订号以拒绝导入前的编辑草稿，不删除本机独有模型。
func (s *Store) ImportConfiguration(ctx context.Context, tx *sql.Tx, values []Model, overwrite bool) (map[string]string, error) {
	if err := ValidateConfiguration(values); err != nil {
		return nil, err
	}
	current, err := s.ExportConfiguration(ctx, tx)
	if err != nil {
		return nil, err
	}
	byName, byID := map[string]Model{}, map[string]Model{}
	for _, m := range current {
		byName[m.ModelID], byID[m.ID] = m, m
	}
	identities := map[string]string{}
	for _, m := range values {
		sourceID := m.ID
		old, exists := byName[m.ModelID]
		if sameID, found := byID[m.ID]; found && sameID.ModelID != m.ModelID {
			return nil, errors.New("模型稳定身份与本机模型 ID 冲突，已保留全部原配置")
		}
		if exists {
			identities[sourceID] = old.ID
			if !overwrite {
				continue
			}
			m.ID, m.Revision, m.EditRevision = old.ID, old.Revision+1, old.EditRevision+1
		} else {
			m.Revision, m.EditRevision = 1, 0
			identities[sourceID] = m.ID
		}
		data, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		if exists {
			_, err = tx.ExecContext(ctx, "UPDATE models SET document=? WHERE id=?", string(data), m.ID)
		} else {
			_, err = tx.ExecContext(ctx, "INSERT INTO models(id,model_id,document) VALUES(?,?,?)", m.ID, m.ModelID, string(data))
		}
		if err != nil {
			return nil, err
		}
	}
	return identities, nil
}
