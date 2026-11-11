import assert from "node:assert/strict";
import { test } from "node:test";
import { modelInitial } from "./display.ts";

/** 在线模型沿用来源首字母，手动模型使用显示名称首字母且不再显示“手”。 */
test("模型头像首字母区分在线来源与手动模型", () => {
  assert.equal(modelInitial("GPT-6.1 Sol", "OpenAI"), "O");
  assert.equal(modelInitial("DeepSeek V4.1 Flash", "手动创建", true), "D");
  assert.equal(modelInitial("", "手动创建", true), "M");
});
