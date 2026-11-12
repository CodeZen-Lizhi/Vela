package providers

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

// TestKeysPriorityOrder 验证列表按优先级升序、同级保持创建顺序，启停、编辑与重读后仍遵循该规则。
func TestKeysPriorityOrder(t *testing.T) {
	ctx := context.Background()
	s, closeDB := openStore(t, filepath.Join(t.TempDir(), "vela.sqlite"), &memoryKeys{})
	defer closeDB()
	provider, err := s.Create(ctx, validDraft("排序验收"))
	if err != nil {
		t.Fatal(err)
	}
	var created []ProviderKey
	for _, priority := range []int{5, 4, 3, 2, 1, 2} {
		key, err := s.CreateKey(ctx, provider.ID, keyDraft("排序 Key", "", priority))
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, key)
		if priority != 5 {
			if err := s.SetKeyEnabled(ctx, key.ID, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	check := func(want []string) {
		t.Helper()
		list, err := s.Keys(ctx, provider.ID)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, key := range list {
			got = append(got, key.ID)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Key 顺序 = %v，应为 %v", got, want)
		}
	}
	want := []string{created[4].ID, created[3].ID, created[5].ID, created[2].ID, created[1].ID, created[0].ID}
	check(want)
	if err := s.SetKeyEnabled(ctx, created[3].ID, true); err != nil {
		t.Fatal(err)
	}
	check(want)
	if _, err := s.UpdateKey(ctx, created[0].ID, keyDraft("修改优先级", "", 0)); err != nil {
		t.Fatal(err)
	}
	check([]string{created[0].ID, created[4].ID, created[3].ID, created[5].ID, created[2].ID, created[1].ID})
}
