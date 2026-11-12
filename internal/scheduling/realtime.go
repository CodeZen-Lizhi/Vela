package scheduling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/records"
	"github.com/coder/websocket"
)

// errRealtimeModel 表示客户端试图改变当前连接的模型绑定，不应惩罚上游或重新选路。
var errRealtimeModel = errors.New("Realtime model 与当前连接绑定不一致")

// runRealtime 经实际候选出口建立标准 Realtime WebSocket；成功握手即提交，之后永不重放。
func (r *HTTPRunner) runRealtime(ctx context.Context, c Candidate, req ExecutionRequest, secret string, _ io.Writer) (result AttemptResult, cause error) {
	if req.Realtime == nil || req.Request.Protocol != "openai:realtime" {
		return result, errors.New("Realtime 客户端连接或协议无效")
	}
	path, err := url.Parse(req.Path)
	if err != nil || path.IsAbs() || path.Host != "" || path.Path != "/v1/realtime" || path.Fragment != "" {
		return result, errors.New("Realtime 上游路径无效")
	}
	query := path.Query()
	for name := range query {
		if strings.EqualFold(name, "model") {
			delete(query, name)
		}
	}
	query.Set("model", c.Model)
	path.RawQuery = query.Encode()
	call, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Provider.URL, "/")+path.String(), nil)
	if err != nil {
		return result, errors.New("Realtime 供应商地址无效")
	}
	forwardBusinessHeaders(call.Header, req.Headers)
	call.Header.Set("Authorization", "Bearer "+secret)
	client := r.Client
	if r.ClientFor != nil {
		client, err = r.ClientFor(ctx, c)
		if err != nil {
			return result, err
		}
	}
	if client == nil {
		return result, errors.New("Realtime 上游客户端尚未初始化")
	}
	// 旁路登记真实尝试；握手必须复用可写 transport，不能经过 HTTP 响应体采集包装。
	observe, finish, err := records.PrepareRealtimeCapture(call)
	if err != nil {
		result.Status = http.StatusInternalServerError
		return result, errors.New("本地请求记录准备失败，未发送上游")
	}
	defer func() {
		if err := finish(result.Status, cause); err != nil {
			log.Printf("保存 Realtime 尝试记录失败: %v", err)
		}
	}()
	dialClient := *client
	if transport, ok := client.Transport.(*records.Transport); ok {
		dialClient.Transport = transport.Base
	}
	dialClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	first := c.Provider.Request.FirstByteSeconds
	if first == 0 {
		first = 30
	}
	dialCtx, dialCancel := context.WithTimeout(ctx, time.Duration(first)*time.Second)
	upstream, response, err := websocket.Dial(dialCtx, call.URL.String(), &websocket.DialOptions{HTTPClient: &dialClient, HTTPHeader: call.Header})
	expired := errors.Is(dialCtx.Err(), context.DeadlineExceeded)
	dialCancel()
	if response != nil {
		result.Status = response.StatusCode
	}
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result.Outcome.Penalty = Heavy
		if response != nil {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
			result.Outcome = classifyHTTP(response.StatusCode, body)
			result.Outcome.RetryAfter, result.Outcome.RetryAfterSet = parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
			if response.StatusCode < 400 {
				result.Outcome.Penalty = Heavy
			}
		} else if expired {
			result.Outcome.Penalty = Light
		}
		result.Retryable = req.RetrySafe && (result.Outcome.Penalty == Heavy || result.Outcome.Penalty == Light)
		return result, fmt.Errorf("Realtime 上游握手失败（%d）", result.Status)
	}
	defer upstream.CloseNow()
	upstream.SetReadLimit(16 << 20)
	result.Status = http.StatusSwitchingProtocols
	result.Committed = true
	return relayRealtime(ctx, upstream, req, c.Model, observe, result)
}

// realtimeRelayExit 只传递方向及终止原因，不跨协程保留任何音频或事件正文。
type realtimeRelayExit struct {
	// upstream 为 true 表示上游读写失败；false 表示客户端读写失败或模型校验拒绝。
	upstream bool
	// err 是原连接错误；模型越权使用固定本地错误，避免回显客户端正文。
	err error
}

// relayRealtime 双向透传原始消息，任一方向结束即释放另一方向；返回前等待观察循环退出。
func relayRealtime(ctx context.Context, upstream *websocket.Conn, req ExecutionRequest, model string, observe func([]byte), result AttemptResult) (AttemptResult, error) {
	relayCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	exits := make(chan realtimeRelayExit, 2)
	go func() {
		for {
			kind, data, err := req.Realtime.Read(relayCtx)
			if err != nil {
				exits <- realtimeRelayExit{err: err}
				return
			}
			if kind == websocket.MessageText {
				data, err = realtimeClientFrame(data, req.Request.Model, model)
				if err != nil {
					exits <- realtimeRelayExit{err: err}
					return
				}
			}
			if err = writeRealtimeFrame(relayCtx, upstream, kind, data); err != nil {
				exits <- realtimeRelayExit{upstream: true, err: err}
				return
			}
		}
	}()
	go func() {
		for {
			kind, data, err := upstream.Read(relayCtx)
			if err != nil {
				exits <- realtimeRelayExit{upstream: true, err: err}
				return
			}
			if kind == websocket.MessageText {
				observe(data)
			}
			if err = writeRealtimeFrame(relayCtx, req.Realtime, kind, data); err != nil {
				exits <- realtimeRelayExit{err: err}
				return
			}
		}
	}()
	first := <-exits
	status := websocket.CloseStatus(first.err)
	// 先传播正常关闭或策略拒绝，再取消读协程；取消 Read 会直接关闭连接，不能先取消再发送 Close 帧。
	if ctx.Err() == nil {
		if errors.Is(first.err, errRealtimeModel) {
			_ = req.Realtime.Close(websocket.StatusPolicyViolation, "Realtime model 与连接绑定不一致")
		} else if status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway {
			if first.upstream {
				_ = req.Realtime.Close(status, "")
			} else {
				_ = upstream.Close(status, "")
			}
		}
	}
	cancel()
	_ = upstream.CloseNow()
	<-exits
	if ctx.Err() != nil {
		_ = req.Realtime.CloseNow()
		return result, ctx.Err()
	}
	if errors.Is(first.err, errRealtimeModel) {
		return result, errRealtimeModel
	}
	if status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway {
		result.Outcome.Success = true
		return result, nil
	}
	_ = req.Realtime.CloseNow()
	if !first.upstream {
		return result, context.Canceled
	}
	result.Outcome.Penalty = Heavy
	return result, errors.New("Realtime 上游连接异常中断")
}

// writeRealtimeFrame 限制单次写入等待为 30 秒，并沿用连接取消和总期限。
func writeRealtimeFrame(ctx context.Context, conn *websocket.Conn, kind websocket.MessageType, data []byte) error {
	writeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return conn.Write(writeCtx, kind, data)
}

// realtimeClientFrame 只重写显式模型别名；未知事件及非模型字段保留原字节或 RawMessage，避免丢失未来协议字段。
func realtimeClientFrame(data []byte, logical, mapped string) ([]byte, error) {
	var event map[string]json.RawMessage
	if json.Unmarshal(data, &event) != nil || event == nil {
		return data, nil
	}
	changed := false
	rewrite := func(object map[string]json.RawMessage) error {
		raw, ok := object["model"]
		if !ok {
			return nil
		}
		var model string
		if json.Unmarshal(raw, &model) != nil || model != logical && model != mapped {
			return errRealtimeModel
		}
		if model != mapped {
			object["model"], _ = json.Marshal(mapped)
			changed = true
		}
		return nil
	}
	if err := rewrite(event); err != nil {
		return nil, err
	}
	var kind string
	_ = json.Unmarshal(event["type"], &kind)
	container := ""
	if kind == "session.update" {
		container = "session"
	} else if kind == "response.create" {
		container = "response"
	}
	if container != "" {
		var object map[string]json.RawMessage
		if json.Unmarshal(event[container], &object) == nil && object != nil {
			if err := rewrite(object); err != nil {
				return nil, err
			}
			if changed {
				event[container], _ = json.Marshal(object)
			}
		}
	}
	if !changed {
		return data, nil
	}
	return json.Marshal(event)
}
