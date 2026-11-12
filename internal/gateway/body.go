package gateway

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// bodyError 保留入口失败的 HTTP 分类，不把压缩格式或容量错误误报为 JSON 语法错误。
type bodyError struct {
	// status 是客户端响应码；message 不含原始正文。
	status  int
	message string
}

// Error 返回可直接展示的稳定错误说明。
func (e *bodyError) Error() string { return e.message }

// readRequestBody 按 Content-Encoding 的逆序解码正文，各阶段都执行同一容量检查。
func readRequestBody(r *http.Request) ([]byte, error) {
	if r.ContentLength > requestBodyLimit {
		return nil, &bodyError{http.StatusRequestEntityTooLarge, "请求正文超过 128 MiB"}
	}
	data, err := readBody(r.Body)
	if err != nil {
		return nil, err
	}
	encodings := strings.Split(strings.Join(r.Header.Values("Content-Encoding"), ","), ",")
	for i := len(encodings) - 1; i >= 0; i-- {
		encoding := strings.ToLower(strings.TrimSpace(encodings[i]))
		if encoding == "" || encoding == "identity" {
			continue
		}
		var reader io.Reader
		var closeReader func()
		source := bytes.NewReader(data)
		switch encoding {
		case "gzip", "x-gzip":
			decoded, decodeErr := gzip.NewReader(source)
			err = decodeErr
			if err == nil {
				reader, closeReader = decoded, func() { _ = decoded.Close() }
			}
		case "deflate":
			decoded, decodeErr := zlib.NewReader(source)
			if decodeErr != nil {
				// HTTP 中两种 deflate 封装均有现有客户端使用，沿用旧版的兼容顺序。
				decoded = flate.NewReader(bytes.NewReader(data))
			}
			reader, closeReader = decoded, func() { _ = decoded.Close() }
		case "zstd":
			decoded, decodeErr := zstd.NewReader(source, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(requestBodyLimit))
			err = decodeErr
			if err == nil {
				reader, closeReader = decoded, decoded.Close
			}
		default:
			return nil, &bodyError{http.StatusUnsupportedMediaType, "不支持的 Content-Encoding"}
		}
		if err != nil {
			return nil, &bodyError{http.StatusBadRequest, "压缩请求正文无效"}
		}
		data, err = readBody(reader)
		closeReader()
		if err != nil {
			return nil, err
		}
	}
	// 后续发送的是解码后的 JSON/二进制，原编码及长度不能随业务头继续转发。
	r.Header.Del("Content-Encoding")
	r.Header.Del("Content-Length")
	return data, nil
}

// readBody 读取一个有界正文；超限显式失败，绝不把截断内容交给执行器。
func readBody(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, requestBodyLimit+1))
	if len(data) > requestBodyLimit {
		return nil, &bodyError{http.StatusRequestEntityTooLarge, "请求正文超过 128 MiB"}
	}
	if err != nil {
		return nil, &bodyError{http.StatusBadRequest, "读取或解码请求正文失败"}
	}
	return data, nil
}

// bodyErrorStatus 提取已分类入口错误，未知读取故障按非法请求处理。
func bodyErrorStatus(err error) int {
	var failure *bodyError
	if errors.As(err, &failure) {
		return failure.status
	}
	return http.StatusBadRequest
}
