package records

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// captureLimit 是单个正文快照的上限，不限制真实转发的响应大小。
const captureLimit = 1 << 20

// Transport 在真实出站位置旁路记录，未关联逻辑请求时原样转发。
type Transport struct {
	// Base 为项目已有代理与 TLS 传输；不改变认证、重试或重定向规则。
	Base http.RoundTripper
}

// RoundTrip 只观察调用方实际读取的响应，记录失败由逻辑请求收尾传播。
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	run, _ := req.Context().Value(sessionKey{}).(*session)
	if run == nil {
		return base.RoundTrip(req)
	}
	target, _ := req.Context().Value(targetKey{}).(Target)
	secrets := headerSecrets(req.Header)
	var requestBody []byte
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		requestBody, err = io.ReadAll(io.LimitReader(body, captureLimit+1))
		body.Close()
		if err != nil {
			return nil, err
		}
	}
	started := time.Now()
	a := Attempt{Polling: run.record.Protocol == "video" && req.Method == http.MethodGet, Target: target, Path: req.URL.EscapedPath(), StartedAt: started.UnixMilli(), Charges: []Charge{}}
	payload := Payload{RequestHeaders: safeHeaders(req.Header), RequestBody: sanitize(string(requestBody), secrets), BodyState: "available"}
	if len(requestBody) > captureLimit {
		payload.BodyState = "truncated"
	}
	if !strings.Contains(strings.ToLower(req.Header.Get("Content-Type")), "json") && len(requestBody) > 0 {
		payload.RequestBody = "[非文本请求：仅保存元信息]"
	}
	run.mu.Lock()
	run.secrets = append(run.secrets, secrets...)
	a.Number = len(run.record.Attempts) + 1
	run.record.Target = target
	run.record.Attempts = append(run.record.Attempts, a)
	var model map[string]any
	_ = json.Unmarshal(requestBody, &model)
	if run.record.Source != "business" {
		run.record.Effort, _ = model["reasoning_effort"].(string)
		if run.record.Effort == "" {
			run.record.Effort, _ = object(model["reasoning"])["effort"].(string)
		}
		run.record.Tier, _ = model["service_tier"].(string)
	}
	if value, ok := model["model"].(string); ok {
		run.record.UpstreamModel = value
	}
	if run.record.UpstreamModel == "" && strings.Contains(req.URL.Path, "/models/") {
		part := strings.SplitN(req.URL.Path, "/models/", 2)[1]
		run.record.UpstreamModel = strings.SplitN(part, ":", 2)[0]
	}
	if spec, ok := model["size"].(string); ok {
		run.mediaSpecification = spec
	}
	if spec, ok := model["resolution"].(string); ok {
		run.mediaSpecification = spec
	}
	if quality, ok := model["quality"].(string); ok {
		run.mediaQuality = quality
	}
	specification, quality := run.mediaSpecification, run.mediaQuality
	saveErr := run.store.save(context.WithoutCancel(req.Context()), run.record, false)
	run.mu.Unlock()
	if saveErr != nil {
		return nil, saveErr
	}
	resp, err := base.RoundTrip(req)
	b := &captureBody{run: run, attempt: a, payload: payload, started: started, secrets: secrets, specification: specification, quality: quality, stream: resp != nil && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")}
	if spec, ok := model["size"].(string); ok {
		b.specification = spec
	}
	if b.specification == "" {
		b.specification, _ = model["resolution"].(string)
	}
	if quality, ok := model["quality"].(string); ok {
		b.quality = quality
	}
	if err != nil {
		b.attempt.Error = "上游连接失败"
		b.complete()
		return nil, err
	}
	b.ReadCloser = resp.Body
	b.attempt.Status = resp.StatusCode
	b.payload.ResponseHeaders = sanitize(safeHeaders(resp.Header), secrets)
	resp.Body = b
	return resp, nil
}

// CloseIdleConnections 将连接池释放传递到原有传输。
func (t *Transport) CloseIdleConnections() {
	if closer, ok := t.Base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// captureBody 旁路收集有界正文并逐个 SSE 事件提取用量，不积累整个长流。
type captureBody struct {
	io.ReadCloser
	// run 与 attempt 关联逻辑请求；once 保证响应关闭或 EOF 只结算一次。
	run     *session
	attempt Attempt
	payload Payload
	once    sync.Once
	started time.Time
	secrets []string
	// raw 保存有界文本；pending 只保存未完成的 SSE 行；usage 为最近完整用量快照。
	raw     bytes.Buffer
	pending []byte
	usage   *Usage
	stream  bool
	// specification、quality 来自该次真实请求，不从当前模型配置猜测媒体规格。
	specification string
	quality       string
	truncated     bool
	readErr       error
}

// Read 转发所有实际字节，首字按第一次非空正文计时；EOF 不替代上层协议终态判断。
func (b *captureBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		if b.attempt.FirstByteMillis == nil {
			ms := time.Since(b.started).Milliseconds()
			b.attempt.FirstByteMillis = &ms
			b.run.mu.Lock()
			if b.run.record.FirstByteMillis == nil {
				total := b.started.UnixMilli() - b.run.record.StartedAt + ms
				b.run.record.FirstByteMillis = &total
			}
			b.run.mu.Unlock()
		}
		room := captureLimit - b.raw.Len()
		if room > 0 {
			b.raw.Write(p[:min(n, room)])
		}
		if n > room {
			b.truncated = true
		}
		if b.stream {
			b.pending = append(b.pending, p[:n]...)
			for {
				line, rest, ok := bytes.Cut(b.pending, []byte("\n"))
				if !ok {
					break
				}
				b.accept(line)
				b.pending = rest
			}
			if len(b.pending) > captureLimit {
				b.pending = nil
				b.truncated = true
			}
		}
	}
	if err != nil && err != io.EOF {
		b.readErr = err
		b.attempt.Error = "读取上游响应中断"
	}
	return n, err
}

// accept 合并独立事件里的实际用量；不把累计快照逐事件相加。
func (b *captureBody) accept(line []byte) {
	line = bytes.TrimSpace(line)
	if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
		if u := parseUsage(bytes.TrimSpace(data), b.run.record.Protocol); u != nil {
			b.usage = mergeUsage(b.usage, u)
		}
	}
}

// Close 先关闭原响应，再持久化已观察内容；调用者未读完时不继续主动读取。
func (b *captureBody) Close() error { err := b.ReadCloser.Close(); b.complete(); return err }

// complete 固定一次真实尝试的内容与费用，写入错误交给逻辑收尾统一传播。
func (b *captureBody) complete() {
	b.once.Do(func() {
		if b.stream {
			b.accept(b.pending)
		} else {
			b.usage = parseUsage(b.raw.Bytes(), b.run.record.Protocol)
		}
		if b.run.record.Protocol == "video" {
			var status map[string]any
			if json.Unmarshal(b.raw.Bytes(), &status) == nil {
				b.attempt.BillingPending = status["status"] == "queued" || status["status"] == "in_progress"
			}
		}
		if b.usage != nil {
			if b.usage.Unit == "" && (!b.usage.inputKnown || !b.usage.outputKnown) {
				b.usage = nil
			} else {
				if b.specification != "" {
					b.usage.Specification = b.specification
				}
				b.usage.Quality = b.quality
			}
		}
		b.attempt.DurationMillis = time.Since(b.started).Milliseconds()
		if b.attempt.Status >= 400 && b.attempt.Error == "" {
			b.attempt.Error = http.StatusText(b.attempt.Status)
		}
		b.payload.ResponseBody = sanitize(b.raw.String(), b.secrets)
		if b.truncated {
			b.payload.BodyState = "truncated"
		}
		b.run.mu.Lock()
		defer b.run.mu.Unlock()
		r := &b.run.record
		b.attempt.Usage = b.usage
		b.attempt.Cost, b.attempt.Charges = estimate(r.Pricing, b.usage, b.attempt.Multiplier, r.Tier, time.UnixMilli(r.StartedAt))
		if b.usage != nil {
			if r.Usage == nil {
				r.Usage = &Usage{}
			}
			addUsage(r.Usage, b.usage)
		}
		if b.attempt.Cost != nil {
			if r.Cost == nil {
				zero := 0.0
				r.Cost = &zero
			}
			*r.Cost += *b.attempt.Cost
			r.Charges = append(r.Charges, b.attempt.Charges...)
		} else if !b.attempt.BillingPending {
			r.UnknownCosts++
		}
		// 任一上游尝试无法计价，逻辑请求总费用必须保持未知，而非部分合计冒充完整费用。
		if r.UnknownCosts > 0 {
			r.Cost = nil
		}
		r.Attempts[b.attempt.Number-1] = b.attempt
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := b.run.store.putPayload(ctx, r.ID, b.attempt.Number, b.payload); err != nil {
			b.run.writeErr = errors.Join(b.run.writeErr, err)
		}
		if err := b.run.store.save(ctx, *r, false); err != nil {
			b.run.writeErr = errors.Join(b.run.writeErr, err)
		}
	})
}

// sensitiveKey 标识不可保存或复制的认证字段，不清除正常 token 用量字段。
func sensitiveKey(name string) bool {
	name = strings.ToLower(strings.ReplaceAll(name, "-", "_"))
	switch name {
	case "authorization", "proxy_authorization", "cookie", "set_cookie", "x_api_key", "api_key", "apikey", "x_goog_api_key", "access_token", "refresh_token", "secret", "password", "b64_json", "base64":
		return true
	}
	return false
}

// headerSecrets 提取本次使用凭据，覆盖响应中回显的原文与 URL 编码。
func headerSecrets(h http.Header) []string {
	result := []string{}
	for key, values := range h {
		if sensitiveKey(key) {
			for _, v := range values {
				result = append(result, v)
				if strings.HasPrefix(v, "Bearer ") {
					result = append(result, strings.TrimPrefix(v, "Bearer "))
				}
			}
		}
	}
	return result
}

// safeHeaders 将认证头统一脱敏；普通头也检查已知认证值的回显。
func safeHeaders(h http.Header) string {
	if h == nil {
		return "{}"
	}
	copy := h.Clone()
	secrets := headerSecrets(h)
	for key, values := range copy {
		if sensitiveKey(key) {
			copy[key] = []string{"[REDACTED]"}
		} else {
			for i, value := range values {
				copy[key][i] = sanitize(value, secrets)
			}
		}
	}
	data, _ := json.Marshal(copy)
	return string(data)
}

// redact 删除凭据及其 URL 转义形式，空凭据不参与替换。
func redact(text, secret string) string {
	if secret == "" {
		return text
	}
	encoded, _ := json.Marshal(secret)
	escaped := string(encoded[1 : len(encoded)-1])
	for _, s := range []string{secret, escaped, strings.ReplaceAll(escaped, "/", string([]byte{92, 47})), url.QueryEscape(secret), url.PathEscape(secret)} {
		text = strings.ReplaceAll(text, s, "[REDACTED]")
	}
	return text
}

// sanitize 对 JSON 递归移除认证及媒体二进制，对文本流逐行处理；截断只影响快照。
func sanitize(text string, secrets []string) string {
	for _, secret := range secrets {
		text = redact(text, secret)
	}
	var value any
	parsed := false
	if json.Unmarshal([]byte(text), &value) == nil {
		parsed = true
		scrub(value)
		data, _ := json.Marshal(value)
		text = string(data)
	} else if strings.Contains(text, "data:") {
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			if raw, ok := strings.CutPrefix(line, "data:"); ok && json.Unmarshal([]byte(raw), &value) == nil {
				scrub(value)
				data, _ := json.Marshal(value)
				lines[i] = "data: " + string(data)
			}
		}
		text = strings.Join(lines, "\n")
	}
	// 未完成 JSON 或纯文本不得保留内嵌媒体；无法安全解析时只留元信息提示。
	if !parsed && (strings.Contains(strings.ToLower(text), ";base64,") || strings.Contains(text, "\"b64_json\"") || strings.Contains(text, "\"base64\"")) {
		return "[包含内嵌媒体：未保存正文，仅保留元信息]"
	}
	if len(text) > captureLimit {
		text = text[:captureLimit] + "\n[正文已截断]"
	}
	return strings.ToValidUTF8(text, "�")
}

// scrub 在原数据结构内去除认证和完整媒体，保留其他文本及规格。
func scrub(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if sensitiveKey(key) || key == "data" && isEncodedMedia(item) {
				v[key] = "[REDACTED]"
			} else if s, ok := item.(string); ok && strings.Contains(strings.ToLower(s), ";base64,") {
				v[key] = "[媒体内容未保存]"
			} else {
				scrub(item)
			}
		}
	case []any:
		for _, item := range v {
			scrub(item)
		}
	}
}

// isEncodedMedia 只处理内嵌媒体的字符串 data，不删除正常响应 data 数组元信息。
func isEncodedMedia(value any) bool { _, ok := value.(string); return ok }
