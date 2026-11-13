<script lang="ts">
  import * as Dialog from "$lib/components/ui/dialog";
  import * as Table from "$lib/components/ui/table";
  import * as ToggleGroup from "$lib/components/ui/toggle-group";
  import { Button } from "$lib/components/ui/button";
  import { Hint } from "$lib/components/ui/tooltip";
  import { ArrowDown, ArrowUp } from "@lucide/svelte";
  import type { OverviewMetrics, OverviewRow } from "$lib/generated/mygo";
  import { cacheRate, compact, formatMetric, groupRows, successRate, type Dimension } from "./data";
  /** 明细按一个实体一行展示；仅排序与维度切换，不提供下钻或实体筛选。 */
  let { open = $bindable(false), rows, period, dimension = $bindable("model") }: {
    /** 弹窗开关及所属总览时间范围。 */ open: boolean; rows: OverviewRow[]; period: string;
    /** 与分布区共用分析维度。 */ dimension: Dimension;
  } = $props();
  /** 数值列的排序身份，不从格式化文字比较。 */
  type Sort = "requests" | "tokens" | "cache" | "standardCost" | "cost" | "success" | "firstByte" | "outputRate";
  /** 默认标准费用降序；未知数值始终放到末尾。 */
  let sort = $state<Sort>("standardCost"), descending = $state(true);
  /** 表头标题和排序字段保持同一归属。 */
  const columns: { key: Sort; label: string }[] = [
    { key: "requests", label: "请求数" }, { key: "tokens", label: "Token 总量" },
    { key: "cache", label: "缓存命中" }, { key: "standardCost", label: "标准费用" },
    { key: "cost", label: "实际支出" }, { key: "success", label: "成功率" },
    { key: "firstByte", label: "首字延迟" }, { key: "outputRate", label: "输出速度" },
  ];
  /** 切换列时降序，重复点击同列反转方向。 */
  function changeSort(key: Sort): void { if (sort === key) descending = !descending; else { sort = key; descending = true; } }
  /** 平均计时只使用真实已测得的样本。 */
  function value(total: OverviewMetrics, key: Sort): number | null {
    if (key === "cache") return cacheRate(total);
    if (key === "success") return successRate(total);
    if (key === "firstByte") return total.firstByteCount ? total.firstByteTotal / total.firstByteCount : null;
    if (key === "outputRate") return total.outputRateCount ? total.outputRateTotal / total.outputRateCount : null;
    if (key === "standardCost" && total.unknownStandard >= total.requests && total.standardCost === 0 || key === "cost" && total.unknownCost >= total.requests && total.cost === 0 || key === "tokens" && total.unknownUsage >= total.requests && total.tokens === 0) return null;
    return total[key];
  }
  /** 显示与排序使用相同的未知规则，金额和Token复用总览格式。 */
  function display(total: OverviewMetrics, key: Sort): string {
    const amount = value(total, key);
    if (amount === null) return "—";
    if (key === "cache" || key === "success") return `${amount.toFixed(1)}%`;
    if (key === "firstByte") return `${(amount / 1000).toFixed(2)} s`;
    if (key === "outputRate") return `${amount.toFixed(1)} t/s`;
    return formatMetric(total, key);
  }
  /** 缺失值不参与数值比较，同值用稳定身份保证排序稳定。 */
  const groups = $derived(groupRows(rows, dimension).sort((a, b) => {
    const av = value(a.total, sort), bv = value(b.total, sort);
    if (av === null) return bv === null ? a.id.localeCompare(b.id) : 1;
    if (bv === null) return -1;
    return (descending ? bv - av : av - bv) || a.id.localeCompare(b.id);
  }));
  /** Token提示以百万为单位呈现构成，缓存写入归入非读取输入。 */
  function tokenDescription(total: OverviewMetrics): string {
    if (total.detailsKnown !== total.requests || total.unknownUsage) return "历史用量拆分不完整";
    return `总量 ${(total.tokens / 1e6).toLocaleString("en-US", { maximumFractionDigits: 6 })}M · 输入 ${((total.context - total.cached) / 1e6).toLocaleString("en-US", { maximumFractionDigits: 6 })}M · 输出 ${(total.output / 1e6).toLocaleString("en-US", { maximumFractionDigits: 6 })}M · 缓存读取 ${(total.cached / 1e6).toLocaleString("en-US", { maximumFractionDigits: 6 })}M`;
  }
</script>

{#snippet token(total: OverviewMetrics)}
  <div class="overview-token"><span>{formatMetric(total, "tokens")}</span>
    <Hint text={tokenDescription(total)}>{#snippet children(props)}
      <Button {...props} variant="token" size="token" class="overview-token-bar" aria-label={tokenDescription(total)}>
        {#if total.tokens && total.detailsKnown === total.requests && !total.unknownUsage}
          <i class="overview-token-input" style:width={`${(total.context - total.cached) / total.tokens * 100}%`}></i><i class="overview-token-output" style:width={`${total.output / total.tokens * 100}%`}></i><i class="overview-token-cache" style:width={`${total.cached / total.tokens * 100}%`}></i>
        {/if}
      </Button>
    {/snippet}</Hint>
  </div>
{/snippet}

<Dialog.Root bind:open><Dialog.Content class="w-[min(1180px,92vw)] max-w-[92vw] sm:max-w-[min(1180px,92vw)]">
  <Dialog.Header><Dialog.Title>用量明细</Dialog.Title><Dialog.Description>{period} · {dimension === "key" ? "上游 Key 按实际尝试统计" : "全部调用"}</Dialog.Description></Dialog.Header>
  <ToggleGroup.Root spacing={1} type="single" variant="segmented" size="xs" value={dimension} onValueChange={value => { if (value) dimension = value as Dimension; }} class="w-fit" aria-label="明细分析维度">
    <ToggleGroup.Item value="model">模型</ToggleGroup.Item><ToggleGroup.Item value="provider">供应商</ToggleGroup.Item><ToggleGroup.Item value="key">上游 Key</ToggleGroup.Item>
  </ToggleGroup.Root>
  {#if groups.length}
    <div class="overview-details-desktop"><Table.Root class="overview-details-table">
      <Table.Header><Table.Row><Table.Head>{dimension === "model" ? "模型" : dimension === "provider" ? "供应商" : "上游 Key"}</Table.Head>
        {#each columns as column}<Table.Head aria-sort={sort === column.key ? descending ? "descending" : "ascending" : "none"}><Button variant="text" size="table-inline" onclick={() => changeSort(column.key)}>{column.label}{#if sort === column.key}{#if descending}<ArrowDown />{:else}<ArrowUp />{/if}{/if}</Button></Table.Head>{/each}
      </Table.Row></Table.Header>
      <Table.Body>{#each groups as group}<Table.Row><Table.Cell><strong>{group.name}</strong>{#if group.detail}<small>{group.detail}</small>{/if}</Table.Cell>
        {#each columns as column}<Table.Cell>{#if column.key === "tokens"}{@render token(group.total)}{:else}{display(group.total, column.key)}{/if}</Table.Cell>{/each}
      </Table.Row>{/each}</Table.Body>
    </Table.Root></div>
    <div class="overview-details-mobile">
      <div class="overview-details-sort"><span>排序</span><ToggleGroup.Root spacing={1} type="single" variant="segmented" size="xs" value={sort} onValueChange={value => { if (value) changeSort(value as Sort); }} aria-label="明细排序">{#each columns as column}<ToggleGroup.Item value={column.key}>{column.label}</ToggleGroup.Item>{/each}</ToggleGroup.Root><Button variant="ghost" size="icon-xs" aria-label={descending ? "改为升序" : "改为降序"} onclick={() => descending = !descending}>{#if descending}<ArrowDown />{:else}<ArrowUp />{/if}</Button></div>
      {#each groups as group}<article class="overview-detail-card"><header><strong>{group.name}</strong>{#if group.detail}<small>{group.detail}</small>{/if}</header><dl>{#each columns as column}<div><dt>{column.label}</dt><dd>{#if column.key === "tokens"}{@render token(group.total)}{:else}{display(group.total, column.key)}{/if}</dd></div>{/each}</dl></article>{/each}
    </div>
  {:else}<p class="text-sm text-muted-foreground">当前范围暂无用量明细。</p>{/if}
</Dialog.Content></Dialog.Root>

<style>
  :global(.overview-details-table){table-layout:fixed;font-variant-numeric:tabular-nums}
  .overview-details-desktop :global(th),.overview-details-desktop :global(td){text-align:right;font-size:11px;padding:14px 8px;white-space:normal}
  .overview-details-desktop :global(th:first-child),.overview-details-desktop :global(td:first-child){width:22%;text-align:left;overflow-wrap:anywhere}
  .overview-details-desktop :global(th:not(:first-child)){width:9.75%}
  .overview-details-desktop :global(th button){font-size:11px;gap:2px}
  strong{font-weight:550}small{display:block;font-size:10px;color:var(--muted-foreground);margin-top:3px}
  .overview-token{display:flex;flex-direction:column;align-items:flex-end;gap:5px}
  .overview-token-input,.overview-token-output,.overview-token-cache{display:block;height:100%;flex-shrink:0}
  .overview-token-input{background:var(--positive)}.overview-token-output{background:var(--primary)}.overview-token-cache{background:var(--parameter)}
  .overview-details-mobile{display:none}
  @media(max-width:1100px){
    .overview-details-desktop{display:none}.overview-details-mobile{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px}
    .overview-details-sort{grid-column:1/-1;display:flex;flex-wrap:wrap;gap:8px;align-items:center;font-size:11px;color:var(--muted-foreground)}
    .overview-details-sort :global([data-slot=toggle-group]){flex-wrap:wrap;justify-content:flex-start}
    .overview-detail-card{border:1px solid var(--border);border-radius:var(--radius);padding:16px;min-width:0}
    .overview-detail-card header{overflow-wrap:anywhere;font-size:12px}.overview-detail-card dl{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px;margin-top:16px}
    dt{color:var(--muted-foreground);font-size:10px}dd{font-size:12px;margin:3px 0 0;font-variant-numeric:tabular-nums}.overview-token{align-items:flex-start}
  }
  @media(max-width:600px){.overview-details-mobile{grid-template-columns:1fr}}
</style>
