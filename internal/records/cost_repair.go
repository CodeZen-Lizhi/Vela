package records

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// costRepairMarker 保证共享计价规则修正只扫描一次，后续启动不为历史未知项重复解析全部正文。
const costRepairMarker = "records-cost-repair-v1"

// attemptCostSnapshot 保存修复前的尝试费用事实，用于只调整对应的 Key 累计差值。
type attemptCostSnapshot struct {
	// cost 为修复前估算 USD 的独立副本，nil 表示费用未知。
	cost *float64
	// unknown 表示该尝试修复前是否计入 Key 的未知费用次数。
	unknown bool
}

// copyCost 复制金额指针，避免后续重算覆盖修复前的快照。
func copyCost(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cost := *value
	return &cost
}

// snapshotAttemptCosts 固定每次尝试修复前的费用及未知计数。
func snapshotAttemptCosts(r Record) []attemptCostSnapshot {
	result := make([]attemptCostSnapshot, len(r.Attempts))
	for i, attempt := range r.Attempts {
		result[i] = attemptCostSnapshot{
			cost:    copyCost(attempt.Cost),
			unknown: attempt.Cost == nil && (!attempt.BillingPending || r.Usage == nil),
		}
	}
	return result
}

// recoverRecordCost 按历史价格快照重算全部可结算尝试；任一尝试仍缺事实时保持整笔未知。
func recoverRecordCost(r *Record) bool {
	if r == nil || len(r.Attempts) == 0 {
		return false
	}
	total := 0.0
	charges := []Charge{}
	billable := false
	for i := range r.Attempts {
		attempt := &r.Attempts[i]
		if attempt.BillingPending {
			continue
		}
		cost, parts := estimate(r.Pricing, attempt.Usage, attempt.Multiplier, r.Tier, time.UnixMilli(r.StartedAt))
		if cost == nil {
			return false
		}
		attempt.Cost, attempt.Charges = cost, parts
		total += *cost
		charges = append(charges, parts...)
		billable = true
	}
	if !billable {
		return false
	}
	r.Cost, r.Charges = &total, charges
	r.UnknownCosts = 0
	return true
}

// adjustKeyTotals 按尝试费用差量修正 Key 累计；累计缺失时报错，由调用方回滚整笔修复。
func adjustKeyTotals(ctx context.Context, tx *sql.Tx, s *Store, r Record, before []attemptCostSnapshot) error {
	day := time.UnixMilli(r.StartedAt).In(s.zone).Format("2006-01-02")
	for i, attempt := range r.Attempts {
		if attempt.KeyID == "" {
			continue
		}
		beforeCost := 0.0
		if before[i].cost != nil {
			beforeCost = *before[i].cost
		}
		afterCost := 0.0
		if attempt.Cost != nil {
			afterCost = *attempt.Cost
		}
		afterUnknown := attempt.Cost == nil && (!attempt.BillingPending || r.Usage == nil)
		costDelta := afterCost - beforeCost
		unknownDelta := 0
		if afterUnknown {
			unknownDelta++
		}
		if before[i].unknown {
			unknownDelta--
		}
		if costDelta == 0 && unknownDelta == 0 {
			continue
		}
		result, err := tx.ExecContext(ctx, `UPDATE request_key_totals SET cost=cost+?,unknown_cost=MAX(0,unknown_cost+?) WHERE day=? AND provider_id=? AND model_id=? AND protocol=? AND key_id=?`,
			costDelta, unknownDelta, day, attempt.ProviderID, r.ModelID, r.Protocol, attempt.KeyID)
		if err != nil {
			return fmt.Errorf("修正 Key 费用累计: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("核对 Key 费用累计: %w", err)
		}
		if count != 1 {
			return errors.New("Key 费用累计缺失，已取消费用修复")
		}
	}
	return nil
}

// adjustRequestTotals 将逻辑请求从未知费用修正为完整金额；累计缺失时报错，避免明细与长期累计不一致。
func adjustRequestTotals(ctx context.Context, tx *sql.Tx, s *Store, r Record) error {
	day := time.UnixMilli(r.StartedAt).In(s.zone).Format("2006-01-02")
	result, err := tx.ExecContext(ctx, `UPDATE request_totals SET cost=cost+?,unknown_cost=MAX(0,unknown_cost-1) WHERE day=? AND provider_id=? AND model_id=? AND protocol=?`,
		*r.Cost, day, r.ProviderID, r.ModelID, r.Protocol)
	if err != nil {
		return fmt.Errorf("修正请求费用累计: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("核对请求费用累计: %w", err)
	}
	if count != 1 {
		return errors.New("请求费用累计缺失，已取消费用修复")
	}
	return nil
}

// repairMissingCosts 一次性恢复因可选价格维度未启用而被误判未知的历史请求费用。
func (s *Store) repairMissingCosts(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var marker string
	err = tx.QueryRowContext(ctx, `SELECT document FROM settings WHERE name=?`, costRepairMarker).Scan(&marker)
	if err == nil {
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	last := ""
	for {
		rows, err := tx.QueryContext(ctx, `SELECT id,document FROM request_records WHERE ended_at>0 AND state NOT IN('active','local') AND cost IS NULL AND id>? ORDER BY id LIMIT 256`, last)
		if err != nil {
			return err
		}
		batch := []Record{}
		for rows.Next() {
			var id, raw string
			if err = rows.Scan(&id, &raw); err != nil {
				break
			}
			var r Record
			if err = json.Unmarshal([]byte(raw), &r); err != nil {
				break
			}
			if r.ID != id || r.EndedAt <= 0 || r.State == "active" || r.State == "local" {
				err = errors.New("历史请求正文与结算状态不一致")
				break
			}
			last = id
			batch = append(batch, r)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return fmt.Errorf("扫描待修复请求费用: %w", err)
		}
		for i := range batch {
			r := &batch[i]
			before := snapshotAttemptCosts(*r)
			if !recoverRecordCost(r) {
				continue
			}
			if err = adjustKeyTotals(ctx, tx, s, *r, before); err != nil {
				return err
			}
			if err = adjustRequestTotals(ctx, tx, s, *r); err != nil {
				return err
			}
			data, marshalErr := json.Marshal(*r)
			if marshalErr != nil {
				return marshalErr
			}
			if _, err = tx.ExecContext(ctx, `UPDATE request_records SET document=?,cost=? WHERE id=? AND cost IS NULL`, string(data), *r.Cost, r.ID); err != nil {
				return fmt.Errorf("保存修复后的请求费用: %w", err)
			}
		}
		if len(batch) < 256 {
			break
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO settings(name,document) VALUES(?,'done')`, costRepairMarker); err != nil {
		return fmt.Errorf("记录费用修复完成标记: %w", err)
	}
	return tx.Commit()
}
