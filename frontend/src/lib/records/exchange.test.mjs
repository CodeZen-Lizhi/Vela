import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import { test } from "node:test";

// Node 原生执行项目 TypeScript 时补全 Vite 支持的本地省略扩展名。
registerHooks({ resolve(specifier, context, nextResolve) {
  if (context.parentURL?.endsWith("/exchange.ts") && specifier === "./display") return nextResolve(new URL("./display.ts", context.parentURL).href, context);
  return nextResolve(specifier, context);
} });
const { conversationMessages, conversationCopy, formatSnapshot, jsonTokens, messageBlocks } = await import("./exchange.ts");

/** 多轮输入与正文复制共用顺序，代码、空行及多个用户消息完整保留。 */
test("多轮请求保留用户与助手顺序，复制不包含页面元信息", () => {
  const input = [{ role:"user", content:"第一问\n\n第二段" }, { role:"assistant", content:"历史回答" }, { role:"user", content:"```go\nreturn nil\n```" }];
  const rows = conversationMessages(JSON.stringify({model:"test",messages:input}), "request");
  assert.deepEqual(rows.map(row=>row.role), ["用户","助手","用户"]);
  assert.equal(conversationCopy(rows), "用户：\n第一问\n\n第二段\n\n助手：\n历史回答\n\n用户：\n```go\nreturn nil\n```");
  assert.deepEqual(messageBlocks(input[2].content), [{code:true,text:"return nil\n"}]);
});

/** Responses 终态重复包含全文时，不把增量和终态叠加。 */
test("Responses SSE 优先终态，截断末帧保留已收到的增量", () => {
  const frames = [{type:"response.output_text.delta",delta:"你"},{type:"response.output_text.delta",delta:"好\n"}];
  const events=frames.map(frame=>`data: ${JSON.stringify(frame)}\n\n`).join("");
  assert.deepEqual(conversationMessages(events+'data: {"broken',"response"),[{role:"助手",text:"你好\n"}]);
  const terminal={type:"response.completed",response:{output:[{role:"assistant",content:[{type:"output_text",text:"你好\n"}]}]}};
  assert.deepEqual(conversationMessages(events+`data: ${JSON.stringify(terminal)}\n\n`,"response"),[{role:"助手",text:"你好\n"}]);
});

/** Chat、Anthropic 和 Gemini 实际增量归并，而非每个 token 单独一行。 */
test("Chat、Anthropic、Gemini 流式文本按真实输出身份合并", () => {
  const stream=frames=>frames.map(frame=>`data: ${JSON.stringify(frame)}\n\n`).join("");
  assert.deepEqual(conversationMessages(stream([{choices:[{index:0,delta:{content:"a "}}]},{choices:[{index:0,delta:{content:"b"}}]}]),"response"),[{role:"助手",text:"a b"}]);
  assert.deepEqual(conversationMessages(stream([{type:"content_block_start",index:0,content_block:{type:"text",text:""}},{type:"content_block_delta",index:0,delta:{type:"text_delta",text:"答复"}}]),"response"),[{role:"助手",text:"答复"}]);
  assert.deepEqual(conversationMessages(stream([{candidates:[{index:0,content:{role:"model",parts:[{text:"a"}]}}]},{candidates:[{index:0,content:{role:"model",parts:[{text:"b"}]}}]}]),"response"),[{role:"助手",text:"ab"}]);
});

/** 工具、媒体和错误保留诊断信息，无法解析的对象由页面回退原始视图。 */
test("非纯文本内容不伪造成用户消息", () => {
  assert.equal(conversationMessages('{"error":{"message":"失败"}}',"response")[0].role,"错误");
  assert.equal(conversationMessages('{"input":[{"type":"input_image","image_url":"[MEDIA]"}]}',"request")[0].role,"媒体信息");
  assert.equal(conversationMessages('{"output":[{"type":"function_call","name":"search","arguments":"{}"}]}',"response")[0].role,"工具");
  assert.deepEqual(conversationMessages('{"embedding":[1,2]}',"response"),[]);
});

/** 高亮后文本与复制源逐字一致，含 HTML 与转义字符也不丢失。 */
test("JSON 缩进、高亮与复制保持可解析且无损", () => {
  const headers={Authorization:["[REDACTED]"],"X-Long":["<script>\n"+'x'.repeat(3000)]};
  const formatted=formatSnapshot(JSON.stringify(headers));
  assert.ok(formatted.includes('\n  "Authorization": [\n'));
  assert.deepEqual(JSON.parse(formatted),headers);
  assert.equal(jsonTokens(formatted).map(token=>token.text).join(""),formatted);
  const event='event: response.completed\ndata: {"ok":true}\n\ndata: [DONE]\n\n';
  assert.ok(formatSnapshot(event).includes('data: {\n  "ok": true\n}'));
  assert.ok(formatSnapshot(": heartbeat\n"+event).includes('data: {\n  "ok": true\n}'));
  assert.equal(formatSnapshot('not-json'), 'not-json');
});

/** Responses 指令独立于用户消息，消息内多个文本块合并但不合并相邻用户轮次。 */
test("系统指令与多块消息保留正确角色和消息边界", () => {
  const rows=conversationMessages(JSON.stringify({instructions:"遵循要求",input:[{role:"user",content:[{type:"input_text",text:"第一段"},{type:"input_text",text:"第二段"}]},{role:"user",content:"另一条用户消息"}]}),"request");
  assert.deepEqual(rows,[{role:"系统",text:"遵循要求"},{role:"用户",text:"第一段\n\n第二段"},{role:"用户",text:"另一条用户消息"}]);
});

/** 流失败时可能只有终态错误，不能用它覆盖已经收到的回复片段。 */
test("流式失败同时保留已输出内容和错误", () => {
  const frames=[{type:"response.output_text.delta",delta:"已输出片段"},{type:"response.failed",response:{error:{message:"连接中断"},output:[]}}];
  const rows=conversationMessages(frames.map(frame=>`data: ${JSON.stringify(frame)}\n\n`).join(""),"response");
  assert.deepEqual(rows,[{role:"助手",text:"已输出片段"},{role:"错误",text:'{\n  "message": "连接中断"\n}'}]);
});
