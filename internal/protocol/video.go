// Package protocol 保存诊断与正式网关共用的原生协议编码，不依赖调度或桌面宿主。
package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
)

// VideoForm 将已有 JSON 视频字段编码为上游原生表单；limit 是编码后字节上限。
func VideoForm(data []byte, limit int) ([]byte, string, error) {
	var body map[string]json.RawMessage
	if json.Unmarshal(data, &body) != nil || body == nil {
		return nil, "", errors.New("视频配置无效")
	}
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for key, raw := range body {
		var value string
		if json.Unmarshal(raw, &value) != nil {
			var number json.Number
			if json.Unmarshal(raw, &number) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return nil, "", errors.New("视频配置只支持字符串或数字字段")
			}
			value = number.String()
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, "", errors.New("视频配置不支持 null 字段")
		}
		if err := writer.WriteField(key, value); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	if buffer.Len() > limit {
		return nil, "", errors.New("视频请求正文超过容量上限")
	}
	return buffer.Bytes(), writer.FormDataContentType(), nil
}
