package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/credentials"
	"github.com/CodeZen-Lizhi/vela/internal/models"
	"github.com/CodeZen-Lizhi/vela/internal/providers"
	"github.com/CodeZen-Lizhi/vela/internal/records"
	"github.com/CodeZen-Lizhi/vela/internal/scheduling"
	"github.com/CodeZen-Lizhi/vela/internal/settings"
	"github.com/CodeZen-Lizhi/vela/internal/verification"
)

// transferService 为真实 SQLite 执行场景补齐账户、本机偏好与独立凭据，全部使用隔离目录。
func transferService(t *testing.T, f *executionFixture) *VelaService {
	t.Helper()
	a, err := providers.NewAccounts(f.vendors, nil)
	if err != nil {
		t.Fatal(err)
	}
	history, err := records.New(f.catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	f.execution.Executor.Records = history
	preferences, err := settings.New(f.catalog.DB())
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credentials.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = secrets.Set("gateway-access-key", []byte("local-access-key")); err != nil {
		t.Fatal(err)
	}
	return NewVelaService(Options{Models: f.catalog, Providers: f.vendors, Scheduling: f.policy, Accounts: a, Records: history, Settings: preferences, Credentials: secrets})
}

// transferPackageForTest 通过真实导出和文件读写取得配置，验证文件不包含明文凭据及其他本机数据。
func transferPackageForTest(t *testing.T, service *VelaService) string {
	t.Helper()
	content, err := service.ExportConfiguration(context.Background(), "transfer-test-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"key-a", "query-auth-token", "balance-auth-token", "local-access-key", "logical-model"} {
		if strings.Contains(content, secret) {
			t.Fatal("配置文件泄露了明文资源或认证信息")
		}
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err = writeConfigurationFile(path, content); err != nil {
		t.Fatal(err)
	}
	file, err := readConfigurationFile(path)
	if err != nil || file.Content != content {
		t.Fatal("导出文件不能实际读取", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 || !json.Valid([]byte(file.Content)) {
		t.Fatal("导出文件格式或权限错误", err)
	}
	return file.Content
}

// TestConfigurationTransferUsable 验证跨主密钥导出导入后，带认证的真实网关、余额和 Key 查询无需重新配置即可使用。
func TestConfigurationTransferUsable(t *testing.T) {
	ctx := context.Background()
	source := nativeFixture(t, "chat", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/balance":
			if r.Header.Get("Authorization") != "Bearer balance-auth-token" {
				t.Error("导入后余额认证缺失")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"amount":42}`)
		case "/key-info":
			body, _ := io.ReadAll(r.Body)
			if r.Header.Get("Authorization") != "Bearer query-auth-token" || string(body) != `{"key":"key-a"}` {
				t.Error("导入后 Key 查询认证或请求体缺失")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"name":"Upstream Key","rate":0.4}`)
		default:
			if r.Header.Get("Authorization") != "Bearer key-a" || r.URL.Path != "/v1/chat/completions" {
				t.Error("调用认证或请求地址未搬运")
			}
			io.WriteString(w, `{"choices":[{"message":{"content":"import-ready"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`)
		}
	})
	s := transferService(t, source)
	if _, err := s.accounts.SaveProviderAccount(ctx, source.provider.ID, providers.ProviderAccountDraft{Config: providers.QueryConfig{Mode: "custom", URL: source.provider.URL + "/balance", Method: "GET", Headers: `{"Authorization":"Bearer balance-auth-token"}`, BalancePath: "amount", Currency: "USD"}, MonthlyQuota: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.accounts.SaveProviderKeyQuery(ctx, source.keys[0].ID, providers.ProviderKeyQueryDraft{Config: providers.QueryConfig{Mode: "custom", URL: source.provider.URL + "/key-info", Method: "POST", Headers: `{"Authorization":"Bearer query-auth-token"}`, Body: `{"key":"key-a"}`}, NameSource: "upstream_key", MultiplierSource: "upstream", NamePath: "name", MultiplierPath: "rate"}); err != nil {
		t.Fatal(err)
	}
	if err := source.policy.SaveStrategy(ctx, scheduling.CostBased); err != nil {
		t.Fatal(err)
	}
	content := transferPackageForTest(t, s)
	target := newExecutionFixture(t)
	if err := target.vendors.Delete(ctx, target.provider.ID); err != nil {
		t.Fatal(err)
	}
	if err := target.catalog.Delete(ctx, "model-1"); err != nil {
		t.Fatal(err)
	}
	d := transferService(t, target)
	preview, err := d.PreviewConfiguration(content, "transfer-test-password")
	if err != nil || preview.Models != 1 || preview.Providers != 1 || preview.Keys != 2 || preview.Mappings != 2 {
		t.Fatal("预览数量错误", preview, err)
	}
	if _, err = d.ImportConfiguration(ctx, content, "transfer-test-password", "preserve"); err != nil {
		t.Fatal(err)
	}
	var sourceSecret, targetSecret []byte
	if err = source.catalog.DB().QueryRow("SELECT secret FROM provider_keys WHERE id=?", source.keys[0].ID).Scan(&sourceSecret); err != nil {
		t.Fatal(err)
	}
	if err = target.catalog.DB().QueryRow("SELECT secret FROM provider_keys WHERE id=?", source.keys[0].ID).Scan(&targetSecret); err != nil || reflect.DeepEqual(sourceSecret, targetSecret) {
		t.Fatal("目标机没有重新加密凭据", err)
	}
	status, body := compatibilityRequest(t, startCompatibilityGateway(t, target), "/v1/chat/completions", `{"model":"logical-model","messages":[{"role":"user","content":"hello"}]}`, nil)
	if status != 200 || !strings.Contains(string(body), "import-ready") {
		t.Fatal("导入后实际调用失败", status, string(body))
	}
	refreshed, err := d.accounts.RefreshProviderAccounts(ctx, source.provider.ID)
	if err != nil || len(refreshed.Accounts) != 1 || refreshed.Accounts[0].Balance.Amount == nil || *refreshed.Accounts[0].Balance.Amount != 42 || refreshed.Accounts[0].Balance.Status.State != "success" {
		t.Fatal("导入后余额查询不可用", err)
	}
	query, err := d.accounts.RefreshProviderKey(ctx, source.keys[0].ID)
	if err != nil || query.NameStatus.State != "success" || query.MultiplierStatus.State != "success" {
		t.Fatal("导入后 Key 查询不可用", err)
	}
	policy, err := d.scheduling.Policy(ctx)
	if err != nil || policy.Strategy != scheduling.CostBased {
		t.Fatal("空目标实例没有应用导入策略", policy, err)
	}
}

// protectedTransferData 逐值读取历史、统计与偏好；累计表的当前名称缓存随正常改名更新，身份和全部数值仍须完全一致。
func protectedTransferData(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	result := map[string]string{}
	tables, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND (name LIKE 'request_%' OR name LIKE 'verification_%' OR name='settings') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for tables.Next() {
		var name string
		if err = tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err = tables.Err(); err != nil {
		t.Fatal(err)
	}
	tables.Close()
	for _, table := range names {
		rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `" ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		all := [][]any{}
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			for i, column := range columns {
				if table == "request_totals" && (column == "model_name" || column == "provider_name") {
					// R20 要求历史显示当前名称；既有重命名触发器只更新这两个展示缓存。
					values[i] = nil
				}
			}
			all = append(all, values)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		data, err := json.Marshal(all)
		if err != nil {
			t.Fatal(err)
		}
		result[table] = string(data)
	}
	return result
}

// TestConfigurationTransferMerge 验证两种合并模式、模型/供应商/Key 身份重映射、重复导入及预览后新增记录保护。
func TestConfigurationTransferMerge(t *testing.T) {
	ctx := context.Background()
	source := newExecutionFixture(t)
	s := transferService(t, source)
	if _, err := source.catalog.Create(ctx, models.Create{Name: "新增模型", ModelID: "new-model", Kind: "chat"}); err != nil {
		t.Fatal(err)
	}
	if err := source.policy.SaveStrategy(ctx, scheduling.CostBased); err != nil {
		t.Fatal(err)
	}
	content := transferPackageForTest(t, s)
	for _, mode := range []string{"preserve", "overwrite"} {
		t.Run(mode, func(t *testing.T) {
			f := newExecutionFixture(t)
			d := transferService(t, f)
			if _, err := f.catalog.Update(ctx, models.Edit{ID: "model-1", Name: "本机模型"}); err != nil {
				t.Fatal(err)
			}
			if err := d.settings.SaveGateway(ctx, settings.GatewaySettings{Port: 19999}); err != nil {
				t.Fatal(err)
			}
			if err := f.policy.SaveStrategy(ctx, scheduling.FixedOrder); err != nil {
				t.Fatal(err)
			}
			if _, err := f.catalog.Create(ctx, models.Create{Name: "独有模型", ModelID: "local-only", Kind: "chat"}); err != nil {
				t.Fatal(err)
			}
			if _, err := d.PreviewConfiguration(content, "transfer-test-password"); err != nil {
				t.Fatal(err)
			}
			verifier, err := verification.New(f.catalog.DB(), f.vendors, f.catalog)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(verifier.Close)
			if err = verifier.SavePlan(ctx, verification.VerificationPlan{ProviderID: f.provider.ID, Hours: 6, Lines: []verification.VerificationLine{{ModelID: "model-1", KeyID: f.keys[0].ID, Protocol: "chat_completions", Effort: "auto"}}}); err != nil {
				t.Fatal(err)
			}
			if _, err = f.catalog.DB().Exec("INSERT INTO verification_reports(id,provider_id,model_id,key_id,document) VALUES('preserved-report',?,?,?,?)", f.provider.ID, "model-1", f.keys[0].ID, `{"id":"preserved-report","time":123}`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.execution.Executor.Execute(ctx, f.request(), io.Discard); err != nil {
				t.Fatal(err)
			}
			before := protectedTransferData(t, f.catalog.DB())
			for range 2 {
				if _, err := d.ImportConfiguration(ctx, content, "transfer-test-password", mode); err != nil {
					t.Fatal(err)
				}
			}
			if after := protectedTransferData(t, f.catalog.DB()); !reflect.DeepEqual(before, after) {
				for table, value := range before {
					if value != after[table] {
						t.Errorf("导入改变表 %s: %s -> %s", table, value, after[table])
					}
				}
				t.Fatal("导入改变了使用记录、总览或本机偏好")
			}
			list, err := d.ListModels(ctx)
			if err != nil || len(list) != 3 {
				t.Fatal("新增模型丢失、本机独有模型被删除或重复导入", err)
			}
			for _, m := range list {
				if m.ModelID == "logical-model" && ((mode == "preserve" && m.Name != "本机模型") || (mode == "overwrite" && m.Name != "Logical Model")) {
					t.Fatal("模型覆盖模式未生效")
				}
			}
			keys, err := f.vendors.Keys(ctx, f.provider.ID)
			if err != nil || len(keys) != 2 || keys[0].ID != f.keys[0].ID {
				t.Fatal("相同供应商/密钥没有保留本机身份", err)
			}
			policy, err := f.policy.Policy(ctx)
			if err != nil || (mode == "preserve" && policy.Strategy != scheduling.FixedOrder) || (mode == "overwrite" && policy.Strategy != scheduling.CostBased) {
				t.Fatal("调度合并模式错误", policy, err)
			}
			if value, err := d.accessKey(); err != nil || value != "local-access-key" {
				t.Fatal("导入改变了本机访问密钥", err)
			}
		})
	}
}

// TestConfigurationTransferDefaultStrategyPreserved 验证默认策略导入后也成为已保存选择，后续保留导入不能将其覆盖。
func TestConfigurationTransferDefaultStrategyPreserved(t *testing.T) {
	ctx := context.Background()
	source := newExecutionFixture(t)
	s := transferService(t, source)
	first := transferPackageForTest(t, s)
	target := newExecutionFixture(t)
	d := transferService(t, target)
	if _, err := d.ImportConfiguration(ctx, first, "transfer-test-password", "preserve"); err != nil {
		t.Fatal(err)
	}
	if err := source.policy.SaveStrategy(ctx, scheduling.CostBased); err != nil {
		t.Fatal(err)
	}
	second := transferPackageForTest(t, s)
	if _, err := d.ImportConfiguration(ctx, second, "transfer-test-password", "preserve"); err != nil {
		t.Fatal(err)
	}
	policy, err := target.policy.Policy(ctx)
	if err != nil || policy.Strategy != scheduling.DefaultStrategy {
		t.Fatal("后续保留导入覆盖了已导入的默认策略", policy, err)
	}
}

// TestConfigurationTransferRejectAndRollback 验证错误密码、损坏包、无效版本/关联、非法模式及中途数据库失败均不留下部分配置。
func TestConfigurationTransferRejectAndRollback(t *testing.T) {
	ctx := context.Background()
	f := newExecutionFixture(t)
	s := transferService(t, f)
	content := transferPackageForTest(t, s)
	if _, err := s.PreviewConfiguration(content, "incorrect-password"); err == nil {
		t.Fatal("错误密码通过")
	}
	var envelope configurationEnvelope
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Data[len(envelope.Data)-1] ^= 1
	damaged, _ := json.Marshal(envelope)
	if _, err := s.PreviewConfiguration(string(damaged), "transfer-test-password"); err == nil {
		t.Fatal("损坏文件通过")
	}
	envelope.Version = 99
	unsupported, _ := json.Marshal(envelope)
	if _, err := s.PreviewConfiguration(string(unsupported), "transfer-test-password"); err == nil {
		t.Fatal("未知版本通过")
	}
	if _, err := s.ImportConfiguration(ctx, content, "transfer-test-password", "replace-all"); err == nil {
		t.Fatal("非法模式通过")
	}
	bundle, err := parseConfiguration(content, "transfer-test-password")
	if err != nil {
		t.Fatal(err)
	}
	bundle.Models[0].Name = "不应保存的模型名称"
	plain, _ := json.Marshal(bundle)
	changed, err := encryptConfiguration(plain, "transfer-test-password")
	if err != nil {
		t.Fatal(err)
	}
	// 模型写入之后、供应商更新时主动失败，观察跨模块回滚而不是只测试预校验拒绝。
	if _, err = f.catalog.DB().Exec(`CREATE TRIGGER reject_configuration BEFORE UPDATE ON providers BEGIN SELECT RAISE(ABORT,'injected import failure'); END`); err != nil {
		t.Fatal(err)
	}
	before, err := s.ExportConfiguration(ctx, "transfer-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ImportConfiguration(ctx, changed, "transfer-test-password", "overwrite"); err == nil {
		t.Fatal("中途写入失败没有报告")
	}
	after, err := s.ExportConfiguration(ctx, "transfer-test-password")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := parseConfiguration(before, "transfer-test-password")
	a, _ := parseConfiguration(after, "transfer-test-password")
	if !reflect.DeepEqual(b, a) {
		t.Fatal("导入中途失败后遗留了部分配置")
	}
	bundle.Scheduling.ProviderOrder = []string{"missing-provider"}
	plain, _ = json.Marshal(bundle)
	invalid, err := encryptConfiguration(plain, "transfer-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PreviewConfiguration(invalid, "transfer-test-password"); err == nil {
		t.Fatal("悬空调度引用通过")
	}
}

// TestConfigurationFileBoundaries 验证输入体积、外部包格式与失败保存对原文件的保护。
func TestConfigurationFileBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(configurationLimit + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err = readConfigurationFile(path); err == nil {
		t.Fatal("超限文件被读取")
	}
	for _, password := range []string{"short", "        "} {
		if _, err = encryptConfiguration([]byte(`{}`), password); err == nil {
			t.Fatal("无效密码被接受")
		}
	}
	for _, content := range []string{`{"type":"backup","version":1}`, `{"format":"vela-configuration","version":99}`, "not-json"} {
		if _, err = decryptConfiguration(content, "transfer-test-password"); err == nil {
			t.Fatal("非正式配置包被接受")
		}
	}
	destination := t.TempDir()
	sentinel := filepath.Join(destination, "preserved.txt")
	if err = os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = writeConfigurationFile(destination, "data"); err == nil {
		t.Fatal("以目录为目标保存没有报错")
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
		t.Fatal("失败保存破坏了原目标", err)
	}
	entries, err := os.ReadDir(filepath.Dir(destination))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".vela-config-") {
			t.Fatal("失败保存留下临时文件")
		}
	}
}
