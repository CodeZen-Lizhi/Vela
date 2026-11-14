package records

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/CodeZen-Lizhi/vela/internal/models"
	"strings"
	"sync"
	"time"
)

// schema 记录不外键关联配置，删除资源不删历史；正文与列表分表，累计不依赖明细。
const schema = `
CREATE TABLE IF NOT EXISTS request_records(
 id TEXT PRIMARY KEY, started_at INTEGER NOT NULL, ended_at INTEGER NOT NULL,
 model_id TEXT NOT NULL, provider_id TEXT NOT NULL, protocol TEXT NOT NULL,
 source TEXT NOT NULL, state TEXT NOT NULL, search TEXT NOT NULL, document TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS request_records_time ON request_records(started_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS request_records_end ON request_records(ended_at) WHERE ended_at>0;
CREATE INDEX IF NOT EXISTS request_records_model ON request_records(model_id,started_at DESC);
CREATE INDEX IF NOT EXISTS request_records_provider ON request_records(provider_id,started_at DESC);
CREATE TABLE IF NOT EXISTS request_options(kind TEXT NOT NULL,value TEXT NOT NULL,label TEXT NOT NULL,PRIMARY KEY(kind,value));
CREATE TABLE IF NOT EXISTS request_payloads(
 request_id TEXT NOT NULL REFERENCES request_records(id) ON DELETE CASCADE,
 number INTEGER NOT NULL, document TEXT NOT NULL, PRIMARY KEY(request_id,number)
);
CREATE TABLE IF NOT EXISTS request_circuits(key_id TEXT PRIMARY KEY,version TEXT NOT NULL,request_id TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS request_totals(
 day TEXT NOT NULL, provider_id TEXT NOT NULL, model_id TEXT NOT NULL, protocol TEXT NOT NULL,
 provider_name TEXT NOT NULL, model_name TEXT NOT NULL,
 requests INTEGER NOT NULL, tokens INTEGER NOT NULL, cost REAL NOT NULL,
 unknown_usage INTEGER NOT NULL, unknown_cost INTEGER NOT NULL,
 PRIMARY KEY(day,provider_id,model_id,protocol)
);`

// Store 复用宿主 SQLite，负责请求与统计原子结算、正文期限及有界查询。
type Store struct {
	// db 的生命周期由宿主管理；now 可在隔离测试控制，zone 首次确定后固定。
	db   *sql.DB
	now  func() time.Time
	zone *time.Location
}

// session 是一个逻辑请求的内存事实，HTTP 包装器在真实出站处附加尝试。
type session struct {
	// mu 保护并发读写；store、record、secrets 仅存活于请求生命周期。
	mu      sync.Mutex
	store   *Store
	record  Record
	secrets []string
	// resourceQuery 为入口确认的非生成资源操作，只在会话内控制零模型消耗结算。
	resourceQuery bool
	// binaryResponse 指示当前响应仅留元信息；没有 HTTP 头的客户端出口也不得保存媒体字节。
	binaryResponse bool
	// mediaSpecification、mediaQuality 让视频轮询沿用创建任务时的实际规格。
	mediaSpecification string
	mediaQuality       string
	// writeErr 保留采集写入失败，不能伪造记录完整成功。
	writeErr error
	// clientOutput 保存实际提交给客户端的文本，随逻辑请求释放。
	clientOutput *responseWriter
}

// sessionKey、targetKey 是私有 context 键，防止调用方伪造请求记录。
type sessionKey struct{}
type targetKey struct{}

// attemptKey 关联一次 Runner 调用；未真正出站时不能改写前一次尝试。
type attemptKey struct{}

// attemptScope 的 number 在真实出站登记时赋值，由 session.mu 保护。
type attemptScope struct {
	// number 为当前 Runner 最后一次真实出站序号，零表示尚未出站。
	number int
}

// New 建立表并回填长期概览，再清理上次进程中断的活跃状态；不能将中断冒充成功。
func New(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("请求记录数据库尚未初始化")
	}
	if _, err := db.Exec(schema + keySchema); err != nil {
		return nil, fmt.Errorf("初始化请求记录: %w", err)
	}
	name := time.Now().Location().String()
	if name == "Local" {
		name = time.Now().Format("-07:00")
	}
	// 时区使用首次保存的 IANA 名或固定偏移；后续系统时区变化不改统计日。
	if _, err := db.Exec(`INSERT OR IGNORE INTO settings(name,document) VALUES('records-timezone',?)`, name); err != nil {
		return nil, err
	}
	if err := db.QueryRow(`SELECT document FROM settings WHERE name='records-timezone'`).Scan(&name); err != nil {
		return nil, err
	}
	zone, err := time.LoadLocation(name)
	if err != nil {
		if t, e := time.Parse("-07:00", name); e == nil {
			_, offset := t.Zone()
			zone = time.FixedZone(name, offset)
		} else {
			return nil, errors.New("请求统计时区无效")
		}
	}
	s := &Store{db: db, now: clockNow, zone: zone}
	if err = s.migrateMetrics(); err != nil {
		return nil, err
	}
	if err = s.repairMissingCosts(context.Background()); err != nil {
		return nil, err
	}
	if err = s.migrateOverview(); err != nil {
		return nil, err
	}
	// 恢复只读取未完成记录；历史正文仅在首次概览迁移扫描。
	rows, err := db.Query(`SELECT document FROM request_records WHERE state='active'`)
	if err != nil {
		return nil, err
	}
	pending := []Record{}
	for rows.Next() {
		var raw string
		var r Record
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal([]byte(raw), &r)
		}
		if err != nil {
			break
		}
		pending = append(pending, r)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, r := range pending {
		r.State = "failed"
		r.Error = "应用退出时请求未完成，最终上游结果未知"
		r.Usage = nil
		r.Cost = nil
		r.EndedAt = s.now().UnixMilli()
		r.DurationMillis = r.EndedAt - r.StartedAt
		if err = s.save(context.Background(), r, true); err != nil {
			return nil, err
		}
	}
	return s, s.Expire(context.Background())
}

// WithTarget 关联实际候选并新建执行结果作用域；AttemptResult 必须使用本次返回的 context。
func WithTarget(ctx context.Context, t Target) context.Context {
	ctx = context.WithValue(ctx, targetKey{}, t)
	return context.WithValue(ctx, attemptKey{}, &attemptScope{})
}

// HasSession 表示入口已建立逻辑记录；嵌套执行器应复用它，由入口写完最终响应后统一收尾。
func HasSession(ctx context.Context) bool {
	run, _ := ctx.Value(sessionKey{}).(*session)
	return run != nil
}

// Begin 在出站前保存进行中请求和开始时价格，失败时拒绝继续产生不可记录的调用。
func (s *Store) Begin(ctx context.Context, in Input) (context.Context, func(error) error, error) {
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return ctx, nil, err
	}
	path, _, _ := strings.Cut(in.Path, "?")
	r := Record{Method: in.Method, Path: path, ID: hex.EncodeToString(key[:]), StartedAt: s.now().UnixMilli(), ModelID: in.ModelID, Model: in.ModelID, Source: in.Source, Protocol: in.Protocol, Type: in.Type, Stream: in.Stream, Transport: in.Transport, State: "active", Execution: "direct", Attempts: []Attempt{}, Charges: []Charge{}}
	r.ProviderID = in.ProviderID
	r.KeyID = in.KeyID
	if in.ResourceQuery {
		r.Usage = &Usage{}
		zero := 0.0
		r.Cost = &zero
	}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT document FROM models WHERE id=? OR model_id=? LIMIT 1`, in.ModelID, in.ModelID).Scan(&raw)
	if err == nil {
		var m models.Model
		if err = json.Unmarshal([]byte(raw), &m); err != nil {
			return ctx, nil, err
		}
		if m.ID != "" {
			r.ModelID = m.ID
		}
		if m.ModelID != "" {
			r.Model = m.ModelID
		}
		r.ModelName = m.Name
		r.Pricing = m.Pricing
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ctx, nil, err
	}
	var body map[string]any
	_ = json.Unmarshal(in.Body, &body)
	r.Effort, _ = body["reasoning_effort"].(string)
	r.Tier, _ = body["service_tier"].(string)
	if reasoning, ok := body["reasoning"].(map[string]any); ok && r.Effort == "" {
		r.Effort, _ = reasoning["effort"].(string)
	}
	if r.Source == "" {
		r.Source = "business"
	}
	if r.Type == "" {
		r.Type = "chat"
	}
	if err = s.save(ctx, r, false); err != nil {
		return ctx, nil, err
	}
	client := Payload{RequestHeaders: safeHeaders(in.Headers), RequestBody: sanitize(string(in.Body), headerSecrets(in.Headers)), BodyState: "available"}
	if len(in.Body) == 0 {
		client.BodyState = "unavailable"
	} else if len(in.Body) > captureLimit {
		client.BodyState = "truncated"
	}
	if err = s.putPayload(ctx, r.ID, 0, client); err != nil {
		return ctx, nil, err
	}
	run := &session{store: s, record: r, secrets: headerSecrets(in.Headers), resourceQuery: in.ResourceQuery}
	return context.WithValue(ctx, sessionKey{}, run), run.finish, nil
}

// putPayload 保存受独立保留期限控制的内容，不把正文塞进可检索摘要。
func (s *Store) putPayload(ctx context.Context, id string, number int, p Payload) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO request_payloads VALUES(?,?,?) ON CONFLICT(request_id,number) DO UPDATE SET document=excluded.document`, id, number, string(data))
	return err
}

// save 将结束标记、小时概览及长期日累计在一个事务结算，重复收尾不重复计数。
func (s *Store) save(ctx context.Context, r Record, final bool) error {
	r.Client = nil
	for i := range r.Attempts {
		r.Attempts[i].Payload = nil
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	search := strings.ToLower(strings.Join([]string{r.ID, r.ModelID, r.Model, r.ModelName, r.UpstreamModel, r.ProviderID, r.ProviderName, r.KeyID, r.KeyName, r.Protocol, r.Type, r.Source, r.Error, r.Execution, r.CircuitSourceID}, " "))
	for _, a := range r.Attempts {
		search += " " + strings.ToLower(a.Path+" "+a.Error+" "+a.ProviderName+" "+a.KeyName)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	err = tx.QueryRowContext(ctx, `SELECT state FROM request_records WHERE id=?`, r.ID).Scan(&state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && state != "active" {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO request_records(id,started_at,ended_at,model_id,provider_id,protocol,source,state,search,document,input_context,output_tokens,cache_read,cost,usage_known,duration_ms)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET ended_at=excluded.ended_at,provider_id=excluded.provider_id,state=excluded.state,search=excluded.search,document=excluded.document,input_context=excluded.input_context,output_tokens=excluded.output_tokens,cache_read=excluded.cache_read,cost=excluded.cost,usage_known=excluded.usage_known,duration_ms=excluded.duration_ms`, r.ID, r.StartedAt, r.EndedAt, r.ModelID, r.ProviderID, r.Protocol, r.Source, r.State, search, string(data), contextTokens(r), outputTokens(r), cacheTokens(r), r.Cost, r.Usage != nil, r.DurationMillis)
	if err != nil {
		return err
	}
	for _, option := range []struct{ kind, value, label string }{{"model", r.ModelID, r.ModelName}, {"provider", r.ProviderID, r.ProviderName}, {"protocol", r.Protocol, r.Protocol}, {"type", r.Type, r.Type}} {
		if option.value != "" {
			name := option.label
			if name == "" {
				name = option.value
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO request_options VALUES(?,?,?) ON CONFLICT(kind,value) DO UPDATE SET label=excluded.label`, option.kind, option.value, name); err != nil {
				return err
			}
		}
	}
	if final {
		if err = s.saveOverview(ctx, tx, r); err != nil {
			return err
		}
		if err = s.saveKeyTotals(ctx, tx, r); err != nil {
			return err
		}
		tokens := int64(0)
		unknownUsage, unknownCost := 0, 0
		cost := 0.0
		if r.Usage != nil {
			tokens = r.Usage.Context + r.Usage.Output
		} else if r.State != "local" {
			unknownUsage = 1
		}
		if r.Cost != nil {
			cost = *r.Cost
		} else if r.State != "local" {
			unknownCost = 1
		}
		day := time.UnixMilli(r.StartedAt).In(s.zone).Format("2006-01-02")
		_, err = tx.ExecContext(ctx, `INSERT INTO request_totals VALUES(?,?,?,?,?,?,1,?,?,?,?) ON CONFLICT(day,provider_id,model_id,protocol) DO UPDATE SET requests=requests+1,tokens=tokens+excluded.tokens,cost=cost+excluded.cost,unknown_usage=unknown_usage+excluded.unknown_usage,unknown_cost=unknown_cost+excluded.unknown_cost,provider_name=excluded.provider_name,model_name=excluded.model_name`, day, r.ProviderID, r.ModelID, r.Protocol, r.ProviderName, r.ModelName, tokens, cost, unknownUsage, unknownCost)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// finish 以执行端结果收尾；取消时仍落库，失败不篡改已产生用量。
func (s *session) finish(cause error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &s.record
	r.EndedAt = s.store.now().UnixMilli()
	r.DurationMillis = r.EndedAt - r.StartedAt
	r.State = "success"
	if cause != nil {
		r.State = "failed"
		r.Error = sanitize(cause.Error(), s.secrets)
	}
	if len(r.Attempts) == 0 {
		r.State = "local"
	}
	if len(r.Attempts) > 1 {
		retry, transfer := false, false
		for i := 1; i < len(r.Attempts); i++ {
			a, b := r.Attempts[i-1], r.Attempts[i]
			if b.Polling {
				continue
			}
			if a.KeyID == b.KeyID && a.ProviderID == b.ProviderID {
				retry = true
			} else {
				transfer = true
			}
		}
		if retry && transfer {
			r.Execution = "both"
		} else if transfer {
			r.Execution = "transfer"
		} else if retry {
			r.Execution = "retry"
		}
	}
	// 仅本地未发出的请求可确定零上游费用，未知上游费用保持未知。
	if r.State == "local" {
		zero := 0.0
		r.Cost = &zero
	}
	// 任一次尝试未提供完整用量时，总用量保持未知；已知分项仍保留在尝试详情。
	for _, a := range r.Attempts {
		// Realtime 未升级成功的握手没有生成会话，保留其未知用量，不抹去后来实际连接的累计值。
		if r.Protocol == "openai:realtime" && a.Status != 101 {
			continue
		}
		if a.Usage == nil && !a.BillingPending {
			r.Usage = nil
			break
		}
	}
	if r.Usage != nil && r.FirstByteMillis != nil && r.Usage.Output > 0 {
		duration := r.DurationMillis
		if r.Stream {
			duration -= *r.FirstByteMillis
		}
		if duration > 0 {
			rate := float64(r.Usage.Output) * 1000 / float64(duration)
			if !(r.Stream && float64(duration)/float64(max(1, r.DurationMillis)) < 0.1 && rate > 5000) {
				r.OutputRate = &rate
			}
		}
	}
	// 仅清理请求派生内容；整条 JSON 替换会误伤与凭据重合的配置名称、身份及字段名。
	texts := []*string{&r.UpstreamModel, &r.Effort, &r.Tier, &r.Error}
	usages := []*Usage{r.Usage}
	for i := range r.Attempts {
		a := &r.Attempts[i]
		texts = append(texts, &a.Path, &a.Error)
		usages = append(usages, a.Usage)
	}
	for i := range r.Candidates {
		texts = append(texts, &r.Candidates[i].Reason)
	}
	for _, usage := range usages {
		if usage != nil {
			texts = append(texts, &usage.Quality, &usage.Specification)
		}
	}
	for _, text := range texts {
		for _, secret := range s.secrets {
			*text = redact(*text, secret)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.redactSaved(ctx); err != nil {
		return err
	}
	if err := s.saveClient(ctx); err != nil {
		return err
	}
	if s.writeErr != nil {
		return fmt.Errorf("保存请求诊断失败: %w", s.writeErr)
	}
	if err := s.store.save(ctx, *r, true); err != nil {
		return fmt.Errorf("调用已结束，但保存请求记录失败: %w", err)
	}
	return nil
}

// Expire 清理到期正文和明细，累计统计不依赖明细，不受删除影响。
func (s *Store) Expire(ctx context.Context) error {
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE request_payloads SET document=json_set(document,'$.requestBody','','$.responseBody','','$.bodyState','expired') WHERE request_id IN(SELECT id FROM request_records WHERE ended_at>0 AND ended_at<=?)`, now.Add(-7*24*time.Hour).UnixMilli())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM request_records WHERE ended_at>0 AND ended_at<=?`, now.Add(-30*24*time.Hour).UnixMilli())
	if err != nil {
		return err
	}
	if err = pruneOptions(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Preview 返回明确截止时刻的已结束记录数量；days 仅允许 0、7、30。
func (s *Store) Preview(ctx context.Context, days int) (CleanupPreview, error) {
	if days != 0 && days != 7 && days != 30 {
		return CleanupPreview{}, errors.New("清理范围无效")
	}
	p := CleanupPreview{Before: s.now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()}
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_records WHERE ended_at>0 AND ended_at<=?`, p.Before).Scan(&p.Count)
	return p, err
}

// Cleanup 仅删除确认截止时间内已结束明细；同事务移除失效筛选候选，不清配置或长期统计。
func (s *Store) Cleanup(ctx context.Context, before int64) (int64, error) {
	if before <= 0 || before > s.now().UnixMilli() {
		return 0, errors.New("清理截止时间无效，请重新预览")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM request_records WHERE ended_at>0 AND ended_at<=?`, before)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err = pruneOptions(ctx, tx); err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

// pruneOptions 仅保留仍有明细的筛选值；表列来自内部固定映射。
func pruneOptions(ctx context.Context, tx *sql.Tx) error {
	for _, item := range []struct{ kind, column string }{{"model", "model_id"}, {"provider", "provider_id"}, {"protocol", "protocol"}, {"type", "json_extract(document,'$.type')"}} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM request_options WHERE kind=? AND NOT EXISTS(SELECT 1 FROM request_records WHERE `+item.column+`=request_options.value)`, item.kind); err != nil {
			return err
		}
	}
	return nil
}

// Run 执行启动后每小时的到期清理，退出取消后结束，不占用常驻请求资源。
func (s *Store) Run(ctx context.Context) error {
	timer := time.NewTicker(time.Hour)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if err := s.Expire(ctx); err != nil {
				return err
			}
		}
	}
}
