package models

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"testing"
)

// TestPriceJSONPresence 验证所有价格入口拒绝遗漏和 null，并允许明确零价及正常金额。
func TestPriceJSONPresence(t *testing.T) {
	templates := map[string]string{
		"base":  `{"base":[{"key":"input","unit":"million_tokens"%s}]}`,
		"tier":  `{"tiers":[{"above":200000,"rates":[{"key":"input","unit":"million_tokens"%s}]}]}`,
		"mode":  `{"modes":[{"name":"fast","rates":[{"key":"input","unit":"million_tokens"%s}]}]}`,
		"image": `{"images":[{"size":"1024x1024"%s}]}`,
		"video": `{"videos":[{"resolution":"1080p"%s}]}`,
	}
	for name, template := range templates {
		for _, field := range []string{"", `,"price":null`, `,"price":0`, `,"price":1.25`} {
			t.Run(name+field, func(t *testing.T) {
				var pricing Pricing
				err := json.Unmarshal([]byte(fmt.Sprintf(template, field)), &pricing)
				valid := field != "" && field != `,"price":null`
				var ipcPricing Pricing
				ipcErr := jsonv2.Unmarshal([]byte(fmt.Sprintf(template, field)), &ipcPricing)
				if (ipcErr == nil) != valid {
					t.Fatalf("MyGo JSON v2 解析错误=%v，预期有效=%v", ipcErr, valid)
				}
				if (err == nil) != valid {
					t.Fatalf("解析错误=%v，预期有效=%v", err, valid)
				}
				if valid {
					if err = validatePricing(pricing); err != nil {
						t.Fatal(err)
					}
					data, err := json.Marshal(pricing)
					if err != nil {
						t.Fatal(err)
					}
					var copy Pricing
					if err = json.Unmarshal(data, &copy); err != nil {
						t.Fatal(err)
					}
					if !sameGroup(pricing, copy, "base") || !sameGroup(pricing, copy, "images") || !sameGroup(pricing, copy, "videos") {
						t.Fatal("往返后单价改变")
					}
				}
			})
		}
	}
}
