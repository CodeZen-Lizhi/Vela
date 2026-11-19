package scheduling

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/protocol"
	"github.com/CodeZen-Lizhi/vela/internal/records"
)

// runSpecialized 复用调度准入和真实记录；Interactions SSE 按读取空闲超时，其余原生操作保留独立总期限。
func (r *HTTPRunner) runSpecialized(ctx context.Context, c Candidate, req ExecutionRequest, secret string, w io.Writer) (AttemptResult, error) {
	var result AttemptResult
	method := req.Method
	if method == "" {
		method = http.MethodPost
	}
	data, contentType, err := specializedBody(c, req)
	if err != nil {
		result.Status = 400
		return result, err
	}
	path, err := url.Parse(req.Path)
	if err != nil || !strings.HasPrefix(req.Path, "/") || strings.HasPrefix(req.Path, "//") || path.IsAbs() || path.Host != "" || path.Fragment != "" {
		return result, errors.New("上游请求路径无效")
	}
	if strings.HasPrefix(req.Request.Protocol, "gemini:") && strings.Contains(path.Path, "/models/") {
		path, _ = url.Parse(upstreamPath(ExecutionRequest{Path: req.Path, Request: Request{Protocol: "gemini"}}, c.Model))
	}
	query := path.Query()
	for _, key := range []string{"key", "api_key", "access_token"} {
		query.Del(key)
	}
	path.RawQuery = query.Encode()
	seconds := c.Provider.Request.RequestSeconds
	if seconds == 0 {
		seconds = 300
	}
	var bounded context.Context
	var cancel context.CancelFunc
	if req.Type == "interactions" && req.Stream {
		bounded, cancel = context.WithCancel(ctx)
	} else {
		bounded, cancel = context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	}
	defer cancel()
	call, err := http.NewRequestWithContext(bounded, method, strings.TrimRight(c.Provider.URL, "/")+path.String(), bytes.NewReader(data))
	if err != nil {
		return result, errors.New("供应商地址无效")
	}
	call.Header = specializedHeaders(req.Headers)
	if contentType != "" {
		call.Header.Set("Content-Type", contentType)
	}
	if strings.HasPrefix(req.Request.Protocol, "gemini:") {
		call.Header.Set("X-Goog-Api-Key", secret)
	} else {
		call.Header.Set("Authorization", "Bearer "+secret)
	}
	client := r.Client
	if r.ClientFor != nil {
		client, err = r.ClientFor(ctx, c)
		if err != nil {
			return result, err
		}
	}
	if client == nil {
		return result, errors.New("上游 HTTP 客户端尚未初始化")
	}
	first := c.Provider.Request.FirstByteSeconds
	if first == 0 {
		first = 30
	}
	// 首次响应以最终响应头为界；Interactions 后续按空闲等待，其余操作保留独立总期限。
	firstResponse := time.AfterFunc(time.Duration(first)*time.Second, cancel)
	response, err := client.Do(call)
	stopped := firstResponse.Stop()
	if err != nil {
		var local *records.LocalError
		if errors.As(err, &local) {
			result.Status = 500
			return result, errors.New("本地请求记录准备失败，未发送上游")
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result.Outcome.Penalty = Heavy
		result.Retryable = req.RetrySafe
		if bounded.Err() != nil {
			result.Status = 504
			result.Outcome.Penalty = Light
			if !stopped {
				return result, fmt.Errorf("首次响应超时（%d 秒），未收到上游 HTTP 响应头", first)
			}
			return result, fmt.Errorf("上游请求超时（%d 秒），未收到 HTTP 响应头", seconds)
		}
		return result, upstreamConnectionError(err, secret)
	}
	defer response.Body.Close()
	if req.Type == "interactions" && req.Stream {
		response.Body = &streamIdleReader{ReadCloser: response.Body, timeoutMillis: c.Provider.Request.StreamTotalMillis, cancel: cancel}
	}
	result.Status = response.StatusCode
	if result.Status < 200 || result.Status >= 300 {
		errorBody, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		result.Outcome = classifyHTTP(result.Status, errorBody)
		result.Outcome.RetryAfter, result.Outcome.RetryAfterSet = parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
		result.Retryable = req.RetrySafe && (result.Outcome.Penalty == Light || result.Outcome.Penalty == Heavy)
		return result, fmt.Errorf("上游返回状态 %d", result.Status)
	}
	if req.Type == "interactions" && req.Stream {
		if !strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
			return result, errors.New("Interactions 流式请求未返回原生 SSE")
		}
		if req.Response != nil {
			headers := response.Header.Clone()
			headers.Set("Content-Type", "text/event-stream")
			w = &httpCommitWriter{destination: w, status: result.Status, headers: headers, respond: req.Response}
		}
		return forwardSSE(bounded, response.Body, w, req.Request.Protocol, result)
	}
	if response.StatusCode == http.StatusNoContent && req.Method == http.MethodDelete {
		if req.Response != nil {
			req.Response(result.Status, response.Header)
		}
		result.Committed = true
		result.Outcome.Success = true
		return result, nil
	}
	if req.Type == "files" && response.Header.Get("X-Goog-Upload-URL") != "" {
		base, _ := url.Parse(c.Provider.URL)
		upload, parseErr := url.Parse(response.Header.Get("X-Goog-Upload-URL"))
		if parseErr != nil || base == nil || upload == nil {
			return result, errors.New("上游上传会话地址无效")
		}
		upload = base.ResolveReference(upload)
		if upload.User != nil || !strings.EqualFold(upload.Host, base.Host) || upload.Scheme != base.Scheme || upload.Fragment != "" {
			return result, errors.New("上游上传会话地址不属于原供应商")
		}
		query := upload.Query()
		for _, name := range []string{"key", "api_key", "access_token"} {
			query.Del(name)
		}
		upload.RawQuery = query.Encode()
		headers := response.Header.Clone()
		headers.Set("X-Goog-Upload-URL", upload.String())
		if req.Response != nil {
			req.Response(result.Status, headers)
		}
		result.Committed, result.Outcome.Success, result.ResponseID = true, true, req.ResourceID
		return result, nil
	}
	// 下载不缓存媒体；采集器只保留脱敏元信息，客户端接收原始二进制。
	if strings.HasSuffix(path.Path, "/content") || strings.Contains(response.Header.Get("Content-Type"), "video/") || strings.Contains(response.Header.Get("Content-Type"), "audio/") {
		if req.Response != nil {
			req.Response(result.Status, response.Header)
		}
		result.Committed = true
		_, err = io.Copy(w, response.Body)
		result.Outcome.Success = err == nil
		return result, err
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, (128<<20)+1))
	if err != nil || len(data) > 128<<20 {
		result.Outcome.Penalty = Heavy
		result.Retryable = req.RetrySafe
		return result, errors.New("读取上游响应失败或响应超过 128 MiB")
	}
	if req.Type == "files" && len(bytes.TrimSpace(data)) == 0 {
		if req.Response != nil {
			req.Response(result.Status, response.Header)
		}
		result.Committed, result.Outcome.Success = true, true
		return result, nil
	}
	var document map[string]json.RawMessage
	if json.Unmarshal(data, &document) != nil || document == nil || len(document["error"]) > 0 && string(document["error"]) != "null" {
		result.Outcome = classifyHTTP(200, data)
		if result.Outcome.Penalty == Neutral {
			result.Outcome.Penalty = Heavy
		}
		result.Retryable = req.RetrySafe
		return result, errors.New("上游返回无效 JSON 或错误信封")
	}
	if !specializedTerminal(req, document) {
		result.Outcome.Penalty = Heavy
		result.Retryable = req.RetrySafe
		return result, errors.New("上游缺少有效协议结果")
	}
	if req.Type == "video" && req.Method != http.MethodDelete {
		var state, id string
		_ = json.Unmarshal(document["status"], &state)
		_ = json.Unmarshal(document["id"], &id)
		if state == "queued" || state == "in_progress" {
			if id == "." || id == ".." || strings.ContainsAny(id, "/\\") {
				return result, errors.New("视频任务 ID 无效")
			}
			// 与现有模型测试一致，在同一总期限和 Key 上等到任务终态；排队不能记作生成成功。
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			select {
			case <-bounded.Done():
				return result, errors.New("等待视频任务完成已超时或取消")
			case <-timer.C:
			}
			if req.Method == http.MethodGet && strings.TrimPrefix(path.Path, "/v1/videos/") != id {
				return result, errors.New("视频任务 ID 与原绑定不一致")
			}
			// 每次真实查询都复核原候选，不能让创建时的凭据快照绕过后续停用、撤权或换密钥。
			if req.Revalidate == nil {
				result.Status = http.StatusConflict
				return result, ErrContinuation
			}
			pollSecret, checkErr := req.Revalidate(bounded)
			if checkErr != nil {
				if bounded.Err() != nil {
					return result, bounded.Err()
				}
				result.Status = http.StatusConflict
				return result, ErrContinuation
			}
			req.Method, req.Path, req.WireBody, req.RetrySafe = http.MethodGet, "/v1/videos/"+url.PathEscape(id), nil, false
			return r.runSpecialized(bounded, c, req, pollSecret, w)
		}
	}
	_ = json.Unmarshal(document["id"], &result.ResponseID)
	if req.Type == "files" {
		var file map[string]json.RawMessage
		_ = json.Unmarshal(document["file"], &file)
		if file != nil {
			_ = json.Unmarshal(file["name"], &result.ResponseID)
		} else {
			_ = json.Unmarshal(document["name"], &result.ResponseID)
		}
		var files []struct {
			// Name 是本次实际 Key 返回的具名资源，不从客户端输入或其他候选推测归属。
			Name string `json:"name"`
		}
		if json.Unmarshal(document["files"], &files) == nil {
			for _, file := range files {
				if file.Name != "" {
					result.ResourceIDs = append(result.ResourceIDs, file.Name)
				}
			}
		}
	}
	if req.Response != nil {
		req.Response(result.Status, response.Header)
	}
	result.Committed = true
	if _, err := w.Write(data); err != nil {
		return result, err
	}
	result.Outcome.Success = true
	if req.Type == "video" && string(document["status"]) == `"failed"` {
		result.Outcome.Success = false
		return result, errors.New("视频任务生成失败")
	}
	return result, nil
}

// specializedBody 只重写协议承载的模型身份，媒体二进制和 multipart 文件内容保持原样。
func specializedBody(c Candidate, req ExecutionRequest) ([]byte, string, error) {
	if req.Method == http.MethodGet || req.Method == http.MethodDelete {
		return nil, "", nil
	}
	contentType := req.Headers.Get("Content-Type")
	if req.WireBody != nil {
		mediaType, params, _ := mime.ParseMediaType(contentType)
		if mediaType != "multipart/form-data" || req.Type == "files" {
			return req.WireBody, contentType, nil
		}
		reader := multipart.NewReader(bytes.NewReader(req.WireBody), params["boundary"])
		var wire bytes.Buffer
		writer := multipart.NewWriter(&wire)
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, "", errors.New("multipart 请求无效")
			}
			output, err := writer.CreatePart(part.Header)
			if err != nil {
				return nil, "", err
			}
			if part.FormName() == "model" {
				_, err = io.WriteString(output, c.Model)
			} else {
				_, err = io.Copy(output, part)
			}
			_ = part.Close()
			if err != nil {
				return nil, "", err
			}
		}
		if err := writer.Close(); err != nil {
			return nil, "", err
		}
		return wire.Bytes(), writer.FormDataContentType(), nil
	}
	var document map[string]json.RawMessage
	if json.Unmarshal(req.Body, &document) != nil || document == nil {
		return nil, "", errors.New("请求正文必须是 JSON 对象")
	}
	if req.Type != "files" {
		if req.Type == "interactions" && len(document["agent"]) > 0 && len(document["model"]) == 0 {
			document["agent"], _ = json.Marshal(c.Model)
		} else {
			document["model"], _ = json.Marshal(c.Model)
		}
		if strings.HasPrefix(req.Request.Protocol, "gemini:") && req.Type != "interactions" {
			delete(document, "model")
			if req.Type == "embedding" {
				var requests []map[string]json.RawMessage
				if json.Unmarshal(document["requests"], &requests) == nil && requests != nil {
					for _, item := range requests {
						item["model"], _ = json.Marshal("models/" + strings.TrimPrefix(c.Model, "models/"))
					}
					document["requests"], _ = json.Marshal(requests)
				}
			}
		}
	}
	data, err := json.Marshal(document)
	if err == nil && req.Type == "video" {
		return protocol.VideoForm(data, maxHTTPPayloadBytes)
	}
	return data, "application/json", err
}

// specializedTerminal 检查独立协议的有效结果；异步视频接受真实任务状态，不误称生成已完成。
func specializedTerminal(req ExecutionRequest, document map[string]json.RawMessage) bool {
	if req.Method == http.MethodDelete {
		return true
	}
	switch req.Type {
	case "embedding":
		var data []struct {
			// Embedding 是上游的浮点数组或显式 base64 编码字符串。
			Embedding json.RawMessage `json:"embedding"`
		}
		if json.Unmarshal(document["data"], &data) == nil && len(data) > 0 {
			for _, item := range data {
				var vector []float64
				var encoded string
				if !(json.Unmarshal(item.Embedding, &vector) == nil && len(vector) > 0 || json.Unmarshal(item.Embedding, &encoded) == nil && encoded != "") {
					return false
				}
			}
			return true
		}
		var embedding struct {
			Values []float64 `json:"values"` /* Values 是 Gemini 单条向量。 */
		}
		if json.Unmarshal(document["embedding"], &embedding) == nil && len(embedding.Values) > 0 {
			return true
		}
		var embeddings []struct {
			Values []float64 `json:"values"` /* Values 是 Gemini 批次中的一条向量。 */
		}
		if json.Unmarshal(document["embeddings"], &embeddings) != nil || len(embeddings) == 0 {
			return false
		}
		for _, item := range embeddings {
			if len(item.Values) == 0 {
				return false
			}
		}
		return true
	case "rerank":
		var results []struct {
			// Index 与 Score 必须有值；零索引和零分均有效。
			Index *int     `json:"index"`
			Score *float64 `json:"relevance_score"`
		}
		if json.Unmarshal(document["results"], &results) != nil || results == nil {
			return false
		}
		for _, item := range results {
			if item.Index == nil || *item.Index < 0 || item.Score == nil {
				return false
			}
		}
		return true
	case "image":
		var data []map[string]json.RawMessage
		if json.Unmarshal(document["data"], &data) != nil || len(data) == 0 {
			return false
		}
		for _, image := range data {
			var address, encoded string
			_ = json.Unmarshal(image["url"], &address)
			_ = json.Unmarshal(image["b64_json"], &encoded)
			if address == "" && encoded == "" {
				return false
			}
		}
		return true
	case "video":
		var id, status string
		_ = json.Unmarshal(document["id"], &id)
		_ = json.Unmarshal(document["status"], &status)
		return id != "" && (status == "queued" || status == "in_progress" || status == "completed" || status == "failed")
	case "files":
		return true
	case "interactions":
		var id string
		return json.Unmarshal(document["id"], &id) == nil && id != "" && len(id) <= 256
	case "search":
		for _, key := range []string{"results", "data", "output"} {
			var items []json.RawMessage
			if json.Unmarshal(document[key], &items) == nil && items != nil {
				return true
			}
		}
		return false
	}
	return false
}

// specializedHeaders 过滤客户端凭据和逐跳头，同时保留业务、协议及上传控制头。
func specializedHeaders(headers http.Header) http.Header {
	result := make(http.Header)
	forwardBusinessHeaders(result, headers)
	return result
}
