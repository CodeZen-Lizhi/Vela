package providers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// UpstreamModelsResult 是仅供候选选择的上游名称，不修改本地模型、关联或映射。
type UpstreamModelsResult struct {
	// ModelNames 是去重并按名称排序的请求名；成功空列表序列化为 []。
	ModelNames []string `json:"modelNames"`
	// Warning 提示部分 Key 查询失败，不包含上游响应正文或秘密。
	Warning string `json:"warning"`
	// FromCache 表示本次命中当前有效连接配置的成功缓存。
	FromCache bool `json:"fromCache"`
}

// upstreamModelsCache 保存配置摘要、成功结果和五分钟有效期；不保存明文凭据。
type upstreamModelsCache struct {
	// fingerprint 隔离凭据、地址和实际出口；expires 是成功结果的失效时刻。
	fingerprint [32]byte
	expires     time.Time
	// result 的切片在读写缓存时复制，避免调用者修改缓存。
	result UpstreamModelsResult
}

// UpstreamModelClient 复用宿主持有的连接池；返回客户端由宿主关闭。
type UpstreamModelClient func(context.Context, Provider, ProviderKey) (*http.Client, error)

// upstreamModelSource 是查询快照中的启用 Key 和对应连接，不持有明文密钥。
type upstreamModelSource struct {
	// row 保存密文及 Key 配置；client 是有效出口；err 是脱敏连接配置错误。
	row    keyRow
	client *http.Client
	err    error
}

// UpstreamModels 查询全部启用且已设置凭据的 Key；整段查询复用供应商请求超时，未配置时为 20 秒，失败不覆盖旧缓存。
func (s *Store) UpstreamModels(ctx context.Context, providerID string, forceRefresh bool, clientFor UpstreamModelClient) (UpstreamModelsResult, error) {
	if clientFor == nil {
		return UpstreamModelsResult{}, errors.New("上游模型查询连接尚未初始化")
	}
	p, rows, err := s.upstreamModelSnapshot(ctx, providerID)
	if err != nil {
		return UpstreamModelsResult{}, err
	}
	seconds := p.Request.RequestSeconds
	if seconds <= 0 {
		seconds = 20
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	sources := []upstreamModelSource{}
	signature, err := json.Marshal(p)
	if err != nil {
		return UpstreamModelsResult{}, errors.New("供应商连接配置无效")
	}
	for _, row := range rows {
		if !row.enabled || len(row.sealed) == 0 {
			continue
		}
		client, e := clientFor(ctx, p, row.doc.key(row.id, providerID, true, row.allModels, "", true))
		sources = append(sources, upstreamModelSource{row: row, client: client, err: e})
		config, marshalErr := json.Marshal(row.doc)
		if marshalErr != nil {
			return UpstreamModelsResult{}, errors.New("上游 Key 配置无效")
		}
		signature = append(signature, []byte(fmt.Sprintf("%s:%s:%p:%t:", row.id, executionVersion(row.sealed), client, e != nil))...)
		signature = append(signature, config...)
	}
	if len(sources) == 0 {
		return UpstreamModelsResult{}, errors.New("请先配置并启用至少一把上游 Key")
	}
	fingerprint := sha256.Sum256(signature)
	s.mu.Lock()
	cached, ok := s.upstreamCache[providerID]
	s.mu.Unlock()
	if !forceRefresh && ok && cached.fingerprint == fingerprint && time.Now().Before(cached.expires) {
		result := cached.result
		result.ModelNames = slices.Clone(result.ModelNames)
		result.FromCache = true
		return result, nil
	}
	result := UpstreamModelsResult{ModelNames: []string{}}
	failures := 0
	successes := 0
	for _, source := range sources {
		if ctx.Err() != nil {
			return UpstreamModelsResult{}, errors.New("上游模型查询已取消或超时")
		}
		if source.err != nil || source.client == nil {
			failures++
			continue
		}
		// 发送前重新读取快照，配置变化后拒绝沿用旧连接或凭据。
		current, currentRows, e := s.upstreamModelSnapshot(ctx, providerID)
		if e != nil {
			return UpstreamModelsResult{}, e
		}
		found := slices.ContainsFunc(currentRows, func(r keyRow) bool {
			return r.id == source.row.id && r.enabled && executionVersion(r.sealed) == executionVersion(source.row.sealed) && r.doc.Proxy == source.row.doc.Proxy
		})
		if !found || current.URL != p.URL {
			return UpstreamModelsResult{}, errors.New("供应商或 Key 配置已变化，请重新查询")
		}
		master, e := s.existingMasterKey()
		if e != nil {
			failures++
			continue
		}
		secret, e := source.row.revealed(master)
		if e != nil {
			failures++
			continue
		}
		names, e := fetchUpstreamModels(ctx, source.client, p, secret)
		if e != nil {
			failures++
			continue
		}
		successes++
		result.ModelNames = append(result.ModelNames, names...)
	}
	if ctx.Err() != nil {
		return UpstreamModelsResult{}, errors.New("上游模型查询已取消或超时")
	}
	if successes == 0 {
		return UpstreamModelsResult{}, errors.New("所有上游 Key 查询失败，请检查认证、网络或模型列表接口支持")
	}
	slices.Sort(result.ModelNames)
	result.ModelNames = slices.Compact(result.ModelNames)
	if failures > 0 {
		result.Warning = fmt.Sprintf("%d 把 Key 查询失败，已保留其余 Key 的模型", failures)
	}
	s.mu.Lock()
	if s.upstreamCache == nil {
		s.upstreamCache = map[string]upstreamModelsCache{}
	}
	stored := result
	stored.ModelNames = slices.Clone(result.ModelNames)
	s.upstreamCache[providerID] = upstreamModelsCache{fingerprint: fingerprint, expires: time.Now().Add(5 * time.Minute), result: stored}
	s.mu.Unlock()
	return result, nil
}

// upstreamModelSnapshot 在短临界区读取供应商及 Key，网络请求期间不占数据库锁。
func (s *Store) upstreamModelSnapshot(ctx context.Context, id string) (Provider, []keyRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p Provider
	var raw string
	err := s.db.QueryRowContext(ctx, "SELECT id,name,enabled,document FROM providers WHERE id=?", id).Scan(&p.ID, &p.Name, &p.Enabled, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil, errProviderMissing
	}
	if err != nil {
		return p, nil, err
	}
	var doc providerDocument
	if err = s.decodeDocument([]byte(raw), &doc); err != nil {
		return p, nil, err
	}
	p = doc.provider(p.ID, p.Name, p.Enabled)
	rows, err := s.keyRows(ctx, id)
	return p, rows, err
}

// fetchUpstreamModels 因供应商未指定协议，按常见协议选择首个完整成功的列表。
func fetchUpstreamModels(ctx context.Context, client *http.Client, p Provider, secret string) ([]string, error) {
	for _, protocol := range []string{"openai", "anthropic", "gemini"} {
		names, err := fetchUpstreamModelPages(ctx, client, p.URL, secret, protocol)
		if err == nil {
			return names, nil
		}
		if ctx.Err() != nil {
			return nil, errors.New("上游模型查询取消或超时")
		}
	}
	return nil, errors.New("上游模型列表查询失败")
}

// fetchUpstreamModelPages 读取协议分页；分页游标只作为参数，不接受上游提供的任意目标地址。
func fetchUpstreamModelPages(ctx context.Context, client *http.Client, base, secret, protocol string) ([]string, error) {
	path := "/v1/models"
	if protocol == "gemini" {
		path = "/v1beta/models"
	}
	// 复制客户端只固定本次重定向策略，继续共享宿主连接池。
	safeClient := *client
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	h := accountHTTP{client: &safeClient, config: QueryConfig{BaseURL: strings.TrimRight(base, "/")}, headers: map[string]string{}}
	switch protocol {
	case "openai":
		h.headers["Authorization"] = "Bearer " + secret
	case "anthropic":
		h.headers["x-api-key"] = secret
		h.headers["anthropic-version"] = "2023-06-01"
	case "gemini":
		h.headers["x-goog-api-key"] = secret
	}
	names := []string{}
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 100; page++ {
		target := path
		if cursor != "" {
			param := "after_id"
			if protocol == "gemini" {
				param = "pageToken"
			}
			target += "?" + param + "=" + url.QueryEscape(cursor)
		}
		v, err := h.request(ctx, http.MethodGet, target, "")
		if err != nil {
			return nil, err
		}
		root := object(v)
		fieldName := "data"
		if protocol == "gemini" {
			fieldName = "models"
		}
		items, ok := root[fieldName].([]any)
		if !ok {
			return nil, errors.New("上游模型列表格式无效")
		}
		for _, item := range items {
			name := textValue(object(item)["id"])
			if protocol == "gemini" {
				name = strings.TrimPrefix(textValue(object(item)["name"]), "models/")
			}
			if name == "" {
				return nil, errors.New("上游模型名称缺失")
			}
			names = append(names, name)
		}
		next := ""
		if protocol == "gemini" {
			next = textValue(root["nextPageToken"])
		} else if more, _ := root["has_more"].(bool); more {
			next = textValue(root["last_id"])
			if next == "" {
				return nil, errors.New("上游分页缺少游标")
			}
		}
		if next == "" {
			return names, nil
		}
		if seen[next] {
			return nil, errors.New("上游分页游标重复")
		}
		seen[next] = true
		cursor = next
	}
	return nil, errors.New("上游模型分页超过 100 页")
}
