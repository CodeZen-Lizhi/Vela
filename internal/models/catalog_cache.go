package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"
)

// catalogCacheVersion 随目录解析契约变化递增，避免新版沿用旧解析结果。
const catalogCacheVersion = 3

// BasellmCatalogURL 与 New API 使用同一 basellm 数据库，完整目录同时提供资料和结构化价格。
const BasellmCatalogURL = "https://basellm.github.io/llm-metadata/api/all.json"

// catalogSource 是新增模型与成功同步记录的目录来源名称。
const catalogSource = "basellm"

// CatalogStatus 区分检查时间、内容变更时间、后台状态和失败，供页面轻量轮询。
type CatalogStatus struct {
	// Source 当前在线数据源名称。
	Source string `json:"source"`
	// CheckedAt 最近成功检查的 UTC 时间，不将网络失败伪装成成功检查。
	CheckedAt string `json:"checkedAt"`
	// UpdatedAt 当前缓存内容最近改变的 UTC 时间。
	UpdatedAt string `json:"updatedAt"`
	// Refreshing 是否有共享的更新请求正在进行。
	Refreshing bool `json:"refreshing"`
	// Error 最近更新失败原因；失败时仍保留有效缓存。
	Error string `json:"error"`
	// Failures 个别模型未同步的具体原因。
	Failures []string `json:"failures"`
	// Generation 每次完成检查或发生失败后递增，供前端刷新本地快照。
	Generation int64 `json:"generation"`
}

// catalogCache 将内容和校验标记一起保存，URL 变化时不误用其他来源的条件请求。
type catalogCache struct {
	// Version 标记已解析目录的格式版本，不兼容时重新下载解析。
	Version int `json:"version"`
	// URL、ETag、Modified 与 Hash 表达同一来源版本；Entries 为已校验的完整目录。
	URL      string         `json:"url"`
	ETag     string         `json:"etag"`
	Modified string         `json:"modified"`
	Hash     string         `json:"hash"`
	Entries  []CatalogEntry `json:"entries"`
	// ExchangeRates 是 basellm manifest 提供的每美元兑换单位，和目录共同决定价格版本。
	ExchangeRates map[string]float64 `json:"exchangeRates,omitempty"`
	// Status 在重启后保留更新时间与失败原因，运行态另行重置。
	Status CatalogStatus `json:"status"`
}

// loadCatalogCache 从本地持久目录恢复；损坏数据不阻止启动，但页面明确提示并允许重新下载。
func (s *Store) loadCatalogCache(ctx context.Context) {
	var data string
	err := s.db.QueryRowContext(ctx, "SELECT document FROM settings WHERE name='model-catalog-cache'").Scan(&data)
	if err == nil {
		var cached catalogCache
		if json.Unmarshal([]byte(data), &cached) != nil {
			s.catalogState.Status.Error = "模型目录缓存无效，请重新同步"
		} else if cached.URL == s.catalogURL && cached.Version == catalogCacheVersion {
			cached.Status.Refreshing = false
			if cached.Status.Failures == nil {
				cached.Status.Failures = []string{}
			}
			s.catalogState = cached
			s.catalog = cached.Entries
			s.catalogAttempt, _ = time.Parse(time.RFC3339Nano, cached.Status.CheckedAt)
		}
	}
	// 首次下载失败时没有目录行，独立状态仍需恢复；过期格式缓存不携带其旧更新时间。
	var savedStatus string
	if err = s.db.QueryRowContext(ctx, "SELECT document FROM settings WHERE name='model-catalog-status'").Scan(&savedStatus); err == nil {
		var state catalogCache
		if json.Unmarshal([]byte(savedStatus), &state) == nil && state.URL == s.catalogURL && state.Version == catalogCacheVersion {
			s.catalogState.Status = state.Status
			s.catalogState.Status.Refreshing = false
			if len(s.catalog) == 0 {
				s.catalogState.Status.UpdatedAt = ""
			}
		}
	}
}

// CatalogStatus 只读取内存中的目录状态，不访问网络。
func (s *Store) CatalogStatus() CatalogStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.catalogState.Status
	status.Refreshing = s.catalogFlight != nil
	status.Failures = append([]string{}, status.Failures...)
	return status
}

// ConfigureCatalogUpdates 立即应用自动更新策略并唤醒任务；关闭后不再发起自动联网。
func (s *Store) ConfigureCatalogUpdates(enabled bool, minutes int) {
	s.mu.Lock()
	s.catalogAuto = enabled
	if minutes > 0 {
		s.catalogMinutes = minutes
	}
	s.mu.Unlock()
	select {
	case s.catalogWake <- struct{}{}:
	default:
	}
}

// RunCatalogUpdates 由应用生命周期管理，定时检查也覆盖休眠唤醒后的到期任务。
func (s *Store) RunCatalogUpdates(ctx context.Context) error {
	s.CheckCatalog()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.catalogWake:
			s.CheckCatalog()
		case <-ticker.C:
			s.CheckCatalog()
		}
	}
}

// CheckCatalog 仅在开启且到期时异步启动检查；多个入口共用同一个网络任务。
func (s *Store) CheckCatalog() CatalogStatus {
	s.mu.Lock()
	due := s.catalogAuto && !s.closed && s.catalogURL != "" && (s.catalogAttempt.IsZero() || time.Since(s.catalogAttempt) >= time.Duration(s.catalogMinutes)*time.Minute)
	if due && s.catalogFlight == nil {
		s.startCatalogRefreshLocked()
	}
	s.mu.Unlock()
	return s.CatalogStatus()
}

// Catalog 优先返回本地候选；首次无缓存时等待一次共享下载，关闭自动更新也允许首次下载。
func (s *Store) Catalog(ctx context.Context) ([]CatalogEntry, error) {
	s.mu.Lock()
	if len(s.catalog) > 0 {
		entries := activeCatalog(s.catalog)
		s.mu.Unlock()
		s.CheckCatalog()
		return entries, nil
	}
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("模型目录已关闭")
	}
	if s.catalogFlight == nil {
		s.startCatalogRefreshLocked()
	}
	flight := s.catalogFlight
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-flight:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.catalog) == 0 {
		return nil, errors.New(s.catalogState.Status.Error)
	}
	return activeCatalog(s.catalog), nil
}

// activeCatalog 只对新增隐藏弃用项；完整目录仍可为已添加模型同步价格。
func activeCatalog(entries []CatalogEntry) []CatalogEntry {
	result := make([]CatalogEntry, 0, len(entries))
	for _, e := range entries {
		if !e.Deprecated {
			result = append(result, e)
		}
	}
	return result
}

// Sync 手动强制执行一次共享检查并同步已有模型，不受自动更新开关或间隔限制。
func (s *Store) Sync(ctx context.Context) (SyncResult, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return SyncResult{}, errors.New("模型目录已关闭")
	}
	if s.catalogFlight == nil {
		s.startCatalogRefreshLocked()
	}
	flight := s.catalogFlight
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return SyncResult{}, ctx.Err()
	case <-flight:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.catalogResult, s.catalogError
}

// startCatalogRefreshLocked 登记任务后再启动，关闭模块时会取消并等待网络退出。
func (s *Store) startCatalogRefreshLocked() {
	s.catalogFlight = make(chan struct{})
	s.catalogAttempt = time.Now()
	go func() {
		result, err := s.refreshCatalog(s.catalogContext)
		s.mu.Lock()
		s.catalogResult, s.catalogError = result, err
		s.catalogState.Status.Generation++
		if err != nil {
			s.catalogState.Status.Error = err.Error()
		}
		if statusErr := s.persistCatalogStatusLocked(); statusErr != nil && !s.closed {
			s.catalogState.Status.Error = fmt.Sprintf("%s 保存更新状态失败: %v", s.catalogState.Status.Error, statusErr)
			if s.catalogError == nil {
				s.catalogError = statusErr
			}
		}
		flight := s.catalogFlight
		s.catalogFlight = nil
		close(flight)
		s.mu.Unlock()
	}()
}

// refreshCatalog 执行条件请求，校验后才替换缓存并同步价格；网络期间不占数据库写入锁。
func (s *Store) refreshCatalog(ctx context.Context) (SyncResult, error) {
	result := SyncResult{Failures: []string{}}
	s.mu.Lock()
	cached := s.catalogState
	s.mu.Unlock()
	proxy, err := s.GetProxy(ctx)
	if err != nil {
		return result, err
	}
	transport, err := proxyTransport(proxy)
	if err != nil {
		return result, err
	}
	defer transport.CloseIdleConnections()
	client := *s.client
	client.Transport = transport
	if client.Timeout == 0 {
		client.Timeout = 45 * time.Second
	}
	exchangeRates, err := fetchCatalogExchangeRates(ctx, &client, s.catalogURL)
	if err != nil {
		return result, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.catalogURL, nil)
	if err != nil {
		return result, err
	}
	if len(cached.Entries) > 0 && reflect.DeepEqual(cached.ExchangeRates, exchangeRates) {
		req.Header.Set("If-None-Match", cached.ETag)
		req.Header.Set("If-Modified-Since", cached.Modified)
	}
	response, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("无法更新模型目录: %w", err)
	}
	defer response.Body.Close()
	next := cached
	next.Version = catalogCacheVersion
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if response.StatusCode == http.StatusNotModified && len(cached.Entries) > 0 {
		// 内容未变仍可将缓存应用到新建或待迁移的本地模型。
	} else {
		if response.StatusCode != http.StatusOK {
			return result, fmt.Errorf("模型目录返回 HTTP %d", response.StatusCode)
		}
		const limit = 32 << 20
		data, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
		if readErr != nil {
			return result, readErr
		}
		if len(data) > limit {
			return result, errors.New("模型目录超过32MB，保留原数据")
		}
		rateData, err := json.Marshal(exchangeRates)
		if err != nil {
			return result, err
		}
		hash := sha256.Sum256(append(data, rateData...))
		sum := hex.EncodeToString(hash[:])
		if sum != cached.Hash || len(cached.Entries) == 0 {
			entries, decodeErr := decodeCatalogData(data, exchangeRates)
			if decodeErr != nil {
				return result, decodeErr
			}
			if len(entries) == 0 {
				return result, errors.New("来源没有可用模型，保留原目录")
			}
			next.Entries = entries
			next.Hash = sum
			next.Status.UpdatedAt = now
		}
		next.ExchangeRates = exchangeRates
		next.URL = s.catalogURL
		next.ETag = response.Header.Get("ETag")
		next.Modified = response.Header.Get("Last-Modified")
	}
	next.Status.Source = catalogSource
	next.Status.CheckedAt = now
	next.Status.Error = ""
	next.Status.Failures = []string{}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 缓存先持久化，失败时不更新运行态与价格；保存内容包含上一次成功状态。
	data, err := json.Marshal(next)
	if err != nil {
		return result, err
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO settings(name,document) VALUES('model-catalog-cache',?) ON CONFLICT(name) DO UPDATE SET document=excluded.document", string(data)); err != nil {
		return result, fmt.Errorf("保存模型目录失败: %w", err)
	}
	// 每次手动/自动检查可重试上次个别模型失败，但未改变规则时不会递增模型版本。
	result, err = s.syncEntries(ctx, next.Entries, next.Status.UpdatedAt)
	if err != nil {
		return result, err
	}
	next.Status.Failures = result.Failures
	next.Status.Generation = s.catalogState.Status.Generation
	s.catalogState = next
	s.catalog = next.Entries
	return result, nil
}

// decodeCatalogData 读取 basellm 完整目录；LiteLLM 格式仅兼容旧快照和隔离测试，不提供联网回退。
func decodeCatalogData(data []byte, exchangeRates map[string]float64) ([]CatalogEntry, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("模型目录格式错误: %w", err)
	}
	for _, raw := range root {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) == nil && obj["models"] != nil {
			return decodeProviderCatalog(data, exchangeRates)
		}
	}
	return decodeLiteCatalog(data)
}

// persistCatalogStatusLocked 单独持久化最终检查结果，不以失败状态替换有效目录内容。
func (s *Store) persistCatalogStatusLocked() error {
	state := catalogCache{Version: catalogCacheVersion, URL: s.catalogURL, Status: s.catalogState.Status}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(s.catalogContext, "INSERT INTO settings(name,document) VALUES('model-catalog-status',?) ON CONFLICT(name) DO UPDATE SET document=excluded.document", string(data))
	return err
}

// fetchCatalogExchangeRates 读取同一 basellm 发布物的汇率；自定义隔离来源不以 all.json 结尾时无需额外请求。
func fetchCatalogExchangeRates(ctx context.Context, client *http.Client, catalogURL string) (map[string]float64, error) {
	if !strings.HasSuffix(catalogURL, "/all.json") {
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(catalogURL, "all.json")+"manifest.json", nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法读取 basellm 汇率: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("basellm 汇率返回 HTTP %d", response.StatusCode)
	}
	var manifest struct {
		// NewAPI 使用与 New API 官方预设一致的汇率，避免把人民币单价当作美元。
		NewAPI struct {
			ExchangeRates map[string]float64 `json:"exchangeRates"`
		} `json:"newapi"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("basellm 汇率格式错误: %w", err)
	}
	for currency, rate := range manifest.NewAPI.ExchangeRates {
		if !validPrice(rate, true) {
			return nil, fmt.Errorf("basellm %s 汇率无效", currency)
		}
	}
	return manifest.NewAPI.ExchangeRates, nil
}
