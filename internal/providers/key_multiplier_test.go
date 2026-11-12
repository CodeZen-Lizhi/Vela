package providers

import (
	"context"
	"math"
	"reflect"
	"testing"
)

// TestSetKeyMultiplier 验证真实落库、零值和上限、无关配置与凭据保留，以及自动来源拒绝写入。
func TestSetKeyMultiplier(t *testing.T) {
	a, p := combinedFixture(t, "https://api.example.com")
	ctx := context.Background()
	k, err := a.store.CreateKey(ctx, p.ID, keyDraft("manual", "fixture-secret", 3))
	if err != nil {
		t.Fatal(err)
	}
	q, err := a.SaveProviderKeyQuery(ctx, k.ID, ProviderKeyQueryDraft{ReuseProvider: true, NameSource: "manual", MultiplierSource: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	before := keySaveSnapshot(t, a, k.ID)
	for _, value := range []float64{0, 0.75, 100} {
		if err := a.store.SetKeyMultiplier(ctx, k.ID, value); err != nil {
			t.Fatal(err)
		}
		keys, err := a.store.Keys(ctx, p.ID)
		if err != nil || len(keys) != 1 || keys[0].Multiplier == nil || *keys[0].Multiplier != value {
			t.Fatalf("倍率未保存: %+v %v", keys, err)
		}
		want := k
		want.Multiplier = keys[0].Multiplier
		if !reflect.DeepEqual(keys[0], want) {
			t.Fatal("无关 Key 配置发生改变")
		}
	}
	after := keySaveSnapshot(t, a, k.ID)
	if !reflect.DeepEqual(before[1:], after[1:]) {
		t.Fatal("凭据或查询连接发生改变")
	}
	for _, invalid := range []float64{-1, 101, math.NaN(), math.Inf(1)} {
		if err := a.store.SetKeyMultiplier(ctx, k.ID, invalid); err == nil {
			t.Fatal("接受非法倍率", invalid)
		}
	}
	if _, err = a.store.db.Exec("UPDATE provider_key_queries SET document=json_set(document,'$.multiplierSource','upstream') WHERE key_id=?", q.KeyID); err != nil {
		t.Fatal(err)
	}
	blocked := keySaveSnapshot(t, a, k.ID)
	if err := a.store.SetKeyMultiplier(ctx, k.ID, 1); err == nil {
		t.Fatal("自动来源接受手动写入")
	}
	if !reflect.DeepEqual(blocked, keySaveSnapshot(t, a, k.ID)) {
		t.Fatal("拒绝写入后配置改变")
	}
}
