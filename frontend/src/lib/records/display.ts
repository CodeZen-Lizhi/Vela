import type { RecordsRecord, Query, Usage } from "$lib/generated/mygo";

/** 状态与筛选显示词由记录页面和详情共用。 */
export const states = [
  { value: "success", label: "成功" },
  { value: "failed", label: "失败" },
  { value: "local", label: "未发上游" },
  { value: "active", label: "进行中" },
];
/** 真实执行路径只描述实际调用，不混入最终结论。 */
export const executions = [
  { value: "direct", label: "直接执行" },
  { value: "retry", label: "发生重试" },
  { value: "transfer", label: "发生转移" },
  { value: "both", label: "重试并转移" },
];
/** 客户端传输方式不使用上游模式替代。 */
export const transports = [
  { value: "stream", label: "流式" },
  { value: "standard", label: "标准" },
  { value: "websocket", label: "WebSocket" },
];
/** 来源区分普通业务、模型联通与指纹采样。 */
export const sources = [
  { value: "", label: "全部来源" },
  { value: "business", label: "业务请求" },
  { value: "modeltest", label: "模型测试" },
  { value: "verification", label: "模型验证" },
];
/** 业务类型使用真实端点语义，与 API 格式分开。 */
export const types = [
  { value: "chat", label: "对话" },
  { value: "image", label: "图片" },
  { value: "video", label: "视频" },
  { value: "embedding", label: "向量" },
  { value: "rerank", label: "重排" },
];
/** 协议名称统一归一化显示，未知协议保持原值。 */
export function protocol(value: string): string {
  return (
    (
      {
        chat: "OpenAI Chat Completions",
        chat_completions: "OpenAI Chat Completions",
        messages: "Anthropic Messages",
        "openai:chat": "OpenAI Chat Completions",
        responses: "OpenAI Responses",
        "openai:responses": "OpenAI Responses",
        anthropic: "Anthropic Messages",
        "claude:messages": "Anthropic Messages",
        gemini: "Gemini generateContent",
        "gemini:generate_content": "Gemini generateContent",
        image: "OpenAI Images",
        video: "OpenAI Videos",
        embedding: "OpenAI Embeddings",
        rerank: "Rerank",
      } as { [key: string]: string }
    )[value] ?? value
  );
}
/** 只统计实际尝试之间的重试和转移，视频轮询不构成新的执行路径。 */
export function executionCounts(row: RecordsRecord): { retries: number; transfers: number } {
  let previous: RecordsRecord["attempts"][number] | undefined;
  let retries = 0, transfers = 0;
  for (const current of row.attempts) {
    if (current.polling) continue;
    if (previous) {
      if (previous.providerId === current.providerId && previous.keyId === current.keyId) retries++;
      else transfers++;
    }
    previous = current;
  }
  return { retries, transfers };
}
/** 从固定选项读取标签，不把未知值当作默认成功。 */
export function label(
  options: { value: string; label: string }[],
  value: string,
): string {
  return options.find((x) => x.value === value)?.label ?? value;
}
/** 完整数值保留千分位；不使用中文万/亿，缺失与真实零值区分。 */
export function number(value: number | null | undefined): string {
  return value == null
    ? "—"
    : new Intl.NumberFormat("en-US", {
        maximumFractionDigits: 2,
      }).format(value);
}
/** Token 与速度用英文 K/M 数量级保持旧版数字密度，完整值在对应提示中保留。 */
export function compact(value: number | null | undefined): string {
  return value == null ? "—" : new Intl.NumberFormat("en-US", {
    notation: "compact",
    maximumFractionDigits: 2,
  }).format(value);
}
/** 美元估算金额显示六位小数；未知值不补零。 */
export function money(value: number | null | undefined): string {
  return value == null ? "—" : `$${value.toFixed(6)}`;
}
/** 毫秒展示保持计时精度并缩短列表宽度。 */
export function duration(value: number | null | undefined): string {
  return value == null
    ? "—"
    : value < 1000
      ? `${Math.round(value)} ms`
      : `${(value / 1000).toFixed(2)} s`;
}
/** 列表沿用旧版秒数格式，两位小数紧邻 s，缺失首字显示短横线。 */
export function seconds(value: number | null | undefined): string {
  return value == null ? "-" : `${(value / 1000).toFixed(2)}s`;
}
/** 缓存命中率从归一化完整上下文计算。 */
export function cache(usage: Usage | null): string {
  return usage && usage.context > 0
    ? `${((100 * usage.cacheRead) / usage.context).toFixed(2)}%`
    : "—";
}
/** 请求时间按本机显示，日期使用 YYYY-MM-DD；统计日的固定时区由后端独立维护。 */
export function date(value: number, part: "time" | "day" = "time"): string {
  return new Intl.DateTimeFormat(
    part === "time" ? "zh-CN" : "sv-SE",
    part === "time"
      ? { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }
      : { year: "numeric", month: "2-digit", day: "2-digit" },
  ).format(value);
}
/** 新查询默认每页二十条，所有过滤和快照身份由后端验证。 */
export function emptyQuery(): Query {
  return {
    search: "",
    start: 0,
    end: 0,
    before: 0,
    modelId: "",
    providerId: "",
    protocol: "",
    source: "",
    states: [],
    types: [],
    transports: [],
    executions: [],
    page: 1,
    size: 20,
  };
}
/** 对已展示记录应用同一组状态/类型/传输/路径过滤，不插入新的身份。 */
export function matches(record: RecordsRecord, q: Query): boolean {
  return (
    (!q.states.length || q.states.includes(record.state)) &&
    (!q.types.length || q.types.includes(record.type)) &&
    (!q.transports.length ||
      q.transports.includes(record.transport || (record.stream ? "stream" : "standard"))) &&
    (!q.executions.length || q.executions.includes(record.execution))
  );
}
/** JSON 原始数据在无法解析时按原文显示，格式化不改变内容。 */
export function pretty(text: string): string {
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}
