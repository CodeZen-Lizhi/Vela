import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import { test } from "node:test";

// Node 原生执行时补全 Vite 使用的本地 TypeScript 扩展名。
registerHooks({ resolve(specifier, context, nextResolve) {
  if (context.parentURL?.match(/\/(execution|exchange)\.ts$/) && ["./exchange", "./display"].includes(specifier)) return nextResolve(new URL(`${specifier}.ts`, context.parentURL).href, context);
  return nextResolve(specifier, context);
} });
const { attemptError, entryError, pathEntries, protectionRemaining } = await import("./execution.ts");

/** 协议终态错误不能被真实 HTTP 200 掩盖；重试索引保持与正文选择一致。 */
test("真实尝试保留顺序，HTTP 200 执行错误仍为失败", () => {
  const record = { state: "success", candidates: [{ reason: "未用候选" }], attempts: [
    { number: 1, status: 200, error: "流式响应中断" },
    { number: 2, status: 200, error: "" },
  ] };
  const entries = pathEntries(record);
  assert.deepEqual(entries.map(e => [e.index, e.state]), [[0, "failed"], [1, "success"]]);
  assert.equal(entryError(entries[0]), "流式响应中断");
  assert.equal(pathEntries({ ...record, state: "active" })[1].state, "pending");
});

/** 只有零实际调用的请求展示本地拦截，不把排序候选或本地节点绑定为上游正文。 */
test("本地熔断与冷却节点不创建上游尝试", () => {
  const entries = pathEntries({ state: "local", attempts: [], candidates: [
    { keyId: "unused", reason: "" },
    { keyId: "circuit", reason: "熔断中", circuitOpen: true, nextProbe: 6000 },
    { keyId: "cooldown", reason: "冷却中", cooldownUntil: 5000 },
  ] });
  assert.deepEqual(entries.map(e => [e.item.keyId, e.index, e.state]), [["circuit", null, "local"], ["cooldown", null, "local"]]);
  assert.equal(entryError(entries[1]), "冷却中");
});

/** 倒计时以截止时刻计算，跨小时及到期保留真实语义。 */
test("倒计时逐秒减少，到期不宣称健康恢复", () => {
  assert.equal(protectionRemaining(3661000, 0), "1小时1分1秒");
  assert.equal(protectionRemaining(3661000, 1000), "1小时1分0秒");
  assert.equal(protectionRemaining(1000, 1000), "已到期，等待实际请求重新判定");
});

/** 上游原始错误在可读取且缺少执行说明时仍可展示。 */
test("错误正文解析保留错误原文", () => {
  assert.equal(entryError({ index: 0, state: "failed", item: { error: "", payload: { responseBody: '{"error":{"message":"quota exhausted"}}' } } }), "quota exhausted");
});

/** 原生 WebSocket 的 101 是实际握手状态，完整成功后不能按普通 HTTP 失败处理。 */
test("WebSocket 101 完整成功与正在输出分别展示", () => {
  const record={state:"success",attempts:[{number:1,status:101,error:"",durationMillis:100,endedAt:1000}]};
  assert.equal(pathEntries(record)[0].state,"success");
  assert.equal(pathEntries({...record,state:"active",attempts:[{number:1,status:101,error:"",durationMillis:0,endedAt:0}]})[0].state,"pending");
});

/** HTTP 错误详情和代码不能被通用执行说明遮住，正文到期后不旁路展示。 */
test("失败节点合并执行原因与上游错误，过期正文不可见", () => {
  const item = {number:1, status:429, error:"上游返回状态 429", payload:{bodyState:"available", responseBody:'{"error":{"code":"rate_limit_exceeded","message":"余额不足，请充值"}}'}};
  const [entry] = pathEntries({state:"failed", attempts:[item]});
  assert.equal(entryError(entry), "上游返回状态 429：rate_limit_exceeded：余额不足，请充值");
  assert.equal(attemptError({...item, payload:{...item.payload, bodyState:"expired"}}), item.error);
});

/** 无响应保留网络故障，SSE 终态提取真实错误且不把正常回答当成错误。 */
test("网络故障和流式错误使用各自事实", () => {
  assert.equal(attemptError({error:"首次响应超时（30 秒），未收到上游 HTTP 响应头", status:0, payload:{responseBody:""}}), "首次响应超时（30 秒），未收到上游 HTTP 响应头");
  const item = {status:200, error:"上游流式响应失败", payload:{responseBody:'data: {"type":"response.failed","response":{"error":{"code":"server_error","message":"模型暂不可用"}}}\n\n'}};
  assert.match(attemptError(item), /模型暂不可用/);
  assert.equal(attemptError({status:200,error:"",payload:{responseBody:'{"choices":[{"message":{"content":"正常回答"}}]}'}}), "");
});
