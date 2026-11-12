package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
)

// TestChatAudioAggregateGateway 经真实鉴权网关、Executor 和本地 SSE 验证两段音频、padding 和转写的同步回传。
func TestChatAudioAggregateGateway(t *testing.T) {
	for _, tc := range []struct {
		// name 区分分段编码；first、second 是独立完整音频段，want 为按顺序连接的原始字节。
		name, first, second string
		want                []byte
	}{
		{"without-padding", "AQID", "BAUG", []byte{1, 2, 3, 4, 5, 6}},
		{"with-padding", "AQ==", "AgM=", []byte{1, 2, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExecutionFixture(t)
			f.handle = func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				if r.Header.Get("Authorization") != "Bearer key-a" || body["stream"] != false {
					t.Error("同步音频请求未沿用真实认证和客户端模式")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: {\"id\":\"chat-audio-1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"audio\":{\"id\":\"audio-1\",\"data\":%q,\"transcript\":\"hello \"}}}]}\n\n", tc.first)
				_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"audio\":{\"data\":%q,\"transcript\":\"world\"}},\"finish_reason\":\"stop\"}]}\n\n", tc.second)
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}
			status, out := compatibilityRequest(t, startCompatibilityGateway(t, f), "/v1/chat/completions", `{"model":"logical-model","stream":false,"messages":[],"modalities":["text","audio"],"audio":{"voice":"alloy","format":"pcm16"}}`, nil)
			var response struct {
				// Object、Choices 验证客户端收到同步 Chat 信封和完整音频，而非原始 SSE。
				Object  string
				Choices []struct {
					Message struct {
						Audio struct{ Data, Transcript, ID string }
					}
				}
			}
			if status != http.StatusOK || json.Unmarshal(out, &response) != nil || response.Object != "chat.completion" || len(response.Choices) != 1 {
				t.Fatalf("同步网关回传失败: HTTP=%d body=%s", status, out)
			}
			audio := response.Choices[0].Message.Audio
			decoded, err := base64.StdEncoding.DecodeString(audio.Data)
			if err != nil || !bytes.Equal(decoded, tc.want) || audio.Data != base64.StdEncoding.EncodeToString(tc.want) || audio.Transcript != "hello world" || audio.ID != "audio-1" {
				t.Fatalf("真实网关丢失音频/转写: audio=%+v err=%v", audio, err)
			}
		})
	}
}
