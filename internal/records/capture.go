package records

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// captureLimit 是单个正文快照的上限，不限制真实转发的响应大小。
const captureLimit = 1 << 20

// httpSSEEventLimit 与 HTTP runner 的 128 MiB 事件边界一致，用量观察不受正文快照上限限制。
const httpSSEEventLimit = 128 << 20

// omittedResponseBody 说明非文本媒体只保留状态和响应头，不保存任意媒体字节。
const omittedResponseBody = "[非文本响应：仅保存元信息]"

// textResponse 按真实 Content-Type 判断可保存文本；空头沿用旧 JSON/SSE 适配器的兼容行为。
func textResponse(contentType string) bool {
	if contentType == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") || mediaType == "application/xml" || strings.HasSuffix(mediaType, "+xml")
}

// LocalError 标识实际出站前的本地采集错误，调用方不得将其计为上游健康故障或重试。
type LocalError struct {
	// Cause 是保留给本机诊断的原始错误，对外只返回稳定说明。
	Cause error
}

// Error 返回无凭据和数据库细节的本地错误说明。
func (e *LocalError) Error() string { return "本地请求记录准备失败" }

// Unwrap 保留本机错误链用于定位。
func (e *LocalError) Unwrap() error { return e.Cause }

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
	finish, err := PrepareStreamCapture(req, false)
	if err != nil {
		return nil, err
	}
	response, err := base.RoundTrip(req)
	return finish(response, err)
}

// PrepareStreamCapture 在真实出站前登记尝试，从完整正文副本提取模型，再旁路读取响应；WebSocket 复用脱敏、用量和费用结算。
// forceStream 允许 WebSocket 事件以内部 SSE 形状采集，握手状态仍保留为真实的 101。
func PrepareStreamCapture(req *http.Request, forceStream bool) (func(*http.Response, error) (*http.Response, error), error) {
	run, _ := req.Context().Value(sessionKey{}).(*session)
	if run == nil {
		return func(response *http.Response, err error) (*http.Response, error) { return response, err }, nil
	}
	target, _ := req.Context().Value(targetKey{}).(Target)
	secrets := headerSecrets(req.Header)
	var requestBody []byte
	var upstreamModel *string
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, &LocalError{Cause: err}
		}
		requestBody, err = io.ReadAll(io.LimitReader(body, captureLimit+1))
		if err == nil {
			var metadata struct {
				// Model 只取实际出站正文中的顶层模型，不将大段输入保留为元数据。
				Model *string `json:"model"`
			}
			// 快照截断不能影响模型身份；解析副本的完整 JSON，不读取或改写实际转发流。
			// 非 JSON 或无效 model 沿用未知及 URL 提取规则，不阻断原有转发。
			if json.NewDecoder(io.MultiReader(bytes.NewReader(requestBody), body)).Decode(&metadata) == nil {
				upstreamModel = metadata.Model
			}
		}
		body.Close()
		if err != nil {
			return nil, &LocalError{Cause: err}
		}
	}
	started := time.Now()
	a := Attempt{Polling: strings.TrimPrefix(run.record.Protocol, "openai:") == "video" && req.Method == http.MethodGet, Target: target, Path: req.URL.EscapedPath(), StartedAt: started.UnixMilli(), Charges: []Charge{}}
	payload := Payload{RequestHeaders: safeHeaders(req.Header), RequestBody: sanitize(string(requestBody), secrets), BodyState: "available"}
	if len(requestBody) > captureLimit {
		payload.BodyState = "truncated"
	}
	if !strings.Contains(strings.ToLower(req.Header.Get("Content-Type")), "json") && len(requestBody) > 0 {
		payload.RequestBody = "[非文本请求：仅保存元信息]"
	}
	if run.record.Protocol == "openai:realtime" {
		payload.BodyState = "unavailable"
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
	if upstreamModel != nil {
		run.record.UpstreamModel = *upstreamModel
	}
	if run.record.UpstreamModel == "" && strings.Contains(req.URL.Path, "/models/") {
		part := strings.SplitN(req.URL.Path, "/models/", 2)[1]
		run.record.UpstreamModel = strings.SplitN(part, ":", 2)[0]
	}
	// Realtime 的实际模型来自 GET 握手查询串；只提取 model，不保留其他查询参数。
	if run.record.Protocol == "openai:realtime" {
		run.record.UpstreamModel = req.URL.Query().Get("model")
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
	if saveErr != nil {
		run.record.Attempts = run.record.Attempts[:len(run.record.Attempts)-1]
	} else if scope, _ := req.Context().Value(attemptKey{}).(*attemptScope); scope != nil {
		scope.number = a.Number
	}
	run.mu.Unlock()
	if saveErr != nil {
		return nil, &LocalError{Cause: saveErr}
	}
	b := &captureBody{run: run, attempt: a, payload: payload, started: started, secrets: secrets, specification: specification, quality: quality, stream: forceStream}
	if forceStream {
		b.eventLimit = 16*1024*1024 + 16
	}
	if spec, ok := model["size"].(string); ok {
		b.specification = spec
	}
	if b.specification == "" {
		b.specification, _ = model["resolution"].(string)
	}
	if quality, ok := model["quality"].(string); ok {
		b.quality = quality
	}
	return func(resp *http.Response, err error) (*http.Response, error) {
		if err != nil {
			b.attempt.Error = "上游连接失败"
			b.complete()
			return nil, err
		}
		b.stream = b.stream || strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
		b.omitBody = !textResponse(resp.Header.Get("Content-Type"))
		run.mu.Lock()
		run.binaryResponse = b.omitBody
		run.mu.Unlock()
		b.ReadCloser = resp.Body
		b.attempt.Status = resp.StatusCode
		b.payload.ResponseHeaders = sanitize(safeHeaders(resp.Header), secrets)
		resp.Body = b
		return resp, nil
	}, nil
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
	// raw 保存有界快照；jsonBody 临时保存非流式实际已读正文，关闭后释放，不受快照限制。
	raw      bytes.Buffer
	jsonBody bytes.Buffer
	// pending 与 event 分别保存未完成行和事件；超限后丢弃到对应边界，避免误解析残片。
	pending      []byte
	event        bytes.Buffer
	lineDropped  bool
	eventDropped bool
	// responseIDs 仅用于 Realtime，去重同一连接已经结算的 response.done 快照。
	responseIDs map[string]struct{}
	// eventLimit 只约束用量观察的单事件缓冲；零使用 HTTP 的 128 MiB，WebSocket 为 16 MiB。
	eventLimit int
	usage      *Usage
	stream     bool
	// omitBody 为真实非文本响应，既不积累完整 JSON，也不保存媒体字节快照。
	omitBody bool
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
		b.observeFirstByte()
		// Realtime 的音频和转写只在观察器瞬时解析，不进入通用正文快照。
		if !b.omitBody && b.run.record.Protocol != "openai:realtime" {
			room := captureLimit - b.raw.Len()
			if room > 0 {
				b.raw.Write(p[:min(n, room)])
			}
			if n > room {
				b.truncated = true
			}
		}
		if b.stream && !b.omitBody && !b.run.resourceQuery {
			b.pending = append(b.pending, p[:n]...)
			for {
				line, rest, ok := bytes.Cut(b.pending, []byte("\n"))
				if !ok {
					break
				}
				b.accept(line)
				b.pending = rest
			}
			limit := b.eventLimit
			if limit == 0 {
				limit = httpSSEEventLimit
			}
			if len(b.pending) > limit {
				b.pending = nil
				b.lineDropped = true
				b.eventDropped = true
			}
		} else if !b.omitBody && !b.run.resourceQuery {
			b.jsonBody.Write(p[:n])
		}
	}
	if err != nil && err != io.EOF {
		b.readErr = err
		b.attempt.Error = "读取上游响应中断"
	}
	return n, err
}

// observeFirstByte 以首次实际接收正文或 WebSocket 事件计时，不以成功握手冒充首字。
func (b *captureBody) observeFirstByte() {
	if b.attempt.FirstByteMillis != nil {
		return
	}
	ms := time.Since(b.started).Milliseconds()
	b.attempt.FirstByteMillis = &ms
	b.run.mu.Lock()
	defer b.run.mu.Unlock()
	if b.run.record.FirstByteMillis == nil {
		total := b.started.UnixMilli() - b.run.record.StartedAt + ms
		b.run.record.FirstByteMillis = &total
	}
}

// accept 按 SSE 空行划分事件，多条 data 行以换行合并；超限事件只丢弃用量观察。
func (b *captureBody) accept(line []byte) {
	if b.lineDropped {
		b.lineDropped = false
		return
	}
	line = bytes.TrimSuffix(line, []byte("\r"))
	if len(line) == 0 {
		b.acceptEvent()
		return
	}
	if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
		data = bytes.TrimPrefix(data, []byte(" "))
		limit := b.eventLimit
		if limit == 0 {
			limit = httpSSEEventLimit
		}
		if b.eventDropped || b.event.Len()+len(data)+1 > limit {
			b.eventDropped = true
			b.event.Reset()
			return
		}
		b.event.Write(data)
		b.event.WriteByte('\n')
	}
}

// acceptEvent 只合并完整事件的累计用量，不逐事件相加；结束后清除事件状态。
func (b *captureBody) acceptEvent() {
	if !b.eventDropped {
		b.observeUsage(b.event.Bytes())
	}
	b.event.Reset()
	b.eventDropped = false
}

// observeUsage 普通流合并累计快照；Realtime 按独立 response.id 累加，重复终态不重复计费。
func (b *captureBody) observeUsage(data []byte) {
	if b.run.record.Protocol != "openai:realtime" {
		if u := parseUsage(data, b.run.record.Protocol); u != nil {
			b.usage = mergeUsage(b.usage, u)
		}
		return
	}
	var event struct {
		// Type 与 Response.ID 是 Realtime 终态和去重身份，不从增量事件推测费用。
		Type     string `json:"type"`
		Response struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &event) != nil || event.Type != "response.done" || event.Response.ID == "" {
		return
	}
	if _, seen := b.responseIDs[event.Response.ID]; seen {
		return
	}
	u := parseUsage(data, b.run.record.Protocol)
	if u == nil {
		return
	}
	if b.responseIDs == nil {
		b.responseIDs = make(map[string]struct{})
	}
	b.responseIDs[event.Response.ID] = struct{}{}
	if b.usage == nil {
		b.usage = u
		return
	}
	b.usage.inputKnown = b.usage.inputKnown && u.inputKnown
	b.usage.outputKnown = b.usage.outputKnown && u.outputKnown
	addUsage(b.usage, u)
}

// Close 先关闭原响应，再持久化已观察内容；调用者未读完时不继续主动读取。
func (b *captureBody) Close() error { err := b.ReadCloser.Close(); b.complete(); return err }

// complete 固定一次真实尝试的内容与费用，写入错误交给逻辑收尾统一传播。
func (b *captureBody) complete() {
	b.once.Do(func() {
		if b.run.resourceQuery {
			b.usage = &Usage{inputKnown: true, outputKnown: true}
		} else if b.stream {
			b.accept(b.pending)
			b.acceptEvent()
		} else {
			b.usage = parseUsage(b.jsonBody.Bytes(), b.run.record.Protocol)
		}
		if !b.run.resourceQuery && strings.TrimPrefix(b.run.record.Protocol, "openai:") == "video" {
			var status map[string]any
			if json.Unmarshal(b.jsonBody.Bytes(), &status) == nil {
				b.attempt.BillingPending = status["status"] == "queued" || status["status"] == "in_progress"
			}
		}
		b.jsonBody = bytes.Buffer{}
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
		if b.omitBody {
			b.payload.ResponseBody = omittedResponseBody
			b.payload.BodyState = "unavailable"
		}
		if b.truncated {
			b.payload.BodyState = "truncated"
		}
		b.run.mu.Lock()
		defer b.run.mu.Unlock()
		r := &b.run.record
		b.attempt.Usage = b.usage
		if b.run.resourceQuery {
			zero := 0.0
			b.attempt.Cost, b.attempt.Charges = &zero, []Charge{}
		} else {
			b.attempt.Cost, b.attempt.Charges = estimate(r.Pricing, b.usage, b.attempt.Multiplier, r.Tier, time.UnixMilli(r.StartedAt))
		}
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

// sanitize 对 JSON 递归移除认证及媒体二进制，SSE 按完整事件合并 data 后清理；截断只影响快照。
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
		text = sanitizeSSE(text)
		parsed = true
	}
	// 未完成 JSON 或纯文本不得保留内嵌媒体；无法安全解析时只留元信息提示。
	if !parsed && containsEmbeddedMedia(text) {
		return "[包含内嵌媒体：未保存正文，仅保留元信息]"
	}
	if len(text) > captureLimit {
		text = text[:captureLimit] + "\n[正文已截断]"
	}
	return strings.ToValidUTF8(text, "�")
}

// sanitizeSSE 只改写诊断快照，按事件合并多行 JSON；含媒体的残缺事件仅留元信息，其他文本事件原样保留。
func sanitizeSSE(text string) string {
	events := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n")
	for i, event := range events {
		lines := strings.Split(event, "\n")
		var fragments []string
		for _, line := range lines {
			if data, ok := strings.CutPrefix(line, "data:"); ok {
				fragments = append(fragments, strings.TrimPrefix(data, " "))
			}
		}
		if len(fragments) == 0 {
			continue
		}
		raw := strings.Join(fragments, "\n")
		var value any
		var saved string
		if json.Unmarshal([]byte(raw), &value) == nil {
			scrub(value)
			data, _ := json.Marshal(value)
			saved = string(data)
		} else if containsEmbeddedMedia(event) {
			saved = "[包含内嵌媒体：未保存正文，仅保留元信息]"
		} else {
			continue
		}
		result := make([]string, 0, len(lines))
		written := false
		for _, line := range lines {
			if strings.HasPrefix(line, "data:") {
				if !written {
					result = append(result, "data: "+saved)
					written = true
				}
			} else {
				result = append(result, line)
			}
		}
		events[i] = strings.Join(result, "\n")
	}
	return strings.Join(events, "\n\n")
}

// containsEmbeddedMedia 识别无法安全解析的快照中的媒体字段，覆盖 audio.data 及音频增量事件。
func containsEmbeddedMedia(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, ";base64,") || strings.Contains(lower, `"b64_json"`) || strings.Contains(lower, `"base64"`) ||
		(strings.Contains(lower, `"audio"`) || strings.Contains(lower, ".audio.")) && (strings.Contains(lower, `"data"`) || strings.Contains(lower, `"delta"`))
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
