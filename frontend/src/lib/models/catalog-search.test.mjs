import assert from "node:assert/strict";
import { test } from "node:test";
import { searchCatalogEntries } from "./catalog-search.ts";

/** 构造搜索测试需要的目录字段，避免依赖生成绑定的完整运行时对象。 */
function entry(overrides) {
  return {
    providerId: "",
    providerName: "",
    modelId: "",
    canonicalId: "",
    name: "",
    family: "",
    kind: "chat",
    context: 0,
    capabilities: [],
    official: false,
    pricing: { base: [], tiers: [], modes: [], schedules: [], baseline: "", images: [], videos: [] },
    problem: "",
    ...overrides,
  };
}

/** 粘贴带连字符的真实模型 ID 时，官方展示名称仍可匹配并排在第三方变体之前。 */
test("模型 ID 分隔符归一化后优先返回官方来源", () => {
  const reseller = entry({
    providerId: "reseller",
    providerName: "LLM Gateway",
    modelId: "deepseek-v4.1-flash",
    name: "DeepSeek V4.1 Flash 1017",
  });
  const official = entry({
    providerId: "deepseek",
    providerName: "DeepSeek",
    modelId: "deepseek-flash",
    name: "DeepSeek V4.1 Flash 1017",
    family: "deepseek-v4.1",
    official: true,
  });
  const result = searchCatalogEntries([reseller, official], "deepseek-v4.1-flash", "all");
  assert.deepEqual(
    result.map((item) => item.providerId),
    ["deepseek", "reseller"],
  );
});

/** 官方分组内保留来源顺序，同时继续支持上下文中的模型类型筛选。 */
test("官方优先排序稳定并保留模型类型过滤", () => {
  const chat = entry({ providerId: "official-chat", name: "Shared", official: true });
  const vector = entry({ providerId: "official-vector", name: "Shared", kind: "vector", official: true });
  const thirdParty = entry({ providerId: "third-party", name: "Shared" });
  const result = searchCatalogEntries([chat, thirdParty, vector], "shared", "chat");
  assert.deepEqual(
    result.map((item) => item.providerId),
    ["official-chat", "third-party"],
  );
});
