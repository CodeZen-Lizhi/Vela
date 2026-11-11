<script lang="ts">
  import type { Pricing, Rate } from "$lib/generated/mygo";
  import { rateNames, unitLabel } from "./display";
  /** 模型行展示标准主价，缓存和分时等完整规则保留在详情中。 */
  let {
    pricing,
    kind,
    modified = false,
  }: { pricing: Pricing; kind: string; modified?: boolean } = $props();
  /** 按模型计价类型选择主价，不把缺失值当成零。 */
  const rates = $derived.by((): Rate[] => {
    if (kind === "image") {
      const perImage = pricing.base.find((r) => r.key === "per_image");
      if (perImage) return [perImage];
      if (pricing.images.length)
        return [
          {
            key: "per_image",
            unit: "image",
            price: Math.min(...pricing.images.map((p) => p.price)),
          },
        ];
    }
    if (kind === "video") {
      const perSecond = pricing.base.find((r) => r.key === "per_second");
      if (perSecond) return [perSecond];
      if (pricing.videos.length)
        return [
          {
            key: "per_second",
            unit: "second",
            price: Math.min(...pricing.videos.map((p) => p.price)),
          },
        ];
    }
    return pricing.base
      .filter(
        (r) =>
          r.key === "input" || r.key === "output" || r.key === "per_request",
      )
      .slice(0, 2);
  });
  /** 美元价格至少保留两位，小额缓存价格保留有效小数。 */
  function formatPrice(value: number | undefined): string {
    if (value === undefined || !Number.isFinite(value)) return "未填写";
    return `$${value.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 8 })}`;
  }
</script>

<div class="model-price">
  <div class="price-amount">
    {#each rates as rate, i}{#if i}<span class="price-divider">/</span
        >{/if}<strong>{formatPrice(rate.price)}</strong
      >{/each}{#if !rates.length}<span class="text-sm text-muted-foreground"
        >价格未知</span
      >{/if}
  </div>
  {#if rates.length}<p class="price-basis">
      标准价 · {rates.map((r) => rateNames[r.key]).join(" / ")} · {unitLabel(
        rates[0]?.unit ?? "",
      ).replace("美元 / ", "")}
    </p>{/if}
  <div class="price-traits">
    {#if pricing.base.some((r) => r.key.startsWith("cache_"))}<span
        class="price-trait cache">缓存价</span
      >{/if}
    {#if pricing.tiers.length}<span class="price-trait tier">阶梯价</span>{/if}
    {#if pricing.modes.length}<span class="price-trait speed">Fast</span>{/if}
    {#if pricing.schedules.length}<span class="price-trait time">峰谷价</span
      >{/if}
    {#if pricing.images.length}<span class="price-trait tier">图片矩阵</span
      >{/if}
    {#if pricing.videos.length}<span class="price-trait tier">分辨率</span>{/if}
    {#if modified}<span class="price-trait">本地修改</span>{/if}
  </div>
</div>
