<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import * as Tooltip from "$lib/components/ui/tooltip";
  import { formatMetric, money, compact, type Group, type Metric } from "./data";
  /** 趋势只绘制时段合计，柱线切换不改变统计数据。 */
  let { buckets, metric, style }: {
    /** 与顶部时间范围一致的时间桶。 */ buckets: Group[];
    /** 当前指标与图形样式。 */ metric: Metric; style: string;
  } = $props();
  /** 当前展开提示的时间桶，以稳定键跟随统计刷新。 */
  let openBucket = $state<string | null>(null);
  /** 费用视图始终并列显示两种金额，便于对照。 */
  const isCost = $derived(metric === "standardCost" || metric === "cost");
  /** 纵轴留出15%顶部空间，零数据仍保持有效坐标。 */
  const maximum = $derived(Math.max(0.000001, ...buckets.map(bucket => bucket.total[metric])) * 1.15);
  /** 线条位置与柱状图使用同一值域。 */
  const path = $derived(buckets.map((bucket, index) => `${index ? "L" : "M"}${(index + 0.5) / buckets.length * 900},${210 - bucket.total[metric] / maximum * 200}`).join(" "));
  /** 横轴最多显示七个主刻度，避免日期重叠。 */
  const tickEvery = $derived(Math.max(1, Math.ceil(buckets.length / 7)));
  /** 纵轴为所有指标的最长刻度预留相同宽度，费用口径与指标切换不挤动绘图区。 */
  const axisWidth = $derived(Math.max(8, ...buckets.flatMap(bucket => [money(bucket.total.standardCost * 1.15).length, money(bucket.total.cost * 1.15).length, compact(bucket.total.tokens * 1.15).length, compact(bucket.total.requests * 1.15).length])));

  /** 生成浮层和可访问说明共用的数值，计数使用原值，未知仍显示破折号。 */
  function trendValues(bucket: Group, metric: Metric, isCost: boolean) {
    if (isCost) {
      return [
        { label: "标准费用", value: formatMetric(bucket.total, "standardCost") },
        { label: "实际支出", value: formatMetric(bucket.total, "cost") },
      ];
    }
    const known = formatMetric(bucket.total, metric) !== "—";
    return [{ label: metric === "tokens" ? "Token" : "请求", value: known ? bucket.total[metric].toLocaleString("en-US") : "—" }];
  }
</script>

<div class="overview-trend" aria-label="用量趋势图" style:--overview-axis-width={`${axisWidth}ch`}>
  <div class="overview-axis-y">{#each [1, 0.5, 0] as ratio}<span>{metric === "standardCost" || metric === "cost" ? money(maximum * ratio) : compact(maximum * ratio)}</span>{/each}</div>
  <div class="overview-plot">
    <svg viewBox="0 0 900 220" preserveAspectRatio="none" aria-hidden="true">
      {#each [10, 110, 210] as y}<line x1="0" x2="900" y1={y} y2={y} class="overview-gridline" />{/each}
      {#if style === "line"}<path d={path} fill="none" stroke="var(--primary)" stroke-width="2.4" vector-effect="non-scaling-stroke" />
      {:else}{#each buckets as bucket, index}<rect x={(index + 0.18) / buckets.length * 900} y={210 - bucket.total[metric] / maximum * 200} width={900 / buckets.length * 0.64} height={bucket.total[metric] / maximum * 200} rx="2.5" fill="var(--primary)" />{/each}{/if}
    </svg>
    <div class="overview-plot-targets" role="group" aria-label="各时段用量">
      {#each buckets as bucket (bucket.id)}
        {@const period = bucket.name.includes(":") ? `${bucket.detail} ${bucket.name}` : bucket.detail}
        {@const values = trendValues(bucket, metric, isCost)}
        <Tooltip.Root
          delayDuration={0}
          disableHoverableContent
          disableCloseOnTriggerClick
          bind:open={() => openBucket === bucket.id, (open) => {
            if (open) openBucket = bucket.id;
            else if (openBucket === bucket.id) openBucket = null;
          }}
        >
          <Tooltip.Trigger>
            {#snippet child({ props })}
              <Button
                {...props}
                type="button"
                variant="plot"
                size="plot"
                aria-label={`${period}，${values.map(({ label, value }) => `${label} ${value}`).join("，")}`}
                onclick={() => openBucket = bucket.id}
              />
            {/snippet}
          </Tooltip.Trigger>
          <Tooltip.Content variant="detail" side="bottom">
            <div class="font-medium">{period}</div>
            <dl class="mt-1 grid grid-cols-[auto_auto] gap-x-4 tabular-nums">
              {#each values as { label, value }}
                <dt class="text-muted-foreground">{label}</dt><dd class="text-right">{value}</dd>
              {/each}
            </dl>
          </Tooltip.Content>
        </Tooltip.Root>
      {/each}
    </div>
    <div class="overview-axis-x">{#each buckets as bucket, index}{#if index % tickEvery === 0}<span style:left={`${(index + 0.5) / buckets.length * 100}%`}>{bucket.name}</span>{/if}{/each}</div>
  </div>
</div>

<style>
  .overview-trend{display:flex;height:272px;gap:12px;padding:12px 24px 24px 12px}
  .overview-axis-y{display:flex;flex-direction:column;justify-content:space-between;width:var(--overview-axis-width);flex-shrink:0;padding-bottom:33px;text-align:right;white-space:nowrap;font-size:10px;color:var(--muted-foreground);font-variant-numeric:tabular-nums}
  .overview-plot{position:relative;flex:1;min-width:0;padding-bottom:25px}
  svg{width:100%;height:100%;overflow:visible}
  .overview-gridline{stroke:var(--border);stroke-dasharray:2 6;vector-effect:non-scaling-stroke}
  .overview-plot-targets{position:absolute;inset:0 0 25px;display:flex}
  .overview-axis-x{position:absolute;inset:auto 0 0;height:18px;font-size:10px;color:var(--muted-foreground)}
  .overview-axis-x span{position:absolute;transform:translateX(-50%);white-space:nowrap}
  @media(max-width:900px){.overview-trend{height:245px}}
</style>
