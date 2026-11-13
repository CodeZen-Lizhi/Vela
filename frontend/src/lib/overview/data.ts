import type { OverviewMetrics, OverviewRow } from "../generated/mygo";

/** 统计维度使用稳定身份分组，名称只用于展示。 */
export type Dimension = "model" | "provider" | "key";
/** 金额统一为美元，Token 与请求保持原始计数。 */
export type Metric = "standardCost" | "cost" | "tokens" | "requests";
/** 一个趋势时间桶或实体分组及其合计。 */
export interface Group {
  /** 稳定键、显示名和可选的所属供应商名称。 */
  id: string; name: string; detail: string;
  /** 对实际聚合行求和后的指标，不把未知值补成已知。 */
  total: OverviewMetrics;
}

/** 创建独立的零合计；未知数量由真实数据累加。 */
export function emptyMetrics(): OverviewMetrics {
  return { active: 0, requests: 0, success: 0, failed: 0, detailsKnown: 0, tokens: 0, input: 0,
    output: 0, cached: 0, context: 0, cost: 0, standardCost: 0, unknownUsage: 0,
    unknownCost: 0, unknownStandard: 0, firstByteTotal: 0, firstByteCount: 0,
    outputRateTotal: 0, outputRateCount: 0 };
}

/** 按字段求和，逻辑调用与 Key 尝试行由调用方先选定口径。 */
export function summarize(rows: OverviewRow[]): OverviewMetrics {
  const total = emptyMetrics();
  for (const row of rows) {
    for (const key of Object.keys(total) as (keyof OverviewMetrics)[]) total[key] += row.metrics[key];
  }
  return total;
}

/** 在固定统计时区取得日期，避免本机改时区后历史日期漂移。 */
export function dateKey(time: number, timezone: string): string {
  return new Intl.DateTimeFormat("en-CA", { timeZone: timezone, year: "numeric", month: "2-digit", day: "2-digit" }).format(time);
}

/** 对纯日期加减日数，不受夏令时和浏览器本机时区影响。 */
export function shiftDay(day: string, offset: number): string {
  const date = new Date(`${day}T00:00:00Z`);
  date.setUTCDate(date.getUTCDate() + offset);
  return date.toISOString().slice(0, 10);
}

/** 有限数值以英文数量级显示，完整值保留在提示中。 */
export function compact(value: number): string {
  return new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 2 }).format(value);
}

/** 金额显示足够的小数以保留日常调用的小额消耗。 */
export function money(value: number): string {
  return `$${value.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: value > 0 && value < 0.01 ? 6 : 4 })}`;
}

/** 部分已知数值带下界标记；全部未知时显示破折号。 */
export function formatMetric(total: OverviewMetrics, metric: Metric): string {
  const unknown = metric === "standardCost" ? total.unknownStandard : metric === "cost" ? total.unknownCost : metric === "tokens" ? total.unknownUsage : 0;
  if (total.requests > 0 && unknown >= total.requests && total[metric] === 0) return "—";
  const value = total[metric];
  return `${unknown ? "≥ " : ""}${metric === "cost" || metric === "standardCost" ? money(value) : compact(value)}`;
}

/** 缺少历史状态时不声称完整成功率；进行中请求不在已结算统计内。 */
export function successRate(total: OverviewMetrics): number | null {
  const completed = total.requests - total.active;
  return completed > 0 && total.detailsKnown === total.requests ? total.success / completed * 100 : null;
}

/** 缓存命中使用完整输入上下文；用量或历史拆分不完整时保持未知。 */
export function cacheRate(total: OverviewMetrics): number | null {
  return total.context && !total.unknownUsage && total.detailsKnown === total.requests ? total.cached / total.context * 100 : null;
}

/** 上游 Key 使用真实出站尝试；其他维度使用逻辑调用，避免重试重复计数。 */
export function groupRows(rows: OverviewRow[], dimension: Dimension): Group[] {
  const groups = new Map<string, { name: string; detail: string; rows: OverviewRow[] }>();
  for (const row of rows.filter(row => dimension === "key" ? !!row.keyId : !row.keyId)) {
    const id = dimension === "model" ? row.modelId : dimension === "provider" ? row.providerId : JSON.stringify([row.providerId, row.keyId]);
    const name = dimension === "model" ? row.modelName || row.modelId : dimension === "provider" ? row.providerName || "未发上游" : row.keyName || row.keyId;
    const group = groups.get(id) ?? { name, detail: dimension === "key" ? row.providerName : "", rows: [] };
    group.rows.push(row);
    groups.set(id, group);
  }
  return [...groups].map(([id, group]) => ({ id, name: group.name, detail: group.detail, total: summarize(group.rows) }));
}

/** 单日按小时、短范围按日、超过62天按周；旧日累计不伪造小时归属。 */
export function timeBuckets(rows: OverviewRow[], start: string, end: string, timezone: string): Group[] {
  const hourly = start === end && rows.every(row => row.metrics.detailsKnown === row.metrics.requests);
  const days = Math.round((Date.parse(`${end}T00:00:00Z`) - Date.parse(`${start}T00:00:00Z`)) / 86400000) + 1;
  const step = days > 62 ? 7 : 1;
  const buckets: Group[] = [];
  const hourFormat = new Intl.DateTimeFormat("en-GB", { timeZone: timezone, hour: "2-digit", hourCycle: "h23" });
  for (let index = 0; index < (hourly ? 24 : Math.ceil(days / step)); index++) {
    const day = shiftDay(start, index * step);
    const selected = rows.filter(row => hourly ? Number(hourFormat.format(row.time)) === index : row.day >= day && row.day < shiftDay(day, step));
    buckets.push({ id: hourly ? `${start}-${index}` : day, name: hourly ? `${String(index).padStart(2, "0")}:00` : day.slice(5).replace("-", "/"), detail: hourly ? start : day, total: summarize(selected) });
  }
  return buckets;
}

/** 全年日历按周一排列；统计开始前、未来日与跨年前填充不可点击。 */
export function heatDays(year: number, firstDay: string, today: string, rows: OverviewRow[]) {
  const start = `${year}-01-01`, end = `${year}-12-31`;
  const offset = (new Date(`${start}T00:00:00Z`).getUTCDay() + 6) % 7;
  const grouped = new Map<string, OverviewRow[]>();
  for (const row of rows) {
    const group = grouped.get(row.day) ?? [];
    group.push(row); grouped.set(row.day, group);
  }
  const days = [];
  for (let day = shiftDay(start, -offset); day <= end; day = shiftDay(day, 1)) {
    days.push({ day, within: day >= start, recorded: day >= start && day >= firstDay && day <= today, total: summarize(grouped.get(day) ?? []) });
  }
  return days;
}
