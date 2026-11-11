import assert from "node:assert/strict";
import { test } from "node:test";
import { cacheMultiplier, updateRate } from "./rate-edit.ts";

/** 调用真实换算实现，覆盖修改缓存倍率、输入价联动及零价回退。 */
test("缓存倍率跟随同档输入价，零输入价保留独立缓存单价", () => {
  const input = { key: "input", unit: "million_tokens", price: 8 };
  const write = { key: "cache_write", unit: "million_tokens", price: 10 };
  const read = { key: "cache_read", unit: "million_tokens", price: 0.2 };
  const rates = [input, write, read];
  assert.equal(cacheMultiplier(write, rates), 1.25);
  updateRate(rates, input, 16);
  assert.equal(write.price, 20);
  assert.equal(read.price, 0.4);
  updateRate(rates, write, 1.5);
  assert.equal(write.price, 24);
  updateRate(rates, input, 0);
  assert.equal(cacheMultiplier(write, rates), null);
  assert.equal(write.price, 24);
  updateRate(rates, write, 7);
  assert.equal(write.price, 7);
  assert.equal(cacheMultiplier(write, [write]), null);
});

/** 每档只更新自身缓存，来源单价和其他计费单位不受影响。 */
test("阶梯倍率按本档换算并保留不同单位的价格", () => {
  const input = { key: "input", unit: "million_tokens", price: 16 };
  const cache = { key: "cache_read", unit: "million_tokens", price: 0.4 };
  const image = { key: "image_output", unit: "image", price: 0.08 };
  const rates = [input, cache, image];
  updateRate(rates, cache, 0.05);
  assert.equal(cache.price, 0.8);
  updateRate(rates, input, 32);
  assert.equal(cache.price, 1.6);
  assert.equal(image.price, 0.08);
  assert.equal(cacheMultiplier(image, rates), null);
});
