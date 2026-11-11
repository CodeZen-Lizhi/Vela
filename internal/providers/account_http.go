package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/outbound"
)

// accountHTTP 持有一次查询使用的出口、认证和连接快照；关闭由创建者负责。
type accountHTTP struct {
	// client 与 transport 为本轮 HTTP 资源；config 是仅后端使用的连接。
	client    *http.Client
	transport *http.Transport
	config    QueryConfig
	// headers 保存本轮认证，rotated 是续期后需要条件写回的新令牌。
	headers map[string]string
	rotated string
}

// connection 按 Key→供应商→全局解析有效出口，不读取环境代理或泄露代理认证。
func (a *Accounts) connection(ctx context.Context, c QueryConfig, p ProxyChoice, parent ProxyChoice) (*accountHTTP, error) {
	if p.Mode == ProxyProvider || p.Mode == "" {
		p = parent
	}
	var proxy *url.URL
	if p.Mode == ProxyGlobal {
		if a.globalProxy != nil {
			enabled, raw, e := a.globalProxy(ctx)
			if e != nil {
				return nil, errors.New("无法读取全局代理")
			}
			if enabled {
				var e error
				proxy, e = outbound.ParseProxy(raw)
				if e != nil {
					return nil, errors.New("全局代理地址无效")
				}
			}
		}
	} else if p.Mode != ProxyDirect {
		return nil, errors.New("无效网络代理方式")
	}
	t := outbound.Transport(proxy)
	h := &accountHTTP{transport: t, client: &http.Client{Transport: t, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, config: c, headers: map[string]string{}}
	return h, nil
}

// request 请求 JSON 并消除包含 URL、认证或响应正文的外部错误；内部固定路径可含分页参数。
func (h *accountHTTP) request(ctx context.Context, method, path, body string) (any, error) {
	target := h.config.BaseURL + path
	if path == "" {
		target = h.config.URL
	}
	req, e := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	if e != nil {
		return nil, errors.New("查询请求配置无效")
	}
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, e := h.client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, errors.New("查询取消或超时")
		}
		return nil, errors.New("网络查询失败，请检查站点和出口")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("上游 HTTP %d（请检查认证、权限或接口支持）", resp.StatusCode)
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
	if e != nil || len(data) > 4*1024*1024 {
		return nil, errors.New("响应读取失败或超过 4 MiB")
	}
	var result any
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if dec.Decode(&result) != nil {
		return nil, errors.New("上游未返回有效 JSON")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, errors.New("上游 JSON 含多余内容")
	}
	// 已签到是幂等正常结果，只识别固定词，不向调用方回显外部消息。
	if path == "/api/user/checkin" {
		message := strings.ToLower(textValue(object(result)["message"]))
		if strings.Contains(message, "已签到") || strings.Contains(message, "already checked") || strings.Contains(message, "already signed") {
			return map[string]any{"success": true}, nil
		}
	}

	for _, flag := range []string{"success", "is_active", "isValid"} {
		if v, ok := object(result)[flag].(bool); ok && !v {
			return nil, errors.New("上游业务失败或凭据失效")
		}
	}
	if code, ok := object(result)["code"]; ok {
		v, valid := number(code)
		if !valid || v != 0 {
			return nil, errors.New("上游业务失败，请检查认证与权限")
		}
	}
	return result, nil
}

// authenticate 交换 Sub2API 令牌或安装固定认证；轮换令牌只保留后端内存待条件写回。
func (h *accountHTTP) authenticate(ctx context.Context) error {
	c := h.config
	switch c.Mode {
	case "sub2api":
		body, _ := json.Marshal(map[string]string{"refresh_token": c.RefreshToken})
		v, e := h.request(ctx, "POST", "/api/v1/auth/refresh", string(body))
		if e != nil {
			return e
		}
		d := object(unwrap(v))
		token := textValue(d["access_token"])
		if token == "" {
			return errors.New("续期未返回访问令牌，请重新配置")
		}
		h.rotated = textValue(d["refresh_token"])
		h.headers["Authorization"] = "Bearer " + token
	case "newapi":
		h.headers["Authorization"] = "Bearer " + c.AccessToken
		h.headers["New-Api-User"] = c.UserID
	case "usage_api":
		h.headers["Authorization"] = "Bearer " + c.APIKey
	case "custom":
		if c.Headers != "" {
			if json.Unmarshal([]byte(c.Headers), &h.headers) != nil {
				return errors.New("请求头 JSON 无效")
			}
		}
	}
	return nil
}

// object 安全访问上游 JSON 对象，其他类型作为字段缺失处理。
func object(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// unwrap 兼容带 data 包裹和无包裹响应。
func unwrap(v any) any {
	if d, ok := object(v)["data"]; ok {
		return d
	}
	return v
}

// field 只遍历点分隔对象成员，不执行 JSONPath 或数组脚本。
func field(v any, path string) any {
	if path == "" {
		return nil
	}
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			return nil
		}
		v = object(v)[part]
	}
	return v
}

// number 只接受有限 JSON 数值或可解析数值字符串，拒绝 null、布尔值和空串。
func number(v any) (float64, bool) {
	var s string
	switch n := v.(type) {
	case json.Number:
		s = string(n)
	case string:
		s = strings.TrimSpace(n)
	case float64:
		return n, finite(n)
	default:
		return 0, false
	}
	f, e := strconv.ParseFloat(s, 64)
	return f, e == nil && finite(f)
}

// amount 在字段真实存在且为有效数值时返回指针，零值不视为空。
func amount(v any) *float64 {
	n, ok := number(v)
	if !ok {
		return nil
	}
	return &n
}

// textValue 只接受非空字符串名称，不将其他字段类型转换为名称。
func textValue(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// successStatus 生成带本次成功时间的字段状态。
func successStatus() QueryStatus { return QueryStatus{State: "success", LastSuccess: time.Now().UTC()} }

// failedStatus 标记失败但保留最后成功时间。
func failedStatus(old QueryStatus, e error) QueryStatus {
	old.State = "failed"
	old.Error = e.Error()
	return old
}

// balance 查询余额、订阅和可选签到；独立子项失败不抹除已成功余额。
func (h *accountHTTP) balance(ctx context.Context, old AccountBalance, checkin bool) (AccountBalance, error) {
	c := h.config
	r := old
	if c.Mode == "newapi" && checkin {
		_, e := h.request(ctx, "POST", "/api/user/checkin", "")
		if e != nil {
			r.Checkin = failedStatus(r.Checkin, e)
		} else {
			r.Checkin = successStatus()
		}
	}
	var v any
	var e error
	switch c.Mode {
	case "sub2api":
		v, e = h.request(ctx, "GET", "/api/v1/auth/me?timezone=Asia/Shanghai", "")
	case "newapi":
		v, e = h.request(ctx, "GET", "/api/user/self", "")
	case "usage_api":
		v, e = h.request(ctx, "GET", "/v1/usage", "")
	case "custom":
		body := ""
		if c.Method == "POST" {
			body = c.Body
		}
		v, e = h.request(ctx, c.Method, "", body)
	}
	if e != nil {
		return r, e
	}
	d := unwrap(v)
	var n *float64
	currency := "USD"
	if c.Mode == "custom" {
		currency = c.Currency
	}
	switch c.Mode {
	case "sub2api":
		cash := amount(field(d, "balance"))
		points := amount(field(d, "points"))
		if cash != nil || points != nil {
			total := 0.0
			if cash != nil {
				total += *cash
			}
			if points != nil {
				total += *points
			}
			if finite(total) {
				n = &total
				r.Cash = cash
				r.Points = points
			}
		}
	case "newapi":
		if quota := amount(field(d, "quota")); quota != nil {
			x := *quota / c.QuotaDivisor
			if finite(x) {
				n = &x
			}
		}
		if used := amount(field(d, "used_quota")); used != nil {
			x := *used / c.QuotaDivisor
			if finite(x) {
				r.Used = &x
			}
		}
	case "usage_api":
		for _, path := range []string{"remaining", "quota.remaining", "balance"} {
			if x := amount(field(d, path)); x != nil {
				n = x
				break
			}
		}
		for _, path := range []string{"unit", "quota.unit"} {
			if s := textValue(field(d, path)); s != "" {
				currency = s
				break
			}
		}
	case "custom":
		n = amount(field(v, c.BalancePath))
	}
	if n == nil {
		return r, errors.New("余额字段缺失或不是有限数值")
	}
	if currency == "" {
		currency = "USD"
	}
	// 不同适配器的币种和细项各有契约，切换成功不能沿用上一方式的展示字段。
	if c.Mode != "sub2api" {
		r.Cash, r.Points = nil, nil
	}
	if c.Mode != "newapi" {
		r.Used = nil
	}
	r.Amount = n
	r.Currency = currency
	r.Status = successStatus()
	if c.Mode == "sub2api" {
		sub, e := h.request(ctx, "GET", "/api/v1/subscriptions/summary", "")
		if e != nil {
			r.SubscriptionStatus = failedStatus(r.SubscriptionStatus, e)
		} else {
			safe := subscriptionFields(unwrap(sub))
			data, e := json.Marshal(safe)
			if e != nil {
				return r, errors.New("订阅结果格式无效")
			}
			r.Subscription = string(data)
			r.SubscriptionStatus = successStatus()
		}
	}
	return r, nil
}

// subscriptionFields 只保留订阅的额度、周期、到期和展示身份，防止意外返回认证字段。
func subscriptionFields(v any) any {
	switch x := v.(type) {
	case []any:
		r := make([]any, 0, len(x))
		for _, item := range x {
			r = append(r, subscriptionFields(item))
		}
		return r
	case map[string]any:
		r := map[string]any{}
		for k, item := range x {
			switch k {
			case "active_count", "total_used_usd", "group_name", "daily_used_usd", "weekly_used_usd", "monthly_used_usd", "subscriptions", "active_subscriptions", "subscription", "group", "id", "name", "group_id", "status", "expires_at", "start_at", "end_at", "daily_limit_usd", "weekly_limit_usd", "monthly_limit_usd", "daily_usage_usd", "weekly_usage_usd", "monthly_usage_usd", "daily_limit", "weekly_limit", "monthly_limit", "daily_usage", "weekly_usage", "monthly_usage", "daily_reset_at", "weekly_reset_at", "monthly_reset_at":
				r[k] = subscriptionFields(item)
			}
		}
		return r
	default:
		return v
	}
}

// keyInformation 是已可靠归属当前 Key 的上游字段；缺失名称或倍率分别失败。
type keyInformation struct {
	// name、group、rate 分别对应上游 Key 名、分组名与有效倍率。
	name  string
	group string
	rate  *float64
	// rateError 为分组接口等失败原因，不妨碍有效名称同步。
	rateError error
}

// keyInfo 按方式读取当前 Key 信息；usage API 始终使用当前调用密钥而非余额认证。
func (h *accountHTTP) keyInfo(ctx context.Context, secret string, q ProviderKeyQuery) (keyInformation, error) {
	if secret == "" {
		return keyInformation{}, errors.New("当前 Key 尚未设置调用密钥")
	}
	if h.config.Mode == "usage_api" {
		h.headers["Authorization"] = "Bearer " + secret
		v, e := h.request(ctx, "GET", "/v1/sub2api/billing", "")
		return keyInformation{rate: amount(field(unwrap(v), "effective_rate_multiplier"))}, e
	}
	if h.config.Mode == "custom" {
		body := ""
		if h.config.Method == "POST" {
			body = h.config.Body
		}
		v, e := h.request(ctx, h.config.Method, "", body)
		if e != nil {
			return keyInformation{}, e
		}
		if q.ReuseProvider {
			full, ok := field(v, q.KeyPath).(string)
			if !ok || full != secret {
				return keyInformation{}, errors.New("无法可靠匹配完整 Key，请单独配置信息查询")
			}
		}
		return keyInformation{name: textValue(field(v, q.NamePath)), group: textValue(field(v, q.GroupPath)), rate: amount(field(v, q.MultiplierPath))}, nil
	}
	return h.listedKey(ctx, secret)
}

// listedKey 完整读取最多 100 页并唯一匹配；不完整、重复或未能还原掩码时拒绝猜测归属。
func (h *accountHTTP) listedKey(ctx context.Context, secret string) (keyInformation, error) {
	sub := h.config.Mode == "sub2api"
	var matched map[string]any
	count := 0
	complete := false
	seen := 0
	for page := 1; page <= 100; page++ {
		path := fmt.Sprintf("/api/token/?p=%d&size=100", page)
		if sub {
			path = fmt.Sprintf("/api/v1/keys?page=%d&page_size=100", page)
		}
		v, e := h.request(ctx, "GET", path, "")
		if e != nil {
			return keyInformation{}, e
		}
		d := unwrap(v)
		items, ok := d.([]any)
		if !ok {
			items, ok = object(d)["items"].([]any)
		}
		if !ok {
			return keyInformation{}, errors.New("上游 Key 列表格式无效")
		}
		for _, item := range items {
			record := object(item)
			full, _ := record["key"].(string)
			if full == "" {
				return keyInformation{}, errors.New("上游未返回完整 Key，无法证明唯一匹配")
			}
			if strings.Contains(full, "*") {
				if sub {
					return keyInformation{}, errors.New("上游仅提供掩码，无法可靠匹配")
				}
				id, ok := number(record["id"])
				if !ok || id <= 0 || id != float64(int64(id)) {
					return keyInformation{}, errors.New("上游 Key ID 无效")
				}
				v, e := h.request(ctx, "POST", fmt.Sprintf("/api/token/%d/key", int64(id)), "")
				if e != nil {
					return keyInformation{}, e
				}
				full, _ = field(unwrap(v), "key").(string)
				if full == "" || strings.Contains(full, "*") {
					return keyInformation{}, errors.New("上游未返回完整 Key")
				}
			}
			candidate, wanted := full, secret
			if !sub {
				candidate = strings.TrimPrefix(candidate, "sk-")
				wanted = strings.TrimPrefix(wanted, "sk-")
			}
			if candidate != "" && candidate == wanted {
				count++
				matched = record
			}
		}
		seen += len(items)
		total, hasTotal := number(object(d)["total"])
		if hasTotal && (total < 0 || total != float64(int(total))) {
			return keyInformation{}, errors.New("上游分页总数无效")
		}
		if (hasTotal && float64(seen) >= total) || (!hasTotal && !sub && len(items) < 100) {
			complete = true
			break
		}
		if len(items) == 0 {
			return keyInformation{}, errors.New("上游 Key 分页不完整")
		}
	}
	if !complete {
		return keyInformation{}, errors.New("上游 Key 分页不完整或超过 100 页")
	}
	if count != 1 {
		return keyInformation{}, errors.New("完整 Key 无匹配或重复匹配，未更新本地值")
	}
	r := keyInformation{name: textValue(matched["name"])}
	if !sub {
		r.group = textValue(matched["group"])
		if r.group == "" || r.group == "auto" {
			r.rateError = errors.New("空分组或 auto 不支持固定倍率")
			return r, nil
		}
		v, e := h.request(ctx, "GET", "/api/user/self/groups", "")
		if e != nil {
			v, e = h.request(ctx, "GET", "/api/user/groups", "")
		}
		if e != nil {
			r.rateError = e
			return r, nil
		}
		r.rate = amount(field(object(unwrap(v))[r.group], "ratio"))
		return r, nil
	}
	groupID, ok := number(matched["group_id"])
	if !ok || groupID <= 0 {
		r.rateError = errors.New("上游未返回有效分组")
		return r, nil
	}
	group := object(matched["group"])
	v, e := h.request(ctx, "GET", "/api/v1/groups/available", "")
	if e != nil {
		r.rateError = e
	} else if groups, ok := unwrap(v).([]any); ok {
		for _, item := range groups {
			if id, ok := number(object(item)["id"]); ok && id == groupID {
				if group == nil {
					group = object(item)
				}
				if r.group == "" {
					r.group = textValue(object(item)["name"])
				}
			}
		}
	}
	if name := textValue(group["name"]); name != "" {
		r.group = name
	}
	v, e = h.request(ctx, "GET", "/api/v1/groups/rates", "")
	if e != nil {
		r.rateError = e
		return r, nil
	}
	rates := object(unwrap(v))
	if rate, ok := rates[strconv.FormatFloat(groupID, 'f', -1, 64)]; ok {
		r.rate = amount(rate)
	} else {
		r.rate = amount(group["rate_multiplier"])
	}
	return r, nil
}
