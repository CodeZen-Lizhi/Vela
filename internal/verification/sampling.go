package verification

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/CodeZen-Lizhi/vela/internal/verification/fingerprint"
)

// sampleResponseLimit 限制单次响应的总字节数，包含 SSE 前奏与错误响应。
const sampleResponseLimit = 4 << 20

// requestSample 只尝试一次；调用方必须用总计 120 秒的 context 包住请求，并在每次重试前复核权限与密钥。
func requestSample(ctx context.Context, client *http.Client, target VerificationTarget, secret string, challenge fingerprint.Challenge) fingerprint.Sample {
	if client == nil {
		client = http.DefaultClient
	}
	bounded := *client
	transport := bounded.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	attempt := &sampleTransport{base: transport}
	bounded.Transport = attempt
	// 验证请求不跟随重定向，避免把认证头送往其他目标。
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	effort := target.Effort
	if effort == "auto" {
		effort = ""
	}
	var sample fingerprint.Sample
	if target.Protocol == "gemini" {
		sample = requestGeminiSample(ctx, &bounded, target, secret, effort, challenge)
	} else {
		sample = fingerprint.RequestSample(ctx, fingerprint.DetectConfig{
			BaseURL: strings.TrimRight(target.URL, "/") + "/v1", APIKey: secret,
			Model: target.RequestModel, API: fingerprint.API(target.Protocol), Effort: effort,
			Timeout: 120 * time.Second, HTTPClient: &bounded,
		}, challenge)
	}
	if sample.State == "failed" && attempt.readError != nil && (sample.Error == nil || !strings.HasPrefix(*sample.Error, "HTTP ")) {
		message := attempt.readError.Error()
		sample.Error = &message
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		sample.State = "failed"
		message := "Timed out after the total request deadline."
		sample.Error = &message
	} else if errors.Is(ctx.Err(), context.Canceled) {
		sample.State = "cancelled"
		message := "Cancelled."
		sample.Error = &message
	}
	sample.Text = redactSampleText(sample.Text, secret)
	sample.RawText = redactSampleText(sample.RawText, secret)
	for _, value := range []*string{sample.Error, sample.ResponseID, sample.ResponseModel} {
		if value != nil {
			*value = redactSampleText(*value, secret)
		}
	}
	if sample.Error != nil {
		message := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, *sample.Error)
		runes := []rune(message)
		if len(runes) > 500 {
			message = string(runes[:500])
		}
		sample.Error = &message
	}
	return sample
}

// redactSampleText 清除凭据原文及 URL 编码形式，覆盖回答、错误和响应元数据。
func redactSampleText(text, secret string) string {
	if secret == "" {
		return text
	}
	for _, value := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
		text = strings.ReplaceAll(text, value, "[REDACTED]")
	}
	return text
}

// sampleRetryable 只允许网络故障、请求超时及 HTTP 408、429、5xx；主动取消、认证、参数和解析错误不重试。
func sampleRetryable(sample fingerprint.Sample) bool {
	if sample.State != "failed" || sample.Error == nil {
		return false
	}
	message := *sample.Error
	if strings.HasPrefix(message, "HTTP ") {
		status, _, _ := strings.Cut(strings.TrimPrefix(message, "HTTP "), ":")
		code, err := strconv.Atoi(status)
		return err == nil && (code == 408 || code == 429 || code >= 500 && code <= 599)
	}
	return strings.HasPrefix(message, "sample network error:") || strings.HasPrefix(message, "Timed out after ")
}

// sampleTransport 复用已有传输配置，统一限制四协议响应大小并标记真实网络错误。
type sampleTransport struct {
	// base 是调用方选定的代理、TLS 和连接池传输。
	base http.RoundTripper
	// readError 保留响应读取失败，避免上游 JSON 解析把网络错误转换为普通格式错误。
	readError error
}

// RoundTrip 发出一次请求并为响应体安装总量限制，不在适配层自动重试。
func (t *sampleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		t.readError = fmt.Errorf("sample network error: %w", err)
		return nil, t.readError
	}
	resp.Body = &sampleBody{ReadCloser: resp.Body, remaining: sampleResponseLimit, attempt: t}
	return resp, nil
}

// sampleBody 对整个响应计数，超限返回错误而不是把截断数据伪装成 EOF。
type sampleBody struct {
	// ReadCloser 保留原始响应体的关闭责任；remaining 是剩余可读取字节数。
	io.ReadCloser
	remaining int
	// attempt 接收读取失败类别，不改变上游错误和数字解析算法。
	attempt *sampleTransport
}

// Read 限制总响应大小，并保留网络读取错误以供调用方判断重试。
func (b *sampleBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.ReadCloser.Read(probe[:])
		if n > 0 {
			b.attempt.readError = errors.New("Sample response exceeds 4 MiB.")
			return 0, b.attempt.readError
		}
		if err != nil && err != io.EOF {
			err = fmt.Errorf("sample network error: %w", err)
			b.attempt.readError = err
		}
		return 0, err
	}
	if len(p) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= n
	if err != nil && err != io.EOF {
		err = fmt.Errorf("sample network error: %w", err)
		b.attempt.readError = err
	}
	return n, err
}

// geminiSampleResponse 只解码首候选文本及终态，thought 标记的推理内容不进入样本。
type geminiSampleResponse struct {
	// Candidates 保留协议数组顺序，只读取索引 0；Error 用于识别 HTTP 200 内的错误信封。
	Candidates []struct {
		// Content.Parts 是回答片段；FinishReason 是 STOP、MAX_TOKENS 等终态。
		Content struct {
			// Parts 中 Thought 为 true 的文本只用于推理，不作为输出数字。
			Parts []struct {
				// Text 是回答内容；Thought 标识推理片段。
				Text    string `json:"text"`
				Thought bool   `json:"thought"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	Error *struct {
		// Code 是可重试判断使用的 HTTP 等价状态码，不回显上游错误原文。
		Code int `json:"code"`
	} `json:"error"`
}

// requestGeminiSample 使用原生 SSE 端点；同一端点返回 JSON 时也按同步响应处理。
func requestGeminiSample(ctx context.Context, client *http.Client, target VerificationTarget, secret, effort string, challenge fingerprint.Challenge) fingerprint.Sample {
	sample := fingerprint.Sample{State: "failed", ExpectedCount: challenge.ExpectedCount}
	started := time.Now()
	fail := func(err error) fingerprint.Sample {
		message := err.Error()
		sample.Error = &message
		return sample
	}
	body := map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": challenge.Prompt}}}}}
	if effort != "" {
		thinking := map[string]any{"thinkingLevel": effort}
		if strings.Contains(strings.ToLower(target.RequestModel), "gemini-2.5") {
			// 官方 2.5 系列只支持预算；映射值落在 Pro、Flash、Flash-Lite 共有的有效区间。
			budget, ok := map[string]int{"low": 1024, "medium": 8192, "high": 24576}[effort]
			if !ok {
				return fail(errors.New("Unsupported Gemini thinking effort."))
			}
			thinking = map[string]any{"thinkingBudget": budget}
		}
		body["generationConfig"] = map[string]any{"thinkingConfig": thinking}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fail(err)
	}
	base, err := url.Parse(target.URL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return fail(errors.New("Invalid Gemini base URL."))
	}
	endpoint := strings.TrimRight(target.URL, "/") + "/v1beta/models/" + url.PathEscape(target.RequestModel) + ":streamGenerateContent?alt=sse"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fail(errors.New("Invalid Gemini request."))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("x-goog-api-key", secret)
	resp, err := client.Do(req)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fail(fmt.Errorf("HTTP %d: Gemini request failed.", resp.StatusCode))
	}
	var text strings.Builder
	finish := ""
	accept := func(data []byte) error {
		var response geminiSampleResponse
		if err := json.Unmarshal(data, &response); err != nil {
			return errors.New("Invalid Gemini JSON response.")
		}
		if response.Error != nil {
			return fmt.Errorf("HTTP %d: Gemini stream failed.", response.Error.Code)
		}
		if len(response.Candidates) == 0 {
			return nil
		}
		candidate := response.Candidates[0]
		for _, part := range candidate.Content.Parts {
			if !part.Thought {
				text.WriteString(part.Text)
			}
		}
		if candidate.FinishReason != "" {
			finish = candidate.FinishReason
		}
		return nil
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 4096), sampleResponseLimit)
		var data []string
		flush := func() error {
			if len(data) == 0 {
				return nil
			}
			frame := strings.Join(data, "\n")
			data = nil
			return accept([]byte(frame))
		}
		for scanner.Scan() {
			if sample.FirstByteMs == nil {
				ms := time.Since(started).Milliseconds()
				sample.FirstByteMs = &ms
			}
			line := scanner.Text()
			if line == "" {
				err = flush()
				if err != nil || finish != "" {
					break
				}
			} else if value, ok := strings.CutPrefix(line, "data:"); ok {
				data = append(data, strings.TrimPrefix(value, " "))
			}
		}
		if err == nil {
			err = scanner.Err()
		}
		if err == nil {
			err = flush()
		}
	} else {
		var content []byte
		content, err = io.ReadAll(resp.Body)
		if err == nil {
			err = accept(content)
			ms := time.Since(started).Milliseconds()
			sample.FirstByteMs = &ms
		}
	}
	sample.Text, sample.RawText = text.String(), text.String()
	numbers := fingerprint.ParseNumbers(sample.Text)
	sample.Count = len(numbers)
	if err != nil {
		return fail(err)
	}
	if finish != "STOP" {
		return fail(errors.New("Gemini response did not complete."))
	}
	if sample.Count > challenge.ExpectedCount {
		numbers = numbers[:challenge.ExpectedCount]
		values := make([]string, len(numbers))
		for i, number := range numbers {
			values[i] = strconv.Itoa(number)
		}
		sample.Text = strings.Join(values, ", ")
		sample.Count = len(numbers)
		sample.State = "truncated"
	} else {
		sample.State = "complete"
	}
	// 与上游 minimumNumbers 保持一致；解析算法仍由 fingerprint.ParseNumbers 唯一实现。
	minimum := max(80, int(math.Ceil(float64(challenge.ExpectedCount)*0.55)))
	if sample.Count < minimum {
		sample.State = "failed"
		return fail(fmt.Errorf("Too few valid numbers: %d; need at least %d.", sample.Count, minimum))
	}
	return sample
}
