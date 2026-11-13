import assert from "node:assert/strict";
import { test } from "node:test";
import { emptyMetrics, summarize, groupRows, timeBuckets, dateKey, heatDays, formatMetric, successRate, cacheRate, usagePartsKnown } from "./data.ts";

/** 固定样例分别保留逻辑请求和两次实际尝试，验证UI不会重复累计。 */
test("逻辑请求与Key尝试严格分开，零倍率费用可独立展示", () => {
  const metrics = { ...emptyMetrics(), requests: 1, detailsKnown: 1, success: 1, tokens: 120, context: 100, output: 20, cost: 0, standardCost: 0.0003 };
  const row = { time: Date.parse("2026-11-13T04:00:00Z"), day: "2026-11-13", providerId: "p", providerName: "供应商", modelId: "m", modelName: "模型", keyId: "", keyName: "", protocol: "chat", metrics };
  const rows = [row, { ...row, keyId: "k1", keyName: "Key1" }, { ...row, keyId: "k2", keyName: "Key2" }];
  assert.equal(groupRows(rows, "model")[0].total.requests, 1);
  assert.equal(groupRows(rows, "key").length, 2);
  assert.equal(summarize(rows.filter(row => !row.keyId)).tokens, 120);
  assert.equal(formatMetric(metrics, "cost"), "$0.00");
  assert.equal(formatMetric({ ...metrics, standardCost: 47.435, cost: 38.594 }, "standardCost"), "$47.44");
  assert.equal(formatMetric({ ...metrics, cost: 38.594 }, "cost"), "$38.59");
  assert.equal(formatMetric(metrics, "standardCost"), "$0.00");
  assert.equal(formatMetric({ ...metrics, unknownStandard: 1 }, "standardCost"), "$0.00");
  assert.equal(formatMetric({ ...metrics, unknownUsage: 1 }, "tokens"), "120");
  assert.equal(formatMetric({ ...metrics, cost: 0.0002, unknownCost: 1 }, "cost"), "$0.00");
});

/** 日期采用统计时区；已清理日统计不伪造零点小时峰值。 */
test("固定统计日期、小时桶及历史缺失保持正确", () => {
  const row = { time: Date.parse("2026-11-12T18:00:00Z"), day: "2026-11-13", providerId: "p", modelId: "m", keyId: "", metrics: { ...emptyMetrics(), requests: 2, detailsKnown: 2, tokens: 10 } };
  assert.equal(dateKey(row.time, "Asia/Shanghai"), "2026-11-13");
  const buckets = timeBuckets([row], row.day, row.day, "Asia/Shanghai");
  assert.equal(buckets.length, 24);
  assert.equal(buckets[2].total.requests, 2);
  row.metrics.detailsKnown = 0;
  assert.equal(timeBuckets([row], row.day, row.day, "Asia/Shanghai").length, 1);
  assert.equal(successRate(row.metrics), null);
  assert.equal(formatMetric({ ...row.metrics, unknownStandard: 2 }, "standardCost"), "—");
  const calendar = heatDays(2026, "2026-11-12", "2026-11-13", [row]);
  assert.equal(calendar.find(day => day.day === "2026-11-11").recorded, false);
  assert.equal(calendar.find(day => day.day === "2026-11-13").total.requests, 2);
  assert.equal(calendar.find(day => day.day === "2026-11-14").recorded, false);
});

/** 混有历史累计与未知请求时仍显示真实分项，不能把全部未知补成零。 */
test("部分已知分项与缓存命中保持可见", () => {
  const partial = { ...emptyMetrics(), requests: 100, detailsKnown: 2, unknownUsage: 90, tokens: 377310000, context: 100000000, cached: 20000000, output: 5000000 };
  assert.equal(usagePartsKnown(partial), true);
  assert.equal(cacheRate(partial), 20);
  assert.equal(partial.context - partial.cached, 80000000);
  const unknown = { ...emptyMetrics(), requests: 2, detailsKnown: 2, unknownUsage: 2 };
  assert.equal(usagePartsKnown(unknown), false);
  assert.equal(cacheRate(unknown), null);
  assert.equal(usagePartsKnown({ ...emptyMetrics(), requests: 100, tokens: 377310000 }), false);
  assert.equal(usagePartsKnown(emptyMetrics()), true);
  assert.equal(usagePartsKnown({ ...emptyMetrics(), requests: 1, detailsKnown: 1 }), true);
});
