package verification

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/verification/fingerprint"
)

// scoringTestOutputs 读取第一组上游一致性样本；仅作为固定评分输入，不代表独立准确率评估集。
func scoringTestOutputs(t *testing.T) []fingerprint.Output {
	t.Helper()
	var fixture struct {
		// Cases 保留上游评分输入分组，第一组为三份不同的 Haiku 回答。
		Cases []struct {
			// Outputs 包含原始回答及上游生成时的期望数字数。
			Outputs []fingerprint.Output
		}
	}
	data, err := os.ReadFile("fingerprint/testdata/analyze-snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) == 0 || len(fixture.Cases[0].Outputs) != 3 {
		t.Fatal("固定评分输入必须包含三条不同回答")
	}
	return fixture.Cases[0].Outputs
}

// TestLiveRoundRepeatedEvidence 经真实采样、评分和 SQLite 重读，验证重复数字不产生接近或差异结论。
func TestLiveRoundRepeatedEvidence(t *testing.T) {
	outputs := scoringTestOutputs(t)
	numbers := fingerprint.ParseNumbers(outputs[0].Text)
	if len(numbers) < 230 {
		t.Fatal("固定回答不足以构造通过最小数量校验的不同长度前缀")
	}
	parts := make([]string, len(numbers))
	for i, number := range numbers {
		parts[i] = strconv.Itoa(number)
	}
	for _, test := range []struct {
		// name、texts 描述完全重放、仅两条重复及不同长度前缀的上游回答。
		name  string
		texts []string
	}{
		{name: "完全重放", texts: []string{outputs[0].Text, outputs[0].Text, outputs[0].Text}},
		{name: "格式变化的两条重放", texts: []string{outputs[0].Text, strings.ReplaceAll(outputs[0].Text, ",", "\n"), outputs[2].Text}},
		{name: "不同长度的同一序列", texts: []string{strings.Join(parts[:190], ", "), strings.Join(parts[:210], ", "), strings.Join(parts[:230], ", ")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-fixture-0" {
					t.Error("采样偏离固定目标或认证")
				}
				index := int(calls.Add(1)-1) % len(test.texts)
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": test.texts[index]}, "finish_reason": "stop"}}}); err != nil {
					t.Error(err)
				}
			}))
			defer upstream.Close()
			m, plan := fixture(t, upstream.URL)
			ctx := context.Background()
			plan.Lines = plan.Lines[:1]
			plan.Lines[0].ReferenceModel = "claude-haiku-4.5"
			if err := m.SavePlan(ctx, plan); err != nil {
				t.Fatal(err)
			}
			if err := m.Start(ctx, plan.ProviderID, ""); err != nil {
				t.Fatal(err)
			}
			m.wg.Wait()
			snapshot, err := m.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 3 || len(snapshot.Reports) != 1 {
				t.Fatalf("应只采样三次并保存一份报告: calls=%d reports=%d", calls.Load(), len(snapshot.Reports))
			}
			report := snapshot.Reports[0]
			if report.State != "complete" || report.Conclusion != "insufficient" || !strings.Contains(report.Reason, "无法作为三份独立证据") || len(report.Candidates) != 75 || len(report.Samples) != 3 {
				t.Fatalf("重复证据未保留原始分析并拒绝业务判定: %+v", report)
			}
			if test.name == "完全重放" && (report.Candidates[0].Probability == nil || *report.Candidates[0].Probability < 0.75) {
				t.Fatal("重放反例应保留高于判定门槛的原始置信度")
			}
			reopened, err := New(m.db, m.vendors, m.catalog)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			reread, err := reopened.Snapshot(ctx)
			if err != nil || !reflect.DeepEqual(reread.Reports, snapshot.Reports) || calls.Load() != 3 {
				t.Fatalf("重读改变报告或发起额外检测: %v", err)
			}
		})
	}
}

// TestCompareReferenceModelAlias 验证已确认的 GPT-6 模型别名不会因参考库稳定 ID 不同而误判。
func TestCompareReferenceModelAlias(t *testing.T) {
	candidates := []VerificationCandidate{
		{Model: "gpt-6-astra", Name: "gpt-6-astra/6.1-sol"},
		{Model: "gpt-4o", Name: "GPT-4o"},
	}
	tests := []struct {
		name       string
		prediction string
		reference  string
		want       string
		comparable bool
	}{
		{name: "原始模型 ID 对应参考库稳定 ID", prediction: "gpt-6-astra", reference: "gpt-6.1-sol", want: "match", comparable: true},
		{name: "参考库稳定 ID 精确对应", prediction: "gpt-6-astra", reference: "gpt-6-astra", want: "match", comparable: true},
		{name: "参考库展示名对应稳定 ID", prediction: "gpt-6-astra", reference: "gpt-6-astra/6.1-sol", want: "match", comparable: true},
		{name: "其他首位候选仍判差异", prediction: "gpt-4o", reference: "gpt-6.1-sol", want: "difference", comparable: true},
		{name: "预期身份不在候选集合时不可比较", prediction: "gpt-6-astra", reference: "claude-haiku-4.5", comparable: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, comparable := compareReferenceModel(test.prediction, candidates, test.reference)
			if got != test.want || comparable != test.comparable {
				t.Fatalf("compareReferenceModel() = %q, %v; want %q, %v", got, comparable, test.want, test.comparable)
			}
		})
	}
}
