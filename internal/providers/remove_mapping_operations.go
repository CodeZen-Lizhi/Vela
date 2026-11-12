package providers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// removeMappingOperations 在模型初始化事务中校验全部映射文档，仅删除历史 operations 字段。
// 保留数组顺序、优先级及未知字段；无旧字段的文档不重写，任一文档无效时全部回滚。
func removeMappingOperations(tx *sql.Tx) error {
	rows, err := tx.Query("SELECT provider_id,model_id,document FROM provider_model_mappings")
	if err != nil {
		return err
	}
	// update 只暂存确需清理的文档，关闭结果集后统一写入以兼容单连接数据库。
	type update struct {
		// providerID、modelID 是映射所属的复合主键；document 是清理后的 JSON。
		providerID, modelID, document string
	}
	updates := []update{}
	for rows.Next() {
		var item update
		var raw string
		if err = rows.Scan(&item.providerID, &item.modelID, &raw); err != nil {
			break
		}
		var mappings []map[string]json.RawMessage
		if err = json.Unmarshal([]byte(raw), &mappings); err != nil {
			break
		}
		if mappings == nil {
			err = errors.New("模型映射文档必须为数组")
			break
		}
		changed := false
		for _, mapping := range mappings {
			if mapping == nil {
				err = errors.New("模型映射项必须为对象")
				break
			}
			if _, exists := mapping["operations"]; exists {
				delete(mapping, "operations")
				changed = true
			}
		}
		if err != nil {
			break
		}
		// 同时校验现有请求名和优先级的 JSON 类型，不归一化或重排历史数据。
		var typed []ProviderModelMapping
		if err = json.Unmarshal([]byte(raw), &typed); err != nil {
			break
		}
		if changed {
			var data []byte
			data, err = json.Marshal(mappings)
			if err != nil {
				break
			}
			item.document = string(data)
			updates = append(updates, item)
		}
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return fmt.Errorf("读取历史映射文档: %w", err)
	}
	if rowsErr != nil {
		return rowsErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, item := range updates {
		if _, err = tx.Exec("UPDATE provider_model_mappings SET document=? WHERE provider_id=? AND model_id=?", item.document, item.providerID, item.modelID); err != nil {
			return err
		}
	}
	return nil
}
