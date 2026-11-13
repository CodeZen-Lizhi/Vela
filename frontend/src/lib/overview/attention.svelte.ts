import { formatUSD } from "../currency.js";
import { VelaService, type AttentionSettings, type ProviderKey } from "$lib/generated/mygo";
import { isMyGo } from "mygo-runtime";
import { errorMessage } from "$lib/settings/sections";

/** 关注事项只陈述本地已保存事实，不触发查询上游或模型验证。 */
export interface AttentionItem {
  /** 稳定事项身份，倍率已读操作只作用于这一事件。 */
  id: string;
  /** 类别决定图标与语义色；path、title、detail 提供完整文字结论。 */
  kind: "balance" | "difference" | "health" | "rate-up" | "rate-down";
  path: string; title: string; detail: string;
}
/** 两次已知倍率之间的最近一次未读变化。 */
interface RateChange {
  /** 事件身份和前后倍率，零倍率有效。 */
  id: string; before: number; after: number;
}
/** 本机 WebView 持久观察基线；不包含密钥和账户凭据。 */
interface Tracker {
  /** 组合身份到已知倍率、最近未读事件的映射。 */
  rates: Record<string, number>; changes: Record<string, RateChange>;
}

/** 与设置页同源的关注状态，由应用外壳持有，离开总览仍观察倍率。 */
export const attention = $state({
  preferences: null as AttentionSettings | null,
  items: [] as AttentionItem[], providerCount: 0, ready: false,
  error: "", loaded: false, seconds: 10,
});
/** 存储身份固定于本机配置；观察数据不随用量时间范围变化。 */
const storageKey = "vela-overview-rates-v1";
/** 当前观察器与只读事项缓存，写入失败时不改变已读状态。 */
let tracker: Tracker = { rates: {}, changes: {} };
let latestItems: AttentionItem[] = [];

/** 恢复存储时校验有限倍率和事件结构，损坏数据明确提示而非覆盖。 */
function restore(): void {
  const raw = localStorage.getItem(storageKey);
  if (!raw) return;
  const parsed: unknown = JSON.parse(raw);
  if (!parsed || typeof parsed !== "object" || !("rates" in parsed) || !("changes" in parsed)) throw new Error("倍率提醒记录格式无效");
  const { rates, changes } = parsed;
  if (!rates || typeof rates !== "object" || Array.isArray(rates) || !changes || typeof changes !== "object" || Array.isArray(changes)) throw new Error("倍率提醒记录格式无效");
  for (const value of Object.values(rates)) if (typeof value !== "number" || !Number.isFinite(value) || value < 0) throw new Error("倍率提醒记录格式无效");
  for (const value of Object.values(changes)) {
    if (!value || typeof value !== "object" || typeof value.id !== "string" || typeof value.before !== "number" || typeof value.after !== "number" || !Number.isFinite(value.before) || !Number.isFinite(value.after) || value.before < 0 || value.after < 0) throw new Error("倍率提醒记录格式无效");
  }
  tracker = { rates: rates as Tracker["rates"], changes: changes as Tracker["changes"] };
}

/** 仅写入成功后更新内存观察基线，失败保留可重试状态。 */
function persist(next: Tracker): void {
  localStorage.setItem(storageKey, JSON.stringify(next));
  tracker = next;
}

/** 将当前偏好应用到既有事实，关闭提醒不关闭任何采集。 */
function filterItems(items: AttentionItem[], preferences: AttentionSettings): AttentionItem[] {
  return items.filter(item => preferences[item.kind.startsWith("rate-") ? "rate" : item.kind as "balance" | "difference" | "health"]);
}

/** 读取启用调用链和当前结果，首次倍率只建基线；未知、停用或删除后清除该身份。 */
export async function refreshAttention(): Promise<void> {
  const [settings, snapshot, verification] = await Promise.all([VelaService.settings(), VelaService.providers(), VelaService.verification()]);
  const items: AttentionItem[] = [];
  const next: Tracker = { rates: {}, changes: {} };
  const knownKeys: { key: ProviderKey; path: string }[] = [];
  let ready = false;
  for (const summary of snapshot.providers.filter(item => item.provider.enabled)) {
    const provider = summary.provider;
    const [keys, models, account] = await Promise.all([VelaService.providerKeys(provider.id), VelaService.providerModels(provider.id), VelaService.providerAccount(provider.id)]);
    const active = [];
    for (const key of keys.filter(key => key.enabled && key.hasSecret)) {
      const permissions = key.allModels ? null : await VelaService.providerKeyModelPermissions(key.id);
      const permitted = models.filter(model => model.enabled && (key.allModels || permissions?.modelIds.includes(model.modelId)));
      if (permitted.length) active.push({ key, models: permitted });
    }
    if (!active.length) continue;
    ready = true;
    const balance = account.balance;
    // 美元阈值不能直接比较人民币或积分，保持原币种事实。
    if (balance.amount !== null && balance.currency === "USD" && balance.amount < settings.attention.balanceThreshold) {
      items.push({ id: `balance:${provider.id}`, kind: "balance", path: provider.name, title: "供应商余额不足", detail: `余额 ${formatUSD(balance.amount)} · 低于 ${formatUSD(settings.attention.balanceThreshold)}` });
    }
    if (summary.health.state !== "normal") {
      const state = { degraded: "降级", abnormal: "异常", unavailable: "不可用" }[summary.health.state];
      items.push({ id: `health:${provider.id}`, kind: "health", path: provider.name, title: "供应商健康异常", detail: summary.health.reason || `${state} · 健康度 ${summary.health.percent ?? "—"}%` });
    }
    for (const { key, models } of active) {
      const path = `${provider.name} / ${key.name}`;
      knownKeys.push({ key, path });
      for (const model of models) {
        const latest = verification.reports.filter(report => report.target.providerId === provider.id && report.target.keyId === key.id && report.target.modelId === model.modelId).sort((a, b) => b.time - a.time)[0];
        if (latest?.conclusion === "difference" && latest.state === "complete") items.push({ id: `difference:${latest.id}`, kind: "difference", path: `${path} / ${model.name}`, title: "模型验证存在差异", detail: latest.reason || "最近一次验证存在差异" });
      }
    }
  }
  for (const { key, path } of knownKeys) {
    if (key.multiplier === null) continue;
    const id = JSON.stringify([key.providerId, key.id]);
    next.rates[id] = key.multiplier;
    const before = tracker.rates[id];
    const change = before !== undefined && before !== key.multiplier ? { id: crypto.randomUUID(), before, after: key.multiplier } : tracker.changes[id];
    if (change) {
      next.changes[id] = change;
      items.push({ id: change.id, kind: change.after > change.before ? "rate-up" : "rate-down", path, title: `Key 倍率${change.after > change.before ? "升高" : "降低"}`, detail: `${change.before}× → ${change.after}×` });
    }
  }
  persist(next);
  latestItems = items;
  attention.preferences = settings.attention;
  attention.items = filterItems(items, settings.attention);
  attention.providerCount = snapshot.providers.length;
  attention.ready = ready;
  attention.seconds = settings.general.dashboardSeconds;
  attention.loaded = true;
  attention.error = "";
}

/** 倍率已读先持久化，失败保留当前事项并显示原因。 */
export function markRateRead(id: string): void {
  try {
    const changes = Object.fromEntries(Object.entries(tracker.changes).filter(([, change]) => change.id !== id));
    persist({ rates: tracker.rates, changes });
    latestItems = latestItems.filter(item => item.id !== id);
    attention.items = attention.items.filter(item => item.id !== id);
    attention.error = "";
  } catch (cause) { attention.error = `无法保存已读状态：${errorMessage(cause)}`; }
}

/** 设置编辑器保存成功后立即应用同一份偏好，不等待下轮刷新。 */
export function applyAttentionPreferences(preferences: AttentionSettings): void {
  attention.preferences = preferences;
  attention.items = filterItems(latestItems, preferences);
}

/** 应用生命周期内持续读本地状态；隐藏时停读，恢复立即重读，离开页面不会丢基线。 */
export function observeAttention(): () => void {
  if (!isMyGo()) return () => {};
  let stopped = false, running = false;
  let timer: ReturnType<typeof setTimeout>;
  let storageValid = true;
  try { restore(); } catch (cause) { storageValid = false; attention.error = `无法读取倍率提醒记录：${errorMessage(cause)}`; }
  /** 串行轮询，销毁或隐藏时不继续安排查询。 */
  const poll = async (): Promise<void> => {
    if (stopped || running || document.hidden || !storageValid) return;
    running = true;
    try { await refreshAttention(); } catch (cause) { attention.error = `关注事项更新失败：${errorMessage(cause)}`; }
    finally {
      running = false;
      if (!stopped) timer = setTimeout(() => void poll(), attention.seconds * 1000);
    }
  };
  /** 页面恢复可见时取消旧计时并重读，避免重叠请求。 */
  const visible = (): void => { if (!document.hidden) { clearTimeout(timer); void poll(); } };
  document.addEventListener("visibilitychange", visible);
  void poll();
  return () => { stopped = true; clearTimeout(timer); document.removeEventListener("visibilitychange", visible); };
}
