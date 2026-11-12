package modeltest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/records"
)

// schema 只保留每个供应商关联模型的最新诊断；解除关联或删除模型时同步清理。
const schema = `CREATE TABLE IF NOT EXISTS provider_model_tests (
 provider_id TEXT NOT NULL, -- 所属供应商稳定身份
 model_id TEXT NOT NULL, -- 已关联统一模型稳定身份
 completed_at TEXT NOT NULL, -- RFC3339Nano 完成时刻，供最近结果展示
 document TEXT NOT NULL, -- 已脱敏测试结果，不包含明文认证
 PRIMARY KEY(provider_id,model_id),
 FOREIGN KEY(provider_id,model_id) REFERENCES provider_models(provider_id,model_id) ON DELETE CASCADE
);`

// ClientFor 复用应用的 Key→供应商→全局代理解析与连接池，诊断不创建另一份代理规则。
type ClientFor func(context.Context, providers.ExecutionCandidate) (*http.Client, error)

// Manager 管理主动测试、取消及最近结果；不参与正式流量准入或健康结算。
type Manager struct {
	// Records 由宿主在运行前注入，独立请求明细不随最近诊断结果删除。
	Records *records.Store
	// db、providers、clientFor 由应用生命周期持有，退出前需先 Close 取消在途测试。
	db        *sql.DB
	providers *providers.Store
	clientFor ClientFor
	// mu 保护请求标识和取消函数；同一供应商模型同时只运行一个测试。
	mu     sync.Mutex
	active map[string]activeRun
	closed bool
	wg     sync.WaitGroup
}

// activeRun 关联一次请求的取消入口及目标，防止重复点击同时诊断同一模型。
type activeRun struct {
	// target 为供应商与模型组合；cancel 取消当前测试全部剩余尝试。
	target string
	cancel context.CancelFunc
}

// New 初始化诊断结果表；不读取或调用任何上游。
func New(db *sql.DB, store *providers.Store, clients ClientFor) (*Manager, error) {
	if db == nil || store == nil || clients == nil {
		return nil, errors.New("模型测试依赖尚未初始化")
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("初始化模型测试: %w", err)
	}
	return &Manager{db: db, providers: store, clientFor: clients, active: map[string]activeRun{}}, nil
}

// mappingNames 按优先级、请求名生成诊断顺序，无配置时回退默认请求名。
func mappingNames(model providers.ProviderModel) []string {
	mappings := slices.Clone(model.Mappings)
	sort.SliceStable(mappings, func(i, j int) bool {
		if mappings[i].Priority != mappings[j].Priority {
			return mappings[i].Priority < mappings[j].Priority
		}
		return mappings[i].RequestName < mappings[j].RequestName
	})
	names := []string{}
	for _, mapping := range mappings {
		names = append(names, mapping.RequestName)
	}
	if len(names) == 0 {
		names = append(names, model.ModelName)
	}
	return names
}

// Options 返回当前模型及可选 Key、映射，停用对象仍可由用户主动诊断。
func (m *Manager) Options(ctx context.Context, providerID, modelID string) (Options, error) {
	snapshot, err := m.providers.Diagnostic(ctx, providerID, modelID)
	if err != nil {
		return Options{}, err
	}
	options := Options{ModelName: snapshot.Model.ModelName, Kind: snapshot.Model.Kind, Keys: []KeyOption{}, Mappings: mappingNames(snapshot.Model)}
	for _, c := range snapshot.Keys {
		options.Keys = append(options.Keys, KeyOption{Value: c.Key.ID, Label: c.Key.Name})
	}
	return options, nil
}

// Results 重读当前供应商所有关联模型的最近诊断，首次使用返回空数组。
func (m *Manager) Results(ctx context.Context, providerID string) ([]Result, error) {
	rows, err := m.db.QueryContext(ctx, "SELECT document FROM provider_model_tests WHERE provider_id=? ORDER BY model_id", providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []Result{}
	for rows.Next() {
		var raw string
		var result Result
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &result); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

// Cancel 只停止指定请求，不取消其他模型的测试；已完成请求无需额外处理。
func (m *Manager) Cancel(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if active, ok := m.active[id]; ok {
		active.cancel()
	}
}

// Close 停止并等待全部诊断，必须先于关闭共享数据库执行。
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	for _, run := range m.active {
		run.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

// Run 按映射优先、Key 次序实际尝试，成功即停止；输入错误不发请求，最终结果脱敏持久化。
func (m *Manager) Run(ctx context.Context, request Request) (result Result, err error) {
	if m.Records == nil {
		return m.run(ctx, request)
	}
	kind := request.Protocol
	if slices.Contains([]string{"chat", "responses", "anthropic", "gemini"}, kind) {
		kind = "chat"
	}
	ctx, finish, err := m.Records.Begin(ctx, records.Input{ModelID: request.ModelID, ProviderID: request.ProviderID, KeyID: request.KeyID, Source: "modeltest", Protocol: request.Protocol, Type: kind, Body: []byte(request.RequestBody)})
	if err != nil {
		return result, err
	}
	result, err = m.run(ctx, request)
	cause := err
	if cause == nil && result.State != "success" {
		cause = errors.New(result.Error)
	}
	return result, errors.Join(err, finish(cause))
}

// run 保留主动诊断的权限复核、取消、映射顺序及最近结果保存契约。
func (m *Manager) run(ctx context.Context, request Request) (Result, error) {
	request.ID = strings.TrimSpace(request.ID)
	if request.ID == "" || len(request.ID) > 128 {
		return Result{}, errors.New("测试请求标识无效")
	}
	if len(request.RequestBody) > 1<<20 || len(request.RequestHeaders) > 64<<10 || len(request.Message) > 64<<10 {
		return Result{}, errors.New("测试内容超过允许大小")
	}
	// 先登记取消入口，数据库准备期间点击停止也能阻止后续真实出站。
	ctx, cancel := context.WithCancel(ctx)
	target := request.ProviderID + ":" + request.ModelID
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		return Result{}, errors.New("模型测试已停止")
	}
	for id, run := range m.active {
		if id == request.ID || run.target == target {
			m.mu.Unlock()
			cancel()
			return Result{}, errors.New("该模型已有测试正在执行")
		}
	}
	m.active[request.ID] = activeRun{target: target, cancel: cancel}
	m.wg.Add(1)
	m.mu.Unlock()
	defer func() { cancel(); m.mu.Lock(); delete(m.active, request.ID); m.mu.Unlock(); m.wg.Done() }()
	snapshot, err := m.providers.Diagnostic(ctx, request.ProviderID, request.ModelID)
	if err != nil {
		return Result{}, err
	}
	allowed := map[string][]string{"chat": {"chat", "responses", "anthropic", "gemini"}, "image": {"image"}, "video": {"video"}, "vector": {"embedding", "rerank"}}
	if !slices.Contains(allowed[snapshot.Model.Kind], request.Protocol) {
		return Result{}, errors.New("所选协议与模型类型不兼容")
	}
	keys := snapshot.Keys
	if request.KeyID != "" {
		keys = slices.DeleteFunc(slices.Clone(keys), func(c providers.ExecutionCandidate) bool { return c.Key.ID != request.KeyID })
	}
	if len(keys) == 0 {
		return Result{}, errors.New("没有可测试的 Key，请检查密钥与模型权限")
	}
	names := mappingNames(snapshot.Model)
	if request.RequestModel != "" {
		if !slices.Contains(names, request.RequestModel) {
			return Result{}, errors.New("所选映射已不存在，请刷新后重试")
		}
		names = []string{request.RequestModel}
	}
	if err = validateRequest(request); err != nil {
		return Result{}, err
	}
	request.firstByteSeconds = snapshot.Provider.Request.FirstByteSeconds
	if request.firstByteSeconds == 0 {
		request.firstByteSeconds = 30
	}
	seconds := snapshot.Provider.Request.RequestSeconds
	if seconds == 0 {
		seconds = 300
	}
	runCtx, stopDeadline := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer stopDeadline()
	started := time.Now()
	usedSecrets := []string{}
	result := Result{ID: request.ID, ProviderID: request.ProviderID, ModelID: request.ModelID, Protocol: request.Protocol, State: "failed", Attempts: []Attempt{}}
	for _, name := range names {
		for _, key := range keys {
			result.Attempts = append(result.Attempts, Attempt{KeyID: key.Key.ID, KeyName: key.Key.Name, RequestModel: name, State: "unused"})
		}
	}
	for i := range result.Attempts {
		attempt := &result.Attempts[i]
		if runCtx.Err() != nil {
			break
		}
		key := keys[i%len(keys)]
		attemptStarted := time.Now()
		// 每次出站复核最新关联、映射和 Key 权限，历史候选不得携带已撤销的权限执行。
		fresh, checkErr := m.providers.Diagnostic(runCtx, request.ProviderID, request.ModelID)
		if checkErr == nil && !slices.Contains(mappingNames(fresh.Model), attempt.RequestModel) {
			checkErr = errors.New("模型映射已变化，请重新测试")
		}
		var secret string
		if checkErr == nil {
			secret, checkErr = m.providers.DiagnosticSecret(runCtx, request.ProviderID, request.ModelID, key.Key.ID, key.Version)
		}
		if checkErr != nil {
			attempt.State = "failed"
			attempt.Error = checkErr.Error()
			result.Error = attempt.Error
			continue
		}
		// 复用当前出口配置；配置更新后不继续使用此前的代理快照。
		currentKey := slices.IndexFunc(fresh.Keys, func(c providers.ExecutionCandidate) bool { return c.Key.ID == key.Key.ID })
		if currentKey < 0 {
			attempt.State = "failed"
			attempt.Error = "Key 权限已变化"
			result.Error = attempt.Error
			continue
		}
		client, clientErr := m.clientFor(runCtx, fresh.Keys[currentKey])
		if clientErr != nil {
			attempt.State = "failed"
			attempt.Error = "无法建立测试出口，请检查网络代理设置"
			result.Error = attempt.Error
			continue
		}
		result.RequestModel = attempt.RequestModel
		result.KeyName = key.Key.Name
		usedSecrets = append(usedSecrets, secret)
		output := runHTTP(records.WithTarget(runCtx, records.Target{ProviderID: fresh.Provider.ID, ProviderName: fresh.Provider.Name, KeyID: key.Key.ID, KeyName: key.Key.Name, Multiplier: key.Key.Multiplier}), client, request, fresh.Provider.URL, attempt.RequestModel, secret)
		attempt.Status = output.Status
		attempt.DurationMillis = time.Since(attemptStarted).Milliseconds()
		result.Status = output.Status
		result.FirstByteMillis = output.FirstByteMillis
		result.RequestBody = output.RequestBody
		result.RequestHeaders = output.RequestHeaders
		result.ResponseBody = output.ResponseBody
		if output.Error != nil {
			attempt.State = "failed"
			if errors.Is(runCtx.Err(), context.Canceled) {
				attempt.State = "cancelled"
			}
			attempt.Error = output.Error.Error()
			result.Error = attempt.Error
			continue
		}
		attempt.State = "success"
		result.State = "success"
		result.Error = ""
		break
	}
	if runCtx.Err() != nil {
		result.State = "cancelled"
		result.Error = "测试已取消"
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			result.State = "failed"
			result.Error = "测试超过总超时时间"
		}
	}
	// 最终正文可能回显先前尝试的认证，持久化前对全部实际使用过的密钥再次脱敏。
	for _, secret := range usedSecrets {
		redact := secretRedactor(secret)
		result.RequestBody = redact(result.RequestBody)
		result.RequestHeaders = redact(result.RequestHeaders)
		result.ResponseBody = redact(result.ResponseBody)
		result.Error = redact(result.Error)
		for i := range result.Attempts {
			result.Attempts[i].Error = redact(result.Attempts[i].Error)
		}
	}
	result.DurationMillis = time.Since(started).Milliseconds()
	result.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if result.State == "failed" && result.Error == "" {
		result.Error = "没有完成有效的上游响应"
	}
	// 取消后仍需保存明确结果；仅写仍存在的关联，解除关联期间完成的请求不复活旧数据。
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer saveCancel()
	data, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	_, err = m.db.ExecContext(saveCtx, `INSERT INTO provider_model_tests(provider_id,model_id,completed_at,document)
 SELECT ?,?,?,? WHERE EXISTS(SELECT 1 FROM provider_models WHERE provider_id=? AND model_id=?)
 ON CONFLICT(provider_id,model_id) DO UPDATE SET completed_at=excluded.completed_at,document=excluded.document`, request.ProviderID, request.ModelID, result.CompletedAt, string(data), request.ProviderID, request.ModelID)
	if err != nil {
		return result, fmt.Errorf("测试已完成，但保存结果失败: %w", err)
	}
	return result, nil
}
