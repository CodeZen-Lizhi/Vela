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

/** 官方优先排序同时继续支持上下文中的模型类型筛选。 */
test("官方优先排序并保留模型类型过滤", () => {
  const chat = entry({ providerId: "official-chat", name: "Shared", official: true });
  const vector = entry({ providerId: "official-vector", name: "Shared", kind: "vector", official: true });
  const thirdParty = entry({ providerId: "third-party", name: "Shared" });
  const result = searchCatalogEntries([chat, thirdParty, vector], "shared", "chat");
  assert.deepEqual(
    result.map((item) => item.providerId),
    ["official-chat", "third-party"],
  );
});

/** 真实搜索入口按数值排列版本，同名以身份稳定排序，不修改来源数组。 */
test("目录按名称自然降序且搜索筛选后保持相同规则", () => {
  const entries = [
    entry({ name: "GPT 5.10", modelId: "gpt-5.10", official: true }),
    entry({ name: "GPT 5.2", modelId: "gpt-5.2", official: true }),
    entry({ name: "gpt 5.9", modelId: "gpt-5.9", official: true }),
    entry({ name: "GPT 5.2", modelId: "gpt-5.2-2", official: true }),
    entry({ name: "GPT 5.1", modelId: "third-party" }),
    entry({ name: "GPT 6.0", modelId: "gpt-6.0", official: true }),
    entry({ name: "GPT 6.1", modelId: "gpt-6.1", official: true }),
  ];
  const original = [...entries];
  for (const query of ["", "gpt"]) {
    assert.deepEqual(
      searchCatalogEntries(entries, query, "chat").map((item) => item.modelId),
      ["gpt-6.1", "gpt-6.0", "gpt-5.10", "gpt-5.9", "gpt-5.2-2", "gpt-5.2", "third-party"],
    );
  }
  assert.deepEqual(entries, original);
});
