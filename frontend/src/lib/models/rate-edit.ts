import type { Rate } from "$lib/generated/mygo";

/** 缓存维度使用同档文本输入单价作为倍率基准，其他计费单位保持独立单价。 */
export function cacheMultiplier(rate: Rate, rates: Rate[]): number | null {
  const input = rates.find((item) => item.key === "input" && item.unit === "million_tokens");
  return rate.key.startsWith("cache_") && rate.unit === "million_tokens" && input && input.price > 0
    ? rate.price / input.price
    : null;
}

/** 修改单价或缓存倍率；输入价变化保持同档缓存倍率，零输入价时保留缓存独立单价。 */
export function updateRate(rates: Rate[], rate: Rate, value: number): void {
  const multiplier = cacheMultiplier(rate, rates);
  if (multiplier !== null) {
    const input = rates.find((item) => item.key === "input" && item.unit === "million_tokens");
    if (input) rate.price = value * input.price;
    return;
  }
  if (rate.key === "input" && rate.unit === "million_tokens" && rate.price > 0 && value > 0) {
    for (const cache of rates) {
      const ratio = cacheMultiplier(cache, rates);
      if (ratio !== null) cache.price = value * ratio;
    }
  }
  rate.price = value;
}
