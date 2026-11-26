package providers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/vela/internal/credentials"
	"github.com/CodeZen-Lizhi/vela/internal/records"
)

// exportTransferFixture 经真实只读事务导出，设置超时可暴露单连接 SQLite 误用 db 的死锁。
func exportTransferFixture(t *testing.T, s *Store) Configuration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	value, err := s.ExportConfiguration(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return value
}

// transferFixture 使用完整真实模型表、账户表和文档加密建立来源数据；认证由查询配置正常保存入口加密。
func transferFixture(t *testing.T, base string) (modelFixture, *Accounts, string) {
	t.Helper()
	f := newModelFixture(t)
	secrets, err := credentials.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.protectDocuments(secrets); err != nil {
		t.Fatal(err)
	}
	a, err := NewAccounts(f.store, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	d := validDraft("供应商")
	d.URL = base
	if _, err = f.store.Update(ctx, f.provider, d); err != nil {
		t.Fatal(err)
	}
	if err = f.store.SaveModels(ctx, f.provider, ProviderModelsDraft{ModelIDs: f.ids[:2]}); err != nil {
		t.Fatal(err)
	}
	if err = f.store.SaveMappings(ctx, f.provider, []ProviderModelMappingsDraft{{ModelID: f.ids[0], Mappings: []ProviderModelMapping{{RequestName: "upstream-first", Priority: 2}, {RequestName: "upstream-second", Priority: 1}}}}); err != nil {
		t.Fatal(err)
	}
	config := QueryConfig{Mode: "custom", Method: "POST", URL: base + "/balance?token=fixture-url-token", Headers: `{"Authorization":"Bearer fixture-auth-token"}`, Body: `{"credential":"fixture-body-token"}`, BalancePath: "balance", Currency: "CNY"}
	if _, err = a.SaveProviderAccount(ctx, f.provider, ProviderAccountDraft{Config: config, MonthlyQuota: 27.5, Checkin: true}); err != nil {
		t.Fatal(err)
	}
	config.URL = base + "/key?token=fixture-key-token"
	q := ProviderKeyQueryDraft{Config: config, NameSource: "upstream_key", MultiplierSource: "upstream", NamePath: "name", GroupPath: "group", MultiplierPath: "rate", KeyPath: "secret"}
	id, _, err := a.saveKeyLocal(ctx, f.provider, "", keyDraft("来源主力", "sk-transfer-full-credential", 2), q)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.SaveModelPermissions(ctx, id, ProviderModelPermissions{ModelIDs: f.ids[:1]}); err != nil {
		t.Fatal(err)
	}
	if err = f.store.SetEnabled(ctx, f.provider, false); err != nil {
		t.Fatal(err)
	}
	return f, a, id
}

// importTransferFixture 经真实配置事务导入，确保目的机器的单连接与主密钥准备路径均被覆盖。
func importTransferFixture(t *testing.T, s *Store, value Configuration, modelIDs map[string]string, overwrite bool) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var mapping map[string]string
	err := s.ConfigurationTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		mapping, err = s.ImportConfiguration(ctx, tx, value, modelIDs, overwrite)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return mapping
}

// TestConfigurationCrossMachine 验证真实解密、不同机器重新加密、模型重映射、停用及空秘密 Key，以及导入后 HTTP 认证可用。
func TestConfigurationCrossMachine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-auth-token" || r.Method != "POST" {
			http.Error(w, "authentication failed", http.StatusUnauthorized)
			return
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["credential"] != "fixture-body-token" {
			http.Error(w, "body authentication failed", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/balance" && r.URL.Query().Get("token") == "fixture-url-token" {
			fmt.Fprint(w, `{"balance":89}`)
			return
		}
		if r.URL.Path == "/key" && r.URL.Query().Get("token") == "fixture-key-token" {
			fmt.Fprint(w, `{"name":"已认证主力","group":"生产","rate":1.75}`)
			return
		}
		http.Error(w, "URL authentication failed", http.StatusUnauthorized)
	}))
	defer server.Close()
	source, _, sourceKey := transferFixture(t, server.URL)
	value := exportTransferFixture(t, source.store)
	if len(value.Providers) != 1 || len(value.Providers[0].Keys) != 4 || value.Providers[0].Provider.Enabled {
		t.Fatal("未完整导出停用供应商与空秘密 Key")
	}
	key := value.Providers[0].Keys[3]
	if key.ID != sourceKey || key.Draft.Secret != "sk-transfer-full-credential" || key.Draft.Multiplier != nil || key.Query.Config.Headers != `{"Authorization":"Bearer fixture-auth-token"}` {
		t.Fatal("完整秘密或未知上游倍率未保留")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"healthScore", "lastSuccess", `"balance":`, "secretMask", "validationId", "masterKey"} {
		if bytes.Contains(encoded, []byte(unwanted)) {
			t.Fatalf("导出包含运行结果字段 %s", unwanted)
		}
	}
	destination := newModelFixture(t)
	secrets, err := credentials.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = destination.store.protectDocuments(secrets); err != nil {
		t.Fatal(err)
	}
	models := map[string]string{}
	for i, id := range source.ids {
		models[id] = destination.ids[i]
	}
	mapping := importTransferFixture(t, destination.store, value, models, true)
	if mapping[source.provider] != destination.provider {
		t.Fatal("同名供应商应保留目的身份")
	}
	sourceMaster, err := source.store.existingMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	destMaster, err := destination.store.existingMasterKey()
	if err != nil || bytes.Equal(sourceMaster, destMaster) {
		t.Fatal("目的机器必须使用独立主密钥")
	}
	var sealed []byte
	if err = destination.store.db.QueryRow("SELECT secret FROM provider_keys WHERE id=?", sourceKey).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if _, err = open(sourceMaster, secretBinding(sourceKey), sealed); err == nil {
		t.Fatal("来源主密钥不应能解开目的机器密文")
	}
	if secret, err := destination.store.Secret(context.Background(), sourceKey); err != nil || secret != key.Draft.Secret {
		t.Fatal("目的机器无法读取真实调用凭据")
	}
	assertModelPermissions(t, destination.store, sourceKey, false, destination.ids[0])
	providerModels, err := destination.store.Models(context.Background(), destination.provider)
	if err != nil || len(providerModels) != 2 {
		t.Fatal("模型关联或有序映射未正确重映射")
	}
	for _, m := range providerModels {
		if m.ModelID == destination.ids[0] && (len(m.Mappings) != 2 || m.Mappings[0].RequestName != "upstream-first") {
			t.Fatal("模型映射顺序未保留")
		}
	}
	// 重复导入不创建重复资源，且语义配置完全相同。
	before := exportTransferFixture(t, destination.store)
	importTransferFixture(t, destination.store, value, models, true)
	after := exportTransferFixture(t, destination.store)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("重复导入未达到配置幂等")
	}
	var afterSealed []byte
	if err = destination.store.db.QueryRow("SELECT secret FROM provider_keys WHERE id=?", sourceKey).Scan(&afterSealed); err != nil || !bytes.Equal(sealed, afterSealed) {
		t.Fatal("相同秘密重复导入改变了本机执行版本")
	}
	a, err := NewAccounts(destination.store, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.RefreshProviderAccounts(context.Background(), destination.provider)
	if err != nil || len(result.Accounts) != 1 || result.Accounts[0].Balance.Amount == nil || *result.Accounts[0].Balance.Amount != 89 {
		t.Fatalf("跨机余额认证不可用: %v", err)
	}
	keys, err := destination.store.Keys(context.Background(), destination.provider)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, k := range keys {
		if k.ID == sourceKey {
			found = k.Name == "已认证主力" && k.Multiplier != nil && *k.Multiplier == 1.75
		}
	}
	if !found {
		t.Fatal("跨机 Key 查询认证未生效")
	}
}

// TestConfigurationMergeModes 验证匹配完整秘密时保留本机 ID，两种模式均追加独有资源并保留历史、概览和余额结果。
func TestConfigurationMergeModes(t *testing.T) {
	source, _, _ := transferFixture(t, "https://source.example")
	value := exportTransferFixture(t, source.store)
	for _, overwrite := range []bool{false, true} {
		t.Run(fmt.Sprint("overwrite=", overwrite), func(t *testing.T) {
			ctx := context.Background()
			destination := newModelFixture(t)
			s := destination.store
			a, err := NewAccounts(s, nil)
			if err != nil {
				t.Fatal(err)
			}
			key, err := s.CreateKey(ctx, destination.provider, keyDraft("本机主力", "sk-transfer-full-credential", 6))
			if err != nil {
				t.Fatal(err)
			}
			if err = s.SaveModels(ctx, destination.provider, ProviderModelsDraft{ModelIDs: []string{destination.ids[0], destination.ids[2]}}); err != nil {
				t.Fatal(err)
			}
			if err = s.SaveMappings(ctx, destination.provider, []ProviderModelMappingsDraft{{ModelID: destination.ids[0], Mappings: []ProviderModelMapping{{RequestName: "本机映射", Priority: 1}}}}); err != nil {
				t.Fatal(err)
			}
			if _, err = a.SaveProviderAccount(ctx, destination.provider, ProviderAccountDraft{Config: QueryConfig{Mode: "usage_api", APIKey: "local-account-auth"}, MonthlyQuota: 3}); err != nil {
				t.Fatal(err)
			}
			// 保留已保存远端结果，导入不能以来源缺失余额或上游已用值清空它。
			if _, err = s.db.Exec(`UPDATE provider_accounts SET document=json_set(document,'$.balance.amount',777,'$.balance.used',123,'$.balance.status.state','success')`); err != nil {
				t.Fatal(err)
			}
			r, err := records.New(s.db)
			if err != nil {
				t.Fatal(err)
			}
			_, finish, err := r.Begin(ctx, records.Input{ModelID: destination.ids[0], ProviderID: destination.provider, KeyID: key.ID, Protocol: "openai", Source: "verification"})
			if err != nil {
				t.Fatal(err)
			}
			if err = finish(nil); err != nil {
				t.Fatal(err)
			}
			// 单独的 Key 长期累计不依赖请求明细，验证已有数值也不会被导入覆盖。
			if _, err = s.db.Exec(`INSERT INTO request_key_totals(day,provider_id,model_id,protocol,key_id,key_name,attempts,tokens,cost,unknown_usage,unknown_cost) VALUES('2026-11-15',?,?,'openai',?,'本机主力',17,42,2.5,0,0)`, destination.provider, destination.ids[0], key.ID); err != nil {
				t.Fatal(err)
			}
			history := configurationHistorySnapshot(t, s)
			modelIDs := map[string]string{}
			for i, id := range source.ids {
				modelIDs[id] = destination.ids[i]
			}
			importTransferFixture(t, s, value, modelIDs, overwrite)
			if got := configurationHistorySnapshot(t, s); !reflect.DeepEqual(history, got) {
				for table := range history {
					if !reflect.DeepEqual(history[table], got[table]) {
						t.Fatalf("导入改动了历史表 %s", table)
					}
				}
			}
			actual := exportTransferFixture(t, s).Providers[0]
			if len(actual.Models) != 3 || len(actual.Keys) != 7 {
				t.Fatal("本机独有关联/Key 未保留或来源资源未追加")
			}
			var main ConfigurationKey
			for _, k := range actual.Keys {
				if k.ID == key.ID {
					main = k
				}
			}
			if overwrite {
				if actual.Provider.Enabled || main.Draft.Name != "来源主力" || main.Query == nil || actual.Account.MonthlyQuota != 27.5 {
					t.Fatal("覆盖模式未替换匹配配置或未保留 Key 本机身份")
				}
				assertModelPermissions(t, s, key.ID, false, destination.ids[0])
			} else if !actual.Provider.Enabled || main.Draft.Name != "本机主力" || main.Query == nil || !reflect.DeepEqual(main.Query, value.Providers[0].Keys[3].Query) || actual.Account.MonthlyQuota != 3 || !main.AllModels {
				t.Fatal("保留模式修改了匹配本机配置")
			}
			account, err := a.ProviderAccount(ctx, destination.provider)
			if err != nil || account.Balance.Amount == nil || *account.Balance.Amount != 777 || account.Balance.Used == nil || *account.Balance.Used != 123 {
				t.Fatal("导入清空了本机已保存余额结果")
			}
			before := exportTransferFixture(t, s)
			importTransferFixture(t, s, value, modelIDs, overwrite)
			if !reflect.DeepEqual(before, exportTransferFixture(t, s)) {
				t.Fatal("合并模式重复导入不幂等")
			}
		})
	}
}

// TestConfigurationMappingMerge 验证逐请求名合并、相同优先级、稳定顺序、空来源保留和已有无映射关联的补齐。
func TestConfigurationMappingMerge(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		for _, emptyLocal := range []bool{false, true} {
			t.Run(fmt.Sprintf("overwrite=%v/emptyLocal=%v", overwrite, emptyLocal), func(t *testing.T) {
				ctx := context.Background()
				f := newModelFixture(t)
				if err := f.store.SaveModels(ctx, f.provider, ProviderModelsDraft{ModelIDs: f.ids[:1]}); err != nil {
					t.Fatal(err)
				}
				if err := f.store.SetModelEnabled(ctx, f.provider, f.ids[0], false); err != nil {
					t.Fatal(err)
				}
				local := []ProviderModelMapping{{RequestName: "local-only", Priority: 1}, {RequestName: "shared", Priority: 8}}
				if !emptyLocal {
					if err := f.store.SaveMappings(ctx, f.provider, []ProviderModelMappingsDraft{{ModelID: f.ids[0], Mappings: local}}); err != nil {
						t.Fatal(err)
					}
				}
				incoming := []ProviderModelMapping{{RequestName: "shared", Priority: 2}, {RequestName: "source-new", Priority: 1}}
				value := exportTransferFixture(t, f.store)
				value.Providers[0].Keys = nil
				value.Providers[0].Models[0].Enabled = true
				value.Providers[0].Models[0].Mappings = incoming
				ids := map[string]string{f.ids[0]: f.ids[0]}
				importTransferFixture(t, f.store, value, ids, overwrite)
				want := []ProviderModelMapping{{RequestName: "local-only", Priority: 1}, {RequestName: "shared", Priority: 8}, {RequestName: "source-new", Priority: 1}}
				if overwrite {
					want[1].Priority = 2
				}
				if emptyLocal {
					want = incoming
				}
				actual := exportTransferFixture(t, f.store).Providers[0].Models[0]
				if actual.Enabled != overwrite || !reflect.DeepEqual(actual.Mappings, want) {
					t.Fatalf("关联启停或逐项映射合并错误: got=%+v want=%+v", actual, want)
				}
				importTransferFixture(t, f.store, value, ids, overwrite)
				if got := exportTransferFixture(t, f.store).Providers[0].Models[0]; !reflect.DeepEqual(actual, got) {
					t.Fatal("重复合并改变了映射顺序或配置")
				}
				value.Providers[0].Models[0].Mappings = nil
				importTransferFixture(t, f.store, value, ids, overwrite)
				if got := exportTransferFixture(t, f.store).Providers[0].Models[0]; !reflect.DeepEqual(actual, got) {
					t.Fatal("来源空映射删除了本机请求名")
				}
			})
		}
	}
}

// TestConfigurationPreserveKeyQuery 验证保留模式独立补齐查询认证、保留已有查询，且不改变匹配 Key 基本配置及权限。
func TestConfigurationPreserveKeyQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-auth-token" || r.URL.Query().Get("token") != "fixture-key-token" {
			http.Error(w, "authentication failed", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"name":"已认证主力","group":"生产","rate":1.75}`)
	}))
	defer server.Close()
	source, _, _ := transferFixture(t, server.URL)
	value := exportTransferFixture(t, source.store)
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existingQuery=%v", existing), func(t *testing.T) {
			ctx := context.Background()
			f := newModelFixture(t)
			a, err := NewAccounts(f.store, nil)
			if err != nil {
				t.Fatal(err)
			}
			key, err := f.store.CreateKey(ctx, f.provider, keyDraft("保留本机名称", "sk-transfer-full-credential", 6))
			if err != nil {
				t.Fatal(err)
			}
			if err = f.store.SaveModels(ctx, f.provider, ProviderModelsDraft{ModelIDs: f.ids[2:]}); err != nil {
				t.Fatal(err)
			}
			if err = f.store.SaveModelPermissions(ctx, key.ID, ProviderModelPermissions{ModelIDs: f.ids[2:]}); err != nil {
				t.Fatal(err)
			}
			var previousSecret []byte
			var previousRevision string
			if existing {
				query := ProviderKeyQueryDraft{Config: QueryConfig{Mode: "usage_api", BaseURL: "https://local.example", APIKey: "local-query-secret"}, NameSource: "manual", MultiplierSource: "manual"}
				if _, _, err = a.saveKeyLocal(ctx, f.provider, key.ID, keyDraft("保留本机名称", "", 6), query); err != nil {
					t.Fatal(err)
				}
				if err = f.store.db.QueryRow("SELECT secret,revision FROM provider_key_queries WHERE key_id=?", key.ID).Scan(&previousSecret, &previousRevision); err != nil {
					t.Fatal(err)
				}
			}
			before := exportTransferFixture(t, f.store).Providers[0].Keys[3]
			modelIDs := map[string]string{}
			for i, id := range source.ids {
				modelIDs[id] = f.ids[i]
			}
			importTransferFixture(t, f.store, value, modelIDs, false)
			actual := exportTransferFixture(t, f.store).Providers[0].Keys[3]
			if actual.ID != before.ID || !reflect.DeepEqual(actual.Draft, before.Draft) || actual.Enabled != before.Enabled {
				t.Fatal("补齐查询时改动了本机 Key 配置")
			}
			assertModelPermissions(t, f.store, key.ID, false, f.ids[2])
			if existing {
				var currentSecret []byte
				var revision string
				if err = f.store.db.QueryRow("SELECT secret,revision FROM provider_key_queries WHERE key_id=?", key.ID).Scan(&currentSecret, &revision); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before.Query, actual.Query) || !bytes.Equal(previousSecret, currentSecret) || previousRevision != revision {
					t.Fatal("保留模式覆盖或重写了已有查询配置")
				}
			} else {
				if !reflect.DeepEqual(actual.Query, value.Providers[0].Keys[3].Query) {
					t.Fatal("缺失查询未完整补齐来源认证与配置")
				}
				result, err := a.RefreshProviderKey(ctx, key.ID)
				if err != nil || result.NameStatus.State != "success" || result.MultiplierStatus.State != "success" {
					t.Fatalf("补齐的查询认证不可用: %v", err)
				}
			}
		})
	}
}

// configurationHistorySnapshot 读取真实历史表的完整行 JSON，确保导入没有重写统计、明细、正文或概览。
func configurationHistorySnapshot(t *testing.T, s *Store) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	for _, table := range []string{"request_records", "request_payloads", "request_totals", "request_key_totals", "request_overview"} {
		// 表名来自固定内部集合，json_object 参数由 SQLite 列元数据组成。
		rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			t.Fatal(err)
		}
		var columns []string
		for rows.Next() {
			var index, notnull, primary int
			var name, kind string
			var defaultValue any
			if err = rows.Scan(&index, &name, &kind, &notnull, &defaultValue, &primary); err != nil {
				t.Fatal(err)
			}
			columns = append(columns, "'"+name+"',"+name)
		}
		if err = finishConfigurationRows(rows, nil); err != nil {
			t.Fatal(err)
		}
		rows, err = s.db.Query("SELECT json_object(" + strings.Join(columns, ",") + ") FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			result[table] = append(result[table], raw)
		}
		if err = finishConfigurationRows(rows, nil); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

// TestConfigurationRollback 验证整包事务回滚、配置冲突与失钥保护，不在目的机器留下部分配置。
func TestConfigurationRollback(t *testing.T) {
	source, _, _ := transferFixture(t, "https://source.example")
	value := exportTransferFixture(t, source.store)
	destination := newModelFixture(t)
	s := destination.store
	modelIDs := map[string]string{}
	for i, id := range source.ids {
		modelIDs[id] = destination.ids[i]
	}
	before := exportTransferFixture(t, s)
	failure := errors.New("调度导入失败")
	err := s.ConfigurationTransaction(context.Background(), func(tx *sql.Tx) error {
		if _, err := s.ImportConfiguration(context.Background(), tx, value, modelIDs, true); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) || !reflect.DeepEqual(before, exportTransferFixture(t, s)) {
		t.Fatal("后续模块失败未回滚全部供应商配置")
	}
	// 供应商基本信息和模型关联写入后，账户写入失败也必须撤销前面步骤。
	if _, err = NewAccounts(s, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_transfer_account BEFORE INSERT ON provider_accounts BEGIN SELECT RAISE(ABORT,'fixture account failure'); END`); err != nil {
		t.Fatal(err)
	}
	err = s.ConfigurationTransaction(context.Background(), func(tx *sql.Tx) error {
		_, err := s.ImportConfiguration(context.Background(), tx, value, modelIDs, true)
		return err
	})
	if err == nil || !reflect.DeepEqual(before, exportTransferFixture(t, s)) {
		t.Fatal("供应商从属配置写入中途失败未完整回滚")
	}
	if _, err = s.CreateKey(context.Background(), destination.provider, keyDraft("已保存凭据", "sk-existing", 1)); err != nil {
		t.Fatal(err)
	}
	s.vault = &vault{store: &memoryKeys{}}
	called := false
	err = s.ConfigurationTransaction(context.Background(), func(tx *sql.Tx) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("丢失主密钥时不应创建新密钥或进入导入事务")
	}
}

// TestConfigurationIdentityConflicts 验证稳定身份、名称、完整秘密及目的模型映射冲突均明确拒绝且保留原配置。
func TestConfigurationIdentityConflicts(t *testing.T) {
	source, _, _ := transferFixture(t, "https://source.example")
	value := exportTransferFixture(t, source.store)
	for _, scenario := range []string{"供应商身份与名称", "Key 跨供应商身份", "Key 身份与秘密", "模型映射合并"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			f := newModelFixture(t)
			var v Configuration
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
			modelIDs := map[string]string{}
			for i, id := range source.ids {
				modelIDs[id] = f.ids[i]
			}
			switch scenario {
			case "供应商身份与名称":
				p, err := f.store.Create(ctx, validDraft("另一供应商"))
				if err != nil {
					t.Fatal(err)
				}
				v.Providers[0].Provider.ID = p.ID
			case "Key 跨供应商身份":
				p, err := f.store.Create(ctx, validDraft("另一供应商"))
				if err != nil {
					t.Fatal(err)
				}
				k, err := f.store.CreateKey(ctx, p.ID, keyDraft("其他 Key", "sk-other-owner", 1))
				if err != nil {
					t.Fatal(err)
				}
				v.Providers[0].Keys[3].ID = k.ID
			case "Key 身份与秘密":
				k, err := f.store.CreateKey(ctx, f.provider, keyDraft("同秘密 Key", "sk-transfer-full-credential", 1))
				if err != nil || k.ID == f.keys[0] {
					t.Fatal("建立冲突场景失败")
				}
				v.Providers[0].Keys[3].ID = f.keys[0]
			case "模型映射合并":
				modelIDs[source.ids[1]] = f.ids[0]
			}
			before := exportTransferFixture(t, f.store)
			err = f.store.ConfigurationTransaction(ctx, func(tx *sql.Tx) error {
				_, err := f.store.ImportConfiguration(ctx, tx, v, modelIDs, true)
				return err
			})
			if err == nil || !reflect.DeepEqual(before, exportTransferFixture(t, f.store)) {
				t.Fatal("身份冲突未拒绝或破坏了原有配置")
			}
		})
	}
}

// TestConfigurationValidation 验证整包重复身份、秘密、引用、配置边界及脱敏占位符会在写入前被拒绝。
func TestConfigurationValidation(t *testing.T) {
	source, _, _ := transferFixture(t, "https://source.example")
	value := exportTransferFixture(t, source.store)
	known := map[string]bool{}
	for _, id := range source.ids {
		known[id] = true
	}
	mutations := map[string]func(*Configuration){
		"供应商身份": func(v *Configuration) { v.Providers = append(v.Providers, v.Providers[0]) },
		"供应商名称": func(v *Configuration) {
			p := v.Providers[0]
			p.Provider.ID = "another"
			v.Providers = append(v.Providers, p)
		},
		"Key 身份": func(v *Configuration) { p := &v.Providers[0]; p.Keys = append(p.Keys, p.Keys[0]) },
		"完整密钥": func(v *Configuration) {
			k := v.Providers[0].Keys[3]
			k.ID = "another"
			v.Providers[0].Keys = append(v.Providers[0].Keys, k)
		},
		"模型引用": func(v *Configuration) { v.Providers[0].Models[0].ModelID = "missing" },
		"权限引用": func(v *Configuration) { v.Providers[0].Keys[3].ModelIDs = []string{source.ids[2]} },
		"重复权限": func(v *Configuration) { k := &v.Providers[0].Keys[3]; k.ModelIDs = append(k.ModelIDs, k.ModelIDs[0]) },
		"全部权限": func(v *Configuration) { v.Providers[0].Keys[3].AllModels = true },
		"重复映射": func(v *Configuration) {
			v.Providers[0].Models[0].Mappings = []ProviderModelMapping{{RequestName: "duplicate"}, {RequestName: "duplicate"}}
		},
		"请求策略": func(v *Configuration) { v.Providers[0].Provider.Request.Attempts = -1 },
		"月配额":  func(v *Configuration) { v.Providers[0].Account.MonthlyQuota = -1 },
		"手动倍率": func(v *Configuration) { v.Providers[0].Keys[0].Draft.Multiplier = nil },
		"密钥掩码": func(v *Configuration) { v.Providers[0].Keys[3].Draft.Secret = "***" },
		"账户掩码": func(v *Configuration) { v.Providers[0].Account.Config.Headers = "***" },
		"认证 URL 掩码": func(v *Configuration) {
			v.Providers[0].Keys[3].Query.Config.URL = "https://source.example/key?token=***"
		},
		"查询来源": func(v *Configuration) { v.Providers[0].Keys[3].Query.NameSource = "invalid" },
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var v Configuration
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
			mutate(&v)
			if err := ValidateConfiguration(v, known); err == nil {
				t.Fatal("无效来源配置未被拒绝")
			}
		})
	}
	if err := ValidateConfiguration(value, known); err != nil {
		t.Fatal(err)
	}
}
