import assert from "node:assert/strict";
import { test } from "node:test";
import { formatUSD } from "./currency.js";

/** 所有美元入口共用两位小数与标准货币位置，舍入不修改传入的原值。 */
test("美元两位小数、千位分隔、负数与未知值保持正确", () => {
  const amount = 47.435;
  assert.equal(formatUSD(amount), "$47.44");
  assert.equal(amount, 47.435);
  assert.equal(formatUSD(1234.5), "$1,234.50");
  assert.equal(formatUSD(0.0003), "$0.00");
  assert.equal(formatUSD(0), "$0.00");
  assert.equal(formatUSD(-7.125), "-$7.13");
  assert.equal(formatUSD(null), "—");
  assert.equal(formatUSD(undefined), "—");
});
