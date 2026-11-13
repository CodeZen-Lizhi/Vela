package verification

import "testing"

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
