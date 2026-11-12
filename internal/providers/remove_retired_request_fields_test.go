package providers

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/credentials"
)

// TestRemoveRetiredRequestFields 验证初始化清理历史开关和转移限制，明文/密文保留未知字段且重复迁移不重写。
func TestRemoveRetiredRequestFields(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		for _, flag := range []bool{false, true} {
			dir := t.TempDir()
			master := &memoryKeys{}
			s, closeDB := openStore(t, filepath.Join(dir, "retry.sqlite"), master)
			p, err := s.Create(context.Background(), validDraft("legacy"))
			if err != nil {
				t.Fatal(err)
			}
			if encrypted {
				secrets, err := credentials.New(dir, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err = s.protectDocuments(secrets); err != nil {
					t.Fatal(err)
				}
			}
			var doc map[string]json.RawMessage
			if err = s.decodeDocument([]byte(proxyDocument(t, s, "providers", p.ID)), &doc); err != nil {
				t.Fatal(err)
			}
			var request map[string]json.RawMessage
			if err = json.Unmarshal(doc["request"], &request); err != nil {
				t.Fatal(err)
			}
			request["transportFailover"], err = json.Marshal(flag)
			if err != nil {
				t.Fatal(err)
			}
			request["maxTransfers"] = json.RawMessage(`1`)
			request["maxTransferSeconds"] = json.RawMessage(`1`)
			request["attempts"] = json.RawMessage(`3`)
			request["future"] = json.RawMessage(`{"keep":true}`)
			doc["future"] = json.RawMessage(`"keep"`)
			doc["request"], err = json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			data, err := s.encodeDocument(doc)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec("UPDATE providers SET document=? WHERE id=?", string(data), p.ID); err != nil {
				t.Fatal(err)
			}
			upgraded, err := New(s.db, master, s.documents)
			if err != nil {
				t.Fatal(err)
			}
			raw := proxyDocument(t, s, "providers", p.ID)
			if err = upgraded.decodeDocument([]byte(raw), &doc); err != nil {
				t.Fatal(err)
			}
			request = map[string]json.RawMessage{}
			if err = json.Unmarshal(doc["request"], &request); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"transportFailover", "maxTransfers", "maxTransferSeconds"} {
				if _, ok := request[field]; ok {
					t.Fatalf("历史字段 %s 未清理", field)
				}
			}
			if string(request["attempts"]) != "3" {
				t.Fatal("同 Key 尝试次数被修改")
			}
			if string(request["future"]) != `{"keep":true}` || string(doc["future"]) != `"keep"` {
				t.Fatal("未知配置被修改")
			}
			if err = upgraded.removeRetiredRequestFields(context.Background()); err != nil || proxyDocument(t, s, "providers", p.ID) != raw {
				t.Fatal("重复迁移不幂等", err)
			}
			closeDB()
		}
	}
}

// TestRemoveRetiredRequestFieldsRollback 验证第二条更新失败时所有供应商已取消字段原样保留。
func TestRemoveRetiredRequestFieldsRollback(t *testing.T) {
	s, closeDB := openStore(t, filepath.Join(t.TempDir(), "rollback.sqlite"), &memoryKeys{})
	defer closeDB()
	ctx := context.Background()
	for _, name := range []string{"one", "two"} {
		if _, err := s.Create(ctx, validDraft(name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`UPDATE providers SET document=json_set(document,'$.request.transportFailover',json('false'),'$.request.maxTransfers',1,'$.request.maxTransferSeconds',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_retry_cleanup BEFORE UPDATE ON providers WHEN (SELECT count(*) FROM providers WHERE json_type(document,'$.request.transportFailover') IS NULL)>0 BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.removeRetiredRequestFields(ctx); err == nil {
		t.Fatal("迁移未报告失败")
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM providers WHERE json_type(document,'$.request.transportFailover')='false' AND json_extract(document,'$.request.maxTransfers')=1 AND json_extract(document,'$.request.maxTransferSeconds')=1`).Scan(&count); err != nil || count != 2 {
		t.Fatal("迁移未全部回滚", err)
	}
}
