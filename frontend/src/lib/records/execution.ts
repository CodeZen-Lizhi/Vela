import type { Attempt, Candidate, RecordsRecord } from "$lib/generated/mygo";
import { conversationMessages } from "./exchange";

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

/** 失败节点同时显示执行原因和真实上游错误；本地阻断保留准入原因。 */
export function entryError(entry: PathEntry): string {
  if (entry.index == null) return (entry.item as Candidate).reason;
  const item = entry.item as Attempt;
  return entry.state === "failed" ? attemptError(item) : "";
}

/** 合并单次执行错误与已脱敏 JSON/SSE 错误；正文超期时仅使用保留的执行诊断。 */
export function attemptError(item: Attempt | undefined): string {
  if (!item) return "";
  const payload = item.payload;
  if (!payload?.responseBody || payload.bodyState === "expired") return item.error;
  let detail = "";
  try {
    const body = JSON.parse(payload.responseBody);
    if (typeof body?.error?.message === "string") detail = body.error.message;
    else if (typeof body?.error === "string") detail = body.error;
    else if (typeof body?.message === "string") detail = body.message;
    const code = body?.error?.code ?? body?.error?.type;
    if (typeof code === "string" && code) detail = detail ? `${code}：${detail}` : code;
  } catch { /* 非 JSON 错误保留原始文本。 */ }
  if (!detail) detail = conversationMessages(payload.responseBody, "response").filter(row => row.role === "错误").map(row => row.text).join("\n");
  if (!detail && (item.status >= 400 || !item.status)) detail = payload.responseBody;
  return [item.error, detail && detail !== item.error ? detail : ""].filter(Boolean).join("：");
}

/** 冷却按真实 Unix 毫秒截止时间逐秒显示；到期只说明可重试/探测，不改写请求或健康终态。 */
export function protectionRemaining(until: number, now: number): string {
  const seconds = Math.ceil((until - now) / 1000);
  if (seconds <= 0) return "已到期，等待实际请求重新判定";
  const hours = Math.floor(seconds / 3600);
  return `${hours ? `${hours}小时` : ""}${Math.floor(seconds % 3600 / 60)}分${seconds % 60}秒`;
}
