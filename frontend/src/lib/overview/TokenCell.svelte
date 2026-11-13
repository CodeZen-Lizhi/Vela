<script lang="ts">
  import { ChevronDown } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import * as Popover from "$lib/components/ui/popover";
  import type { OverviewMetrics } from "$lib/generated/mygo";
  import { formatMetric } from "./data";

  /** 总量单行展示，点击后查看精确构成；不完整分项保持未知。 */
  let { total }: {
    /** 当前模型、供应商或上游 Key 分组的聚合指标，数量单位为 Token。 */
    total: OverviewMetrics;
  } = $props();

  /** 沿用统一数量格式，仅把 K/M/B/T 单位拆出以弱化视觉权重。 */
  const formatted = $derived(formatMetric(total, "tokens"));
  const compactParts = $derived(formatted.match(/^(.+?)([KMBT])$/));
  /** 分项完整时才展示精确拆分，空数据的已知零仍显示为零。 */
  const partsKnown = $derived(total.detailsKnown === total.requests && !total.unknownUsage);
  /** 精确总量沿用已有未知判断，不把未知总量补零。 */
  const exactTotal = $derived(formatted === "—" ? "—" : total.tokens.toLocaleString("en-US"));
  /** 构成项保存显示名、精确 Token 数和主题色；缓存写入归入非缓存读取输入。 */
  const parts = $derived([
    { label: "非缓存输入", value: total.context - total.cached, color: "bg-positive" },
    { label: "输出", value: total.output, color: "bg-primary" },
    { label: "缓存读取", value: total.cached, color: "bg-parameter" },
  ]);
</script>

<div class="overview-token-value flex min-w-0 justify-end">
  <Popover.Root>
    <Popover.Trigger>
      {#snippet child({ props })}
        <Button {...props} variant="metric" size="table-inline" class="min-w-0 max-w-full justify-end" aria-label={`查看 Token 构成，总量 ${exactTotal}`}>
          <span class="truncate tabular-nums">{compactParts?.[1] ?? formatted}{#if compactParts}<span class="font-normal text-muted-foreground">{compactParts[2]}</span>{/if}</span>
          <ChevronDown class="text-muted-foreground group-aria-expanded/button:rotate-180" aria-hidden="true" />
        </Button>
      {/snippet}
    </Popover.Trigger>
    <Popover.Content align="end" sideOffset={8} class="w-60 max-w-[calc(100vw-2rem)] gap-3" aria-label="Token 构成">
      <Popover.Header class="flex-row items-center justify-between">
        <Popover.Title>Token 构成</Popover.Title>
        <span class="text-xs text-muted-foreground">Token</span>
      </Popover.Header>
      <dl class="grid gap-2 text-xs tabular-nums">
        {#each parts as part}
          <div class="flex items-center justify-between gap-4">
            <dt class="flex items-center gap-2 text-muted-foreground"><span class={`size-1.5 rounded-full ${part.color}`} aria-hidden="true"></span>{part.label}</dt>
            <dd>{partsKnown ? part.value.toLocaleString("en-US") : "—"}</dd>
          </div>
        {/each}
        <div class="mt-1 flex items-center justify-between gap-4 border-t border-border pt-3">
          <dt class="text-muted-foreground">总量</dt><dd class="font-medium">{exactTotal}</dd>
        </div>
      </dl>
    </Popover.Content>
  </Popover.Root>
</div>
