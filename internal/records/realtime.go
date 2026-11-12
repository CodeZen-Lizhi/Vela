package records

import (
	"io"
	"net/http"
	"strings"
	"sync"
)

// PrepareRealtimeCapture 在真实 Dial 前登记一次连接尝试；observe 按接收顺序观察上游 JSON 事件。
// finish 在观察循环退出后调用，status 保留握手实际状态（未收到响应为零），cause 保留失败或取消原因。
// 事件与音频正文均不保存；finish 只结算该次连接尝试，逻辑记录仍由 Begin 返回的函数统一收尾。
// observe 和 finish 不可并发调用，finish 可重复调用；没有记录会话时均为空操作。
func PrepareRealtimeCapture(req *http.Request) (observe func([]byte), finish func(int, error) error, err error) {
	if !HasSession(req.Context()) {
		return func([]byte) {}, func(int, error) error { return nil }, nil
	}
	capture, err := PrepareStreamCapture(req, true)
	if err != nil {
		return nil, nil, err
	}
	response, _ := capture(&http.Response{Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil)
	body := response.Body.(*captureBody)
	body.payload.BodyState = "unavailable"
	var once sync.Once
	var finishErr error
	observe = func(data []byte) {
		if len(data) > 0 {
			body.observeFirstByte()
		}
		body.observeUsage(data)
	}
	finish = func(status int, cause error) error {
		once.Do(func() {
			body.attempt.Status = status
			if cause != nil {
				body.attempt.Error = sanitize(cause.Error(), body.secrets)
			}
			body.complete()
			body.run.mu.Lock()
			finishErr = body.run.writeErr
			body.run.mu.Unlock()
		})
		return finishErr
	}
	return observe, finish, nil
}
