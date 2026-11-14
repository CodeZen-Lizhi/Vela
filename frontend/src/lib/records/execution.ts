import type { Attempt, Candidate, RecordsRecord } from "$lib/generated/mygo";

/** 路径条目保留实际尝试索引；本地跳过的索引为空，不能跳转到其他上游响应。 */
export interface PathEntry {
  /** id 是刷新时稳定的节点身份；item 是真实尝试或准入快照。 */
  id: string;
  item: Attempt | Candidate;
  /** index 仅指向 record.attempts；null 表示本次未出站。 */
  index: number | null;
  /** 状态依据完整执行错误和请求终态，不能仅凭 HTTP 200 判成功。 */
  state: "success" | "failed" | "pending" | "local";
}

/** 与 Aether 一致，优先展示真实调用；纯本地保护时展示被阻断的 Key，不展开未执行的排序候选。 */
export function pathEntries(record: RecordsRecord): PathEntry[] {
  if (record.attempts.length) return record.attempts.map((item, index) => ({
    id: `attempt-${item.number}`, item, index,
    state: item.error ? "failed" : record.state === "active" && index === record.attempts.length - 1 && !item.endedAt && !item.durationMillis ? "pending"
      : (item.status >= 200 && item.status < 400 || item.status === 101 && record.state === "success") ? "success" : "failed",
  }));
  return (record.candidates ?? []).filter(item => item.reason).map((item, index) => ({
    id: `local-${item.providerId}-${item.keyId}-${index}`, item, index: null, state: "local",
  }));
}

/** 错误优先使用执行原因，再读取真实脱敏响应；超期正文不从其他请求补入。 */
export function entryError(entry: PathEntry): string {
  if (entry.index == null) return (entry.item as Candidate).reason;
  const item = entry.item as Attempt;
  if (item.error) return item.error;
  if (entry.state !== "failed" || !item.payload?.responseBody) return "";
  try {
    const body = JSON.parse(item.payload.responseBody);
    if (typeof body?.error?.message === "string") return body.error.message;
    if (typeof body?.error === "string") return body.error;
    if (typeof body?.message === "string") return body.message;
  } catch { /* 非 JSON 错误保留原始文本。 */ }
  return item.payload.responseBody;
}

/** 冷却按真实 Unix 毫秒截止时间逐秒显示；到期只说明可重试/探测，不改写请求或健康终态。 */
export function protectionRemaining(until: number, now: number): string {
  const seconds = Math.ceil((until - now) / 1000);
  if (seconds <= 0) return "已到期，等待实际请求重新判定";
  const hours = Math.floor(seconds / 3600);
  return `${hours ? `${hours}小时` : ""}${Math.floor(seconds % 3600 / 60)}分${seconds % 60}秒`;
}
