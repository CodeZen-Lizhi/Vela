package scheduling

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestStandaloneContextContract 对照 Aether 检查独立通知、空 call_id、图片、工具配对与幂等性，未知输入不丢弃。
func TestStandaloneContextContract(t *testing.T) {
	raw := []byte(`{"previous_response_id":"resp-1","input":[{"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"},{"type":"function_call_output","name":"codex_apps_open_page","output":"{\"page_id\":null}"},{"type":"function_call_output","call_id":"c1","output":"ok"},{"type":"custom_tool_call_output","call_id":"","output":[{"type":"text","text":"snapshot"},{"type":"input_image","image_url":"data:image/png;base64,AQID"}]},{"type":"function_call_output","call_id":null,"output":"invalid"},{"type":"function_call_output","call_id":7,"output":"invalid"},{"type":"function_call_output","output":{"unknown":true}}]}`)
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if err := prepareRequest(body, "openai:responses", true); err != nil {
		t.Fatal(err)
	}
	var got []map[string]json.RawMessage
	if err := json.Unmarshal(body["input"], &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 || string(got[1]["call_id"]) != `"c1"` || string(body["previous_response_id"]) != `"resp-1"` {
		t.Fatal("配对或续接标识被改写")
	}
	if string(got[2]["call_id"]) != "null" || string(got[3]["call_id"]) != "7" || string(got[4]["output"]) != `{"unknown":true}` {
		t.Fatal("未知形状或非法标识被吞掉")
	}
	once, _ := json.Marshal(body)
	if err := prepareRequest(body, "openai:responses", true); err != nil {
		t.Fatal(err)
	}
	twice, _ := json.Marshal(body)
	if !reflect.DeepEqual(once, twice) {
		t.Fatal("重复整理不幂等")
	}
}

// TestEmptyOutputIsFailure 空终态不能回分；合法拒绝、推理与工具输出仍必须接受。
func TestEmptyOutputIsFailure(t *testing.T) {
	for _, tc := range []struct {
		// protocol、body 是原始协议输入；success 是有效终态与输出的组合预期。
		protocol, body string
		success        bool
	}{
		{"openai:chat", `{"choices":[{"message":{},"finish_reason":"stop"}]}`, false},
		{"openai:chat", `{"choices":[{"message":{"content":"","tool_calls":[]},"finish_reason":"stop"}]}`, false},
		{"openai:responses", `{"status":"completed","output":[]}`, false},
		{"claude:messages", `{"type":"message","stop_reason":"end_turn","content":[]}`, false},
		{"openai:chat", `{"choices":[{"message":{"refusal":"cannot comply"},"finish_reason":"stop"}]}`, true},
		{"openai:responses", `{"status":"completed","output":[{"type":"reasoning","encrypted_content":"opaque"}]}`, true},
		{"openai:responses", `{"status":"completed","output":[{"type":"function_call","name":"shell","arguments":"{}"}]}`, true},
		{"claude:messages", `{"type":"message","stop_reason":"tool_use","content":[{"type":"tool_use","name":"shell","input":{}}]}`, true},
	} {
		if got := validJSONTerminal(tc.protocol, []byte(tc.body)); got != tc.success {
			t.Errorf("%s: got %v for %s", tc.protocol, got, tc.body)
		}
	}
	state := streamTerminal{}
	if state.observe("openai:chat", "", `{"choices":[{"delta":{"tool_calls":[]}}]}`) || state.useful {
		t.Fatal("空工具数组不能提交或算输出")
	}
}

// TestStreamingContentAndTerminal 将协议结束与已输出内容分开验收，空工具数组和空终态不能阻止提交前故障转移。
func TestStreamingContentAndTerminal(t *testing.T) {
	for _, tc := range []struct {
		// protocol、body 是 SSE 原始事件，success、committed 是独立的业务结果和重放边界。
		protocol, body     string
		success, committed bool
	}{
		{"openai:chat", "data: [DONE]\n\n", false, false},
		{"openai:chat", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[]}}]}\n\ndata: {\"error\":{\"message\":\"failed\"}}\n\n", false, false},
		{"openai:chat", "data: {\"choices\":[{\"delta\":{\"refusal\":\"no\"}}]}\n\ndata: [DONE]\n\n", true, true},
		{"openai:responses", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n", false, false},
		{"openai:responses", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n", true, true},
	} {
		var out bytes.Buffer
		result, err := forwardSSE(context.Background(), strings.NewReader(tc.body), &out, tc.protocol, AttemptResult{})
		if (err == nil) != tc.success || result.Outcome.Success != tc.success || result.Committed != tc.committed {
			t.Errorf("%s: result=%+v err=%v", tc.body, result, err)
		}
	}
}
