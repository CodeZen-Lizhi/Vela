package records

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// detailArchive 保存独立于 SQLite 明细生命周期的脱敏正文。
type detailArchive struct {
	root string
}

// path 由内部生成的请求身份和真实结束时间定位文件。
func (a *detailArchive) path(id string, ended int64) string {
	day := time.UnixMilli(ended).UTC().Format("2006-01-02")
	return filepath.Join(a.root, day, fmt.Sprintf("%d-%s.json.zst", ended, id))
}

// write 完整写入并同步后才让读者看到压缩文件。
func (a *detailArchive) write(id string, ended int64, payloads map[int]Payload) error {
	if len(id) != 32 {
		return errors.New("正文请求身份无效")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return errors.New("正文请求身份无效")
	}
	path := a.path(id, ended)
	if err := os.MkdirAll(a.root, 0700); err != nil {
		return err
	}
	if err := os.Chmod(a.root, 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".pending-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	writer, err := zstd.NewWriter(file, zstd.WithEncoderConcurrency(1), zstd.WithEncoderCRC(true))
	if err == nil {
		err = errors.Join(json.NewEncoder(writer).Encode(payloads), writer.Close())
	}
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

// read 读取一笔请求的所有已归档往返。
func (a *detailArchive) read(id string, ended int64) (map[int]Payload, error) {
	if len(id) != 32 {
		return nil, errors.New("正文请求身份无效")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return nil, errors.New("正文请求身份无效")
	}
	file, err := os.Open(a.path(id, ended))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader, err := zstd.NewReader(file, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	var payloads map[int]Payload
	decoder := json.NewDecoder(reader)
	if err = decoder.Decode(&payloads); err != nil {
		return nil, err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("正文文件不完整或包含多余数据")
	}
	return payloads, nil
}

// cleanup 依据文件记录的结束时刻预览或删除正文。
func (a *detailArchive) cleanup(ctx context.Context, before int64, remove bool) (int64, error) {
	var count int64
	err := filepath.WalkDir(a.root, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if remove && entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), ".pending-") {
			return os.Remove(path)
		}
		if entry.IsDir() || !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json.zst") {
			return nil
		}
		stamp, _, _ := strings.Cut(entry.Name(), "-")
		ended, err := strconv.ParseInt(stamp, 10, 64)
		if err != nil || ended > before {
			return nil
		}
		if remove {
			if err = os.Remove(path); errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
		}
		count++
		return nil
	})
	return count, err
}

// retentionDays 读取持久化保留期限，旧安装默认保留七天。
func retentionDays(ctx context.Context, db *sql.DB, name string, fallback int) (int, error) {
	var days int
	err := db.QueryRowContext(ctx, "SELECT document FROM settings WHERE name=?", name).Scan(&days)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	return days, err
}

// putPayload 将活动正文暂存在请求会话，SQLite 只写脱敏头和状态。
func (s *session) putPayload(ctx context.Context, number int, payload Payload) error {
	if s.store.archive != nil {
		s.payloads[number] = payload
		payload.RequestBody, payload.ResponseBody = "", ""
		payload.BodyState = "unavailable"
	}
	return s.store.putPayload(ctx, s.record.ID, number, payload)
}

// archivePayloads 在请求结算时保存正文，失败单独记录，不改变执行结果。
func (s *session) archivePayloads(ctx context.Context) error {
	store := s.store
	store.archiveMu.Lock()
	defer store.archiveMu.Unlock()
	days, err := store.Retention(ctx, true)
	if err != nil || days == 0 {
		return err
	}
	err = store.archive.write(s.record.ID, s.record.EndedAt, s.payloads)
	state := "archived"
	if err != nil {
		state = "saveFailed"
	}
	_, updateErr := store.db.ExecContext(ctx, `UPDATE request_payloads SET document=json_set(document,'$.bodyState',?) WHERE request_id=?`, state, s.record.ID)
	return errors.Join(err, updateErr)
}

// Retention 返回使用记录或正文的保留天数。
func (s *Store) Retention(ctx context.Context, body bool) (int, error) {
	if body {
		return retentionDays(ctx, s.db, "request-body-retention", 7)
	}
	return retentionDays(ctx, s.db, "request-record-retention", 30)
}

// detailExpiryDays 给停止采集前已存在的文件保留原到期规则。
func (s *Store) detailExpiryDays(ctx context.Context) (int, error) {
	days, err := s.Retention(ctx, true)
	if err != nil || days != 0 {
		return days, err
	}
	return retentionDays(ctx, s.db, "request-body-previous-retention", 7)
}

// PreviewDetails 使用固定截止时间计算待删除正文数量。
func (s *Store) PreviewDetails(ctx context.Context, days int) (CleanupPreview, error) {
	if s.archive == nil || days < 0 || days > 3650 {
		return CleanupPreview{}, errors.New("正文清理范围无效")
	}
	before := retentionDeadline(s, days)
	count, err := s.archive.cleanup(ctx, before, false)
	return CleanupPreview{Before: before, Count: count}, err
}

// CleanupDetails 仅清理正文文件。
func (s *Store) CleanupDetails(ctx context.Context, before int64) (int64, error) {
	if s.archive == nil || before <= 0 || before > s.now().UnixMilli() {
		return 0, errors.New("正文清理截止时间无效")
	}
	s.archiveMu.Lock()
	defer s.archiveMu.Unlock()
	return s.archive.cleanup(ctx, before, true)
}

// SetRetention 保存确认后的期限，缩短期限时清理预览截止时间内的数据。
func (s *Store) SetRetention(ctx context.Context, body bool, days int, before int64) error {
	if body {
		s.archiveMu.Lock()
		defer s.archiveMu.Unlock()
	}
	if days < 0 || days > 3650 || !body && days == 0 {
		return errors.New("保留天数无效")
	}
	current, err := s.Retention(ctx, body)
	if err != nil {
		return err
	}
	if body && current == 0 && days > 0 {
		current, err = s.detailExpiryDays(ctx)
		if err != nil {
			return err
		}
	}
	if days > 0 && days < current {
		if before <= 0 || before > retentionDeadline(s, days) {
			return errors.New("请先预览受影响的数据")
		}
		if body {
			_, err = s.archive.cleanup(ctx, before, true)
		} else {
			_, err = s.Cleanup(ctx, before)
		}
		if err != nil {
			return err
		}
	}
	name := "request-record-retention"
	if body {
		name = "request-body-retention"
		if days == 0 && current > 0 {
			if _, err = s.db.ExecContext(ctx, `INSERT INTO settings(name,document) VALUES('request-body-previous-retention',?) ON CONFLICT(name) DO UPDATE SET document=excluded.document`, strconv.Itoa(current)); err != nil {
				return err
			}
		}
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO settings(name,document) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET document=excluded.document`, name, strconv.Itoa(days))
	return err
}

// migrateDetails 逐条归档并回读校验，全部完成后收回旧数据库空闲页。
func (s *Store) migrateDetails(ctx context.Context) error {
	var done int
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM settings WHERE name='request-body-archive-v1')`).Scan(&done); err != nil || done != 0 {
		return err
	}
	bodyDays, err := s.detailExpiryDays(ctx)
	if err != nil {
		return err
	}
	cutoff := retentionDeadline(s, bodyDays)
	var cursor int64
	for {
		var id string
		var ended int64
		err := s.db.QueryRowContext(ctx, `SELECT r.rowid,r.id,r.ended_at FROM request_records r WHERE r.rowid>? AND r.ended_at>? AND EXISTS(SELECT 1 FROM request_payloads p WHERE p.request_id=r.id AND COALESCE(json_extract(p.document,'$.bodyState'),'')!='archived') ORDER BY r.rowid LIMIT 1`, cursor, cutoff).Scan(&cursor, &id, &ended)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return err
		}
		if err = s.migrateRequest(ctx, id, ended); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE request_payloads SET document=json_set(document,'$.requestBody','','$.responseBody','','$.bodyState','expired') WHERE COALESCE(json_extract(document,'$.bodyState'),'') NOT IN ('archived','expired')`); err != nil {
		return err
	}
	var free int
	if err := s.db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&free); err != nil {
		return err
	}
	if free > 0 {
		if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO settings(name,document) VALUES('request-body-archive-v1','1')`)
	return err
}

// migrateRequest 在验证正文文件与原始快照一致后移除该请求的旧正文。
func (s *Store) migrateRequest(ctx context.Context, id string, ended int64) error {
	rows, err := s.db.QueryContext(ctx, `SELECT number,document FROM request_payloads WHERE request_id=?`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	payloads := make(map[int]Payload)
	for rows.Next() {
		var number int
		var raw string
		var payload Payload
		if err = rows.Scan(&number, &raw); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(raw), &payload); err != nil {
			return err
		}
		if payload.BodyState == "" {
			payload.BodyState = "unavailable"
			if payload.RequestBody != "" || payload.ResponseBody != "" {
				payload.BodyState = "available"
			}
		}
		payloads[number] = payload
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if err = s.archive.write(id, ended, payloads); err != nil {
		return err
	}
	verified, err := s.archive.read(id, ended)
	if err != nil {
		return err
	}
	if !maps.Equal(payloads, verified) {
		return errors.New("迁移后的正文与原记录不一致")
	}
	_, err = s.db.ExecContext(ctx, `UPDATE request_payloads SET document=json_set(document,'$.requestBody','','$.responseBody','','$.bodyState','archived') WHERE request_id=?`, id)
	return err
}
