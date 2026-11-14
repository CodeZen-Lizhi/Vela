package providers

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/vela/internal/credentials"
)

// TestRemoveProviderNotesRestart 通过真实初始化迁移验证明文和密文旧数据清理，保留未知字段及 Key 备注。
func TestRemoveProviderNotesRestart(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(map[bool]string{false: "明文", true: "密文"}[encrypted], func(t *testing.T) {
			dir := t.TempDir()
			master := &memoryKeys{}
			s, closeDB := openStore(t, filepath.Join(dir, "vela.sqlite"), master)
			defer closeDB()
			ctx := context.Background()
			p, err := s.Create(ctx, validDraft("旧供应商"))
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
			var original string
			if err = s.db.QueryRow("SELECT document FROM providers WHERE id=?", p.ID).Scan(&original); err != nil {
				t.Fatal(err)
			}
			var doc map[string]json.RawMessage
			if err = s.decodeDocument([]byte(original), &doc); err != nil {
				t.Fatal(err)
			}
			doc["note"] = json.RawMessage(`"应清除的旧备注"`)
			doc["future"] = json.RawMessage(`{"keep":true}`)
			data, err := s.encodeDocument(doc)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec("UPDATE providers SET document=? WHERE id=?", string(data), p.ID); err != nil {
				t.Fatal(err)
			}
			// Key 文档不属于此次迁移，连同备注一起逐字节保留。
			keyData, err := s.encodeDocument(map[string]any{"name": "Key", "note": "保留Key备注", "proxy": map[string]string{"mode": "global"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec("INSERT INTO provider_keys(id,provider_id,enabled,all_models,document) VALUES('fixture-key',?,1,1,?)", p.ID, string(keyData)); err != nil {
				t.Fatal(err)
			}
			var upgraded *Store
			if encrypted {
				upgraded, err = New(s.db, master, s.documents)
			} else {
				upgraded, err = New(s.db, master)
			}
			if err != nil {
				t.Fatal(err)
			}
			var raw, keyRaw string
			if err = s.db.QueryRow("SELECT document FROM providers WHERE id=?", p.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var migrated map[string]json.RawMessage
			if err = upgraded.decodeDocument([]byte(raw), &migrated); err != nil {
				t.Fatal(err)
			}
			if _, exists := migrated["note"]; exists || string(migrated["future"]) != `{"keep":true}` || string(migrated["url"]) != string(doc["url"]) {
				t.Fatal("迁移未清理备注或修改了其他配置")
			}
			if encrypted && !strings.HasPrefix(raw, "vela-enc-v1:") {
				t.Fatal("迁移后文档失去加密")
			}
			if err = s.db.QueryRow("SELECT document FROM provider_keys WHERE id='fixture-key'").Scan(&keyRaw); err != nil || keyRaw != string(keyData) {
				t.Fatal("迁移修改了Key备注", err)
			}
			if err = upgraded.removeProviderNotes(ctx); err != nil {
				t.Fatal(err)
			}
			var repeated string
			if err = s.db.QueryRow("SELECT document FROM providers WHERE id=?", p.ID).Scan(&repeated); err != nil || repeated != raw {
				t.Fatal("重复迁移不应重写文档", err)
			}
		})
	}
}

// TestRemoveProviderNotesRollback 验证任意一条更新失败时不会清除其他供应商的备注。
func TestRemoveProviderNotesRollback(t *testing.T) {
	s, closeDB := openStore(t, filepath.Join(t.TempDir(), "vela.sqlite"), &memoryKeys{})
	defer closeDB()
	for _, name := range []string{"正常", "拒绝"} {
		p, err := s.Create(context.Background(), validDraft(name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.Exec("UPDATE providers SET document=json_set(document,'$.note','旧备注') WHERE id=?", p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("CREATE TRIGGER reject_note_migration BEFORE UPDATE ON providers WHEN OLD.name='拒绝' BEGIN SELECT RAISE(ABORT,'fixture rejection'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.removeProviderNotes(context.Background()); err == nil {
		t.Fatal("迁移应失败")
	}
	var remaining int
	if err := s.db.QueryRow("SELECT count(*) FROM providers WHERE json_extract(document,'$.note')='旧备注'").Scan(&remaining); err != nil || remaining != 2 {
		t.Fatal("迁移失败未整体回滚", err)
	}
}
