<script lang="ts">
  import { formatMetric, money, compact, type Group, type Metric } from "./data";
  /** 趋势只绘制时段合计，柱线切换不改变统计数据。 */
  let { buckets, metric, style }: {
    /** 与顶部时间范围一致的时间桶。 */ buckets: Group[];
    /** 当前指标与图形样式。 */ metric: Metric; style: string;
  } = $props();
  /** 纵轴留出15%顶部空间，零数据仍保持有效坐标。 */
  const maximum = $derived(Math.max(0.000001, ...buckets.map(bucket => bucket.total[metric])) * 1.15);
  /** 线条位置与柱状图使用同一值域。 */
  const path = $derived(buckets.map((bucket, index) => `${index ? "L" : "M"}${(index + 0.5) / buckets.length * 900},${210 - bucket.total[metric] / maximum * 200}`).join(" "));
  /** 横轴最多显示七个主刻度，避免日期重叠。 */
  const tickEvery = $derived(Math.max(1, Math.ceil(buckets.length / 7)));
</script>

<div class="overview-trend" aria-label="用量趋势图">
  <div class="overview-axis-y">{#each [1, 0.5, 0] as ratio}<span>{metric === "standardCost" || metric === "cost" ? money(maximum * ratio) : compact(maximum * ratio)}</span>{/each}</div>
  <div class="overview-plot">
    <svg viewBox="0 0 900 220" preserveAspectRatio="none" role="img" aria-label={buckets.map(bucket => `${bucket.detail} ${bucket.name}，${formatMetric(bucket.total, metric)}`).join("；")}>
      {#each [10, 110, 210] as y}<line x1="0" x2="900" y1={y} y2={y} class="overview-gridline" />{/each}
      {#if style === "line"}<path d={path} fill="none" stroke="var(--primary)" stroke-width="2.4" vector-effect="non-scaling-stroke" />
      {:else}{#each buckets as bucket, index}<rect x={(index + 0.18) / buckets.length * 900} y={210 - bucket.total[metric] / maximum * 200} width={900 / buckets.length * 0.64} height={bucket.total[metric] / maximum * 200} rx="2.5" fill="var(--primary)" />{/each}{/if}
    </svg>
    <div class="overview-axis-x">{#each buckets as bucket, index}{#if index % tickEvery === 0}<span style:left={`${(index + 0.5) / buckets.length * 100}%`}>{bucket.name}</span>{/if}{/each}</div>
  </div>
</div>

<style>
  .overview-trend{display:flex;height:272px;gap:12px;padding:12px 24px 24px 12px}
  .overview-axis-y{display:flex;flex-direction:column;justify-content:space-between;min-width:48px;padding-bottom:33px;text-align:right;font-size:10px;color:var(--muted-foreground);font-variant-numeric:tabular-nums}
  .overview-plot{position:relative;flex:1;min-width:0;padding-bottom:25px}
  svg{width:100%;height:100%;overflow:visible}
  .overview-gridline{stroke:var(--border);stroke-dasharray:2 6;vector-effect:non-scaling-stroke}
  .overview-axis-x{position:absolute;inset:auto 0 0;height:18px;font-size:10px;color:var(--muted-foreground)}
  .overview-axis-x span{position:absolute;transform:translateX(-50%);white-space:nowrap}
  @media(max-width:900px){.overview-trend{height:245px}}
</style>
