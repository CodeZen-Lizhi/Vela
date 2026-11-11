package models

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// TestEditRevision 通过真实 HTTP、编辑和 SQLite 重读验证同步/重复保存冲突，以及旧记录的版本兼容。
func TestEditRevision(t *testing.T) {
	ctx := context.Background()
	var version atomic.Int32
	version.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"acme":{"models":{"m":{"name":"M","cost":{"input":%d,"output":5}}}}}`, version.Load())
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "models.sqlite")
	s, err := Open(path, server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	if _, err = s.Catalog(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := s.Add(ctx, "acme", "m")
	if err != nil {
		t.Fatal(err)
	}
	draft := Edit{ID: m.ID, Revision: m.Revision, Name: "只改名称", Pricing: m.Pricing}
	version.Store(2)
	if _, err = s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := s.get(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Update(ctx, draft); err == nil || !strings.Contains(err.Error(), "模型已更新") {
		t.Fatal("同步后的旧草稿必须被拒绝", err)
	}
	after, err := s.get(ctx, m.ID)
	if err != nil || !reflect.DeepEqual(before, after) || after.Pricing.Base[0].Price != 2 || len(after.Overrides) != 0 {
		t.Fatalf("冲突保存改变了有效价格或保护: %+v %v", after, err)
	}
	draft.Revision, draft.Pricing = after.Revision, after.Pricing
	saved, err := s.Update(ctx, draft)
	if err != nil || saved.Name != draft.Name || saved.Revision != after.Revision+1 || len(saved.Overrides) != 0 {
		t.Fatalf("重新读取后应可保存名称: %+v %v", saved, err)
	}
	if _, err = s.Update(ctx, draft); err == nil {
		t.Fatal("同版本重复提交必须拒绝")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 1 || !reflect.DeepEqual(saved, list[0]) {
		t.Fatalf("重启后版本与配置未保留: %+v %v", list, err)
	}
	// 模拟升级前的真实文档格式，缺失版本按零读取，首次保存后开始正常校验。
	if _, err = s.db.Exec(`UPDATE models SET document=json_remove(document,'$.revision') WHERE id=?`, m.ID); err != nil {
		t.Fatal(err)
	}
	legacy, err := s.get(ctx, m.ID)
	if err != nil || legacy.Revision != 0 {
		t.Fatal("旧文档不能读取", err)
	}
	legacy, err = s.Update(ctx, Edit{ID: legacy.ID, Revision: legacy.Revision, Name: legacy.Name, Pricing: legacy.Pricing})
	if err != nil || legacy.Revision != 1 {
		t.Fatal("旧文档首次保存失败", err)
	}
}

// TestRerankingImportAndRepair 验证真实来源类型别名及旧误分类记录的同步修正，不丢失本地输入价。
func TestRerankingImportAndRepair(t *testing.T) {
	for _, kind := range []string{"reranking", "rerank", "embedding"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"novita-ai":{"models":{"baai/bge-reranker-v2-m3":{"name":"BGE","type":%q,"cost":{"input":0.01,"output":0}}}}}`, kind)
			}))
			defer server.Close()
			s, err := Open(filepath.Join(t.TempDir(), "models.sqlite"), server.Client(), server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err = s.Catalog(ctx); err != nil {
				t.Fatal(err)
			}
			m, err := s.Create(ctx, Create{ProviderID: "novita-ai", ModelID: "baai/bge-reranker-v2-m3", Name: "BGE", Pricing: Pricing{Base: []Rate{{Key: "input", Unit: "million_tokens", Price: 0.01}}}})
			if err != nil || m.Kind != "vector" || len(m.SourcePricing.Base) != 1 || len(m.Pricing.Base) != 1 {
				t.Fatalf("向量类型或价格转换错误: %+v %v", m, err)
			}
			// 构造旧导入器保存的误分类配置，同时保留用户已修改的完整规则组。
			m.Kind = "chat"
			m.Pricing.Base = []Rate{{Key: "input", Unit: "million_tokens", Price: 9}, {Key: "output", Unit: "million_tokens", Price: 0}}
			m.Pricing.Tiers = []Tier{{Above: 200000, Rates: append([]Rate{}, m.Pricing.Base...)}}
			m.Overrides = []string{"base", "tiers"}
			m, err = s.save(ctx, m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Sync(ctx); err != nil {
				t.Fatal(err)
			}
			saved, err := s.get(ctx, m.ID)
			if err != nil || saved.Kind != "vector" || len(saved.Pricing.Base) != 1 || saved.Pricing.Base[0].Price != 9 || len(saved.Pricing.Tiers[0].Rates) != 1 || !reflect.DeepEqual(saved.Overrides, m.Overrides) {
				t.Fatalf("误分类修正丢失本地规则: %+v %v", saved, err)
			}
		})
	}
}

// TestReasoningPriceValidation 覆盖普通、阶梯和 Fast 价格，拒绝不能表达的推理价并保留明确同价与零价。
func TestReasoningPriceValidation(t *testing.T) {
	for _, raw := range []string{
		`{"input":2,"output":8,"reasoning":3}`,
		`{"input":2,"reasoning":3}`,
		`{"output":null,"reasoning":0}`,
		`{"output":8,"reasoning":null}`,
		`{"output":8,"reasoning":"8"}`,
		`{"output":8,"reasoning":-1}`,
	} {
		for _, where := range []string{"base", "tier", "fast"} {
			t.Run(where+raw, func(t *testing.T) {
				cost := raw
				var modes map[string]struct {
					// Cost 保留 Fast 来源原始价格以经过实际导入校验。
					Cost json.RawMessage `json:"cost"`
				}
				if where == "tier" {
					cost = `{"input":1,"output":2,"tiers":[` + strings.TrimSuffix(raw, "}") + `,"tier":{"type":"context","size":200000}}]}`
				} else if where == "fast" {
					cost = `{"input":1,"output":2}`
					if err := json.Unmarshal([]byte(`{"fast":{"cost":`+raw+`}}`), &modes); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := decodePricing([]byte(cost), modes, "chat"); err == nil || !strings.Contains(err.Error(), "reasoning") {
					t.Fatal("不可表达的推理单价未被拒绝", err)
				}
			})
		}
	}
	for _, raw := range []string{`{"input":2,"output":8}`, `{"input":2,"output":8,"reasoning":8}`, `{"input":0,"output":0,"reasoning":0}`} {
		if _, err := decodePricing([]byte(raw), nil, "chat"); err != nil {
			t.Fatal("无独立推理价或同价不应拒绝", err)
		}
	}
}

// TestReasoningPartialSync 验证新增与同步均明确报错，失败模型配置完整保留，其他模型继续更新。
func TestReasoningPartialSync(t *testing.T) {
	var independent atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if independent.Load() {
			fmt.Fprint(w, `{"openrouter":{"models":{"sonar":{"cost":{"input":2,"output":8,"reasoning":3}},"other":{"cost":{"input":5}}}}}`)
		} else {
			fmt.Fprint(w, `{"openrouter":{"models":{"sonar":{"cost":{"input":2,"output":8}},"other":{"cost":{"input":1}}}}}`)
		}
	}))
	defer server.Close()
	s, err := Open(filepath.Join(t.TempDir(), "models.sqlite"), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err = s.Catalog(ctx); err != nil {
		t.Fatal(err)
	}
	sonar, err := s.Add(ctx, "openrouter", "sonar")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Add(ctx, "openrouter", "other")
	if err != nil {
		t.Fatal(err)
	}
	independent.Store(true)
	if _, err = s.Catalog(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Create(ctx, Create{ProviderID: "openrouter", ModelID: "sonar", Name: "Sonar"}); err == nil || !strings.Contains(err.Error(), "reasoning") {
		t.Fatal("新增应明确报告不支持的推理单价", err)
	}
	result, err := s.Sync(ctx)
	if err != nil || result.Updated != 1 || len(result.Failures) != 1 || !strings.Contains(result.Failures[0], "reasoning") {
		t.Fatalf("部分同步结果错误: %+v %v", result, err)
	}
	saved, err := s.get(ctx, sonar.ID)
	if err != nil || !reflect.DeepEqual(sonar, saved) {
		t.Fatalf("失败模型配置被修改: %+v %v", saved, err)
	}
	saved, err = s.get(ctx, other.ID)
	if err != nil || saved.Pricing.Base[0].Price != 5 {
		t.Fatalf("有效模型未继续同步: %+v %v", saved, err)
	}
}
