import type { ProviderKeyQueryDraft, QueryConfig } from "$lib/generated/mygo";

/** 创建空白查询连接；地址可由供应商预填，秘密字段只接受用户新输入。 */
export function defaultQueryConfig(baseURL = ""): QueryConfig {
  return { mode: "", baseUrl: baseURL, refreshToken: "", accessToken: "", apiKey: "", userId: "", quotaDivisor: 500000, method: "GET", url: "", headers: "", body: "", balancePath: "", currency: "USD" };
}

/** 新 Key 默认复用供应商连接，名称与倍率均由手动维护；不发起任何查询。 */
export function defaultKeyQuery(baseURL = ""): ProviderKeyQueryDraft {
  return { reuseProvider: true, config: defaultQueryConfig(baseURL), nameSource: "manual", multiplierSource: "manual", namePath: "", groupPath: "", multiplierPath: "", keyPath: "" };
}

/** 保存前清除读取到的秘密掩码，空值交给后端按原方式保留已有秘密。 */
export function queryConfigDraft(config: QueryConfig): QueryConfig {
  const draft = { ...config };
  for (const key of ["refreshToken", "accessToken", "apiKey", "headers", "body"] as const) {
    if (draft[key] === "***") draft[key] = "";
  }
  return draft;
}
