package scheduling

import (
	"context"
	"fmt"
	"io"
	"time"
)

// errStreamIdleTimeout 表示等待下一段上游数据超时，保留标准超时身份以便记录和分类。
var errStreamIdleTimeout = fmt.Errorf("等待上游流式数据超时: %w", context.DeadlineExceeded)

// streamIdleReader 只限制等待上游数据的空闲时间；每次真实读取到字节后，下一次读取重新计时。
type streamIdleReader struct {
	// ReadCloser 是实际 HTTP 正文或 WebSocket 事件流，保留原始关闭能力及传输字节。
	io.ReadCloser
	// timeoutMillis 复用历史 streamTotalMillis 配置；零使用默认 120 秒。
	timeoutMillis int
	// cancel 中断所属尝试的阻塞 IO，由调用方在请求结束时另行取消清理。
	cancel context.CancelFunc
}

// Read 在无数据时取消实际网络读取；SSE 心跳、事件片段和 WebSocket 数据都可重新开始下一段等待。
func (r *streamIdleReader) Read(p []byte) (int, error) {
	millis := r.timeoutMillis
	if millis == 0 {
		millis = 120_000
	}
	timer := time.AfterFunc(time.Duration(millis)*time.Millisecond, r.cancel)
	var n int
	var err error
	for {
		n, err = r.ReadCloser.Read(p)
		if n > 0 || err != nil || len(p) == 0 {
			break
		}
	}
	if !timer.Stop() {
		err = errStreamIdleTimeout
	}
	return n, err
}
