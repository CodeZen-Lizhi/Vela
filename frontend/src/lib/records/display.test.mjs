import assert from "node:assert/strict";
import { test } from "node:test";
import { emptyQuery, matches, transports } from "./display.ts";

/** WebSocket 使用独立客户端传输筛选；旧记录继续按原 stream 标记兼容。 */
test("WebSocket 不冒充 HTTP 流式，旧记录传输仍兼容", () => {
  const query = emptyQuery();
  query.transports = ["websocket"];
  assert.equal(matches({ stream: true, transport: "websocket" }, query), true);
  assert.equal(matches({ stream: true }, query), false);
  query.transports = ["stream"];
  assert.equal(matches({ stream: true, transport: "websocket" }, query), false);
  assert.equal(matches({ stream: true }, query), true);
  assert.ok(transports.some((value) => value.value === "websocket" && value.label === "WebSocket"));
});

/** 仅列表使用六位美元格式，小额费用不丢失精度，未知与免费区分。 */
test("使用记录金额固定六位小数，其他页面仍使用两位", async () => {
  const { recordMoney, money } = await import("./display.ts");
  assert.equal(recordMoney(350.87), "$350.870000");
  assert.equal(recordMoney(349.03), "$349.030000");
  assert.equal(recordMoney(0.0000014), "$0.000001");
  assert.equal(recordMoney(0), "$0.000000");
  assert.equal(recordMoney(null), "—");
  assert.equal(recordMoney(undefined), "—");
  assert.equal(money(350.87), "$350.87");
});

/** 本地冷却旧摘要没有供应商时，展示已知阻断身份，不展示未参与准入的候选。 */
test("本地拦截显示阻断供应商及 Key，旧记录摘要缺失仍可读", async () => {
  const { recordTarget } = await import("./display.ts");
  const row={state:"local",providerName:"",keyName:"",candidates:[
    {providerName:"未使用供应商",keyName:"未使用 Key",reason:""},
    {providerName:"Vela",keyName:"Key A",reason:"冷却"},
    {providerName:"Vela",keyName:"Key B",reason:"熔断"},
    {providerName:"Magpie",keyName:"Key C",reason:"冷却"},
  ]};
  assert.deepEqual(recordTarget(row),{providerName:"Vela、Magpie",keyName:"Key A、Key B、Key C"});
  assert.deepEqual(recordTarget({...row,state:"success",providerName:"执行供应商",keyName:"执行 Key"}),{providerName:"执行供应商",keyName:"执行 Key"});
  assert.deepEqual(recordTarget({state:"local",candidates:[]}),{providerName:"—",keyName:"—"});
});
