<script lang="ts">
  import * as Dialog from "$lib/components/ui/dialog";
  import * as Table from "$lib/components/ui/table";
  import * as ToggleGroup from "$lib/components/ui/toggle-group";
  import { Button } from "$lib/components/ui/button";
  import { Hint } from "$lib/components/ui/tooltip";
  import TokenCell from "./TokenCell.svelte";
  import { ArrowDown, ArrowUp } from "@lucide/svelte";
  import type { OverviewMetrics, OverviewRow } from "$lib/generated/mygo";
  import { cacheRate, formatMetric, groupRows, successRate, type Dimension } from "./data";
  /** 明细按一个实体一行展示；仅排序与维度切换，不提供下钻或实体筛选。 */
  let { open = $bindable(false), rows, period, dimension = $bindable("model") }: {
    /** 弹窗开关及所属总览时间范围。 */ open: boolean; rows: OverviewRow[]; period: string;
    /** 与分布区共用分析维度。 */ dimension: Dimension;
  } = $props();
  /** 数值列的排序身份，不从格式化文字比较。 */
  type Sort = "requests" | "tokens" | "cache" | "standardCost" | "cost" | "success" | "firstByte" | "outputRate";
  /** 默认标准费用降序；null 表示取消排序，保留原始分组顺序。 */
  let sort = $state<Sort | null>("standardCost"), descending = $state(true);
  /** 表头、排序字段和列宽共同维护；名称占16%，数值按内容长度分配其余宽度。 */
  const columns: { key: Sort; label: string; width: string }[] = [
    { key: "requests", label: "请求数", width: "8%" }, { key: "tokens", label: "Token 总量", width: "12%" },
    { key: "cache", label: "缓存命中", width: "9%" }, { key: "standardCost", label: "标准费用", width: "12%" },
    { key: "cost", label: "实际支出", width: "12%" }, { key: "success", label: "成功率", width: "9%" },
    { key: "firstByte", label: "首字延迟", width: "11%" }, { key: "outputRate", label: "输出速度", width: "11%" },
  ];
  /** 同列依次降序、升序、取消；切换到另一列从降序开始。 */
  function changeSort(key: Sort): void {
    if (sort !== key) { sort = key; descending = true; }
    else if (descending) descending = false;
    else sort = null;
  }
  /** 平均计时只使用真实已测得的样本。 */
  function value(total: OverviewMetrics, key: Sort): number | null {
    if (key === "cache") return cacheRate(total);
    if (key === "success") return successRate(total);
    if (key === "firstByte") return total.firstByteCount ? total.firstByteTotal / total.firstByteCount : null;
    if (key === "outputRate") return total.outputRateCount ? total.outputRateTotal / total.outputRateCount : null;
    if (key === "standardCost" && total.unknownStandard >= total.requests && total.standardCost === 0 || key === "cost" && total.unknownCost >= total.requests && total.cost === 0 || key === "tokens" && total.unknownUsage >= total.requests && total.tokens === 0) return null;
    return total[key];
  }
  /** 显示与排序使用相同的未知规则，美元金额统一两位，Token复用总览格式。 */
  function display(total: OverviewMetrics, key: Sort): string {
    const amount = value(total, key);
    if (amount === null) return "—";
    if (key === "cache" || key === "success") return `${amount.toFixed(1)}%`;
    if (key === "firstByte") return `${(amount / 1000).toFixed(2)} s`;
    if (key === "outputRate") return `${amount.toFixed(1)} t/s`;
    return formatMetric(total, key);
  }
  /** 未排序时保留分组输入顺序；排序时未知值置末，同值按稳定身份排列。 */
  const groups = $derived(groupRows(rows, dimension).sort((a, b) => {
    if (!sort) return 0;
    const av = value(a.total, sort), bv = value(b.total, sort);
    if (av === null) return bv === null ? a.id.localeCompare(b.id) : 1;
    if (bv === null) return -1;
    return (descending ? bv - av : av - bv) || a.id.localeCompare(b.id);
  }));
</script>

<!-- 名称及所属供应商共用单行省略与全文提示，桌面表格和窄屏卡片一致。 -->
{#snippet entity(name: string, detail: string)}
  <Hint text={name}>{#snippet children(props)}<strong {...props} class="block truncate">{name}</strong>{/snippet}</Hint>
  {#if detail}<Hint text={detail}>{#snippet children(props)}<small {...props} class="truncate">{detail}</small>{/snippet}</Hint>{/if}
{/snippet}

<Dialog.Root bind:open><Dialog.Content class="flex h-[min(640px,88dvh)] w-[min(1180px,92vw)] max-w-[92vw] flex-col overflow-hidden sm:max-w-[min(1180px,92vw)]">
  <Dialog.Header class="shrink-0 gap-3 pr-6"><Dialog.Title>用量明细</Dialog.Title>
  <div class="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
  <Dialog.Description>{period.split(" · ")[0]}</Dialog.Description>
  <ToggleGroup.Root spacing={1} type="single" variant="segmented" size="xs" value={dimension} onValueChange={value => { if (value) dimension = value as Dimension; }} class="w-fit shrink-0" aria-label="明细分析维度">
    <ToggleGroup.Item value="model">模型</ToggleGroup.Item><ToggleGroup.Item value="provider">供应商</ToggleGroup.Item><ToggleGroup.Item value="key">上游 Key</ToggleGroup.Item>
  </ToggleGroup.Root>
  </div>
  </Dialog.Header>
  <!-- 复用列表页的内容滚动与吸顶表头；独立滚动区保留焦点，支持键盘翻页。 -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div class="list-scroll" role="region" aria-label="用量明细列表内容" tabindex="0">
  {#if groups.length}
    <div class="overview-details-desktop"><Table.Root class="overview-details-table">
      <colgroup><col style:width="16%" />{#each columns as column}<col style:width={column.width} />{/each}</colgroup>
      <Table.Header><Table.Row><Table.Head>{dimension === "model" ? "模型" : dimension === "provider" ? "供应商" : "上游 Key"}</Table.Head>
        <!-- 排序图标始终占位，未排序时隐藏；升降序与取消排序不改变表头文字位置。 -->
        {#each columns as column}<Table.Head aria-sort={sort === column.key ? descending ? "descending" : "ascending" : "none"}><Button variant="text" size="table-inline" class="max-w-full" onclick={() => changeSort(column.key)}><Hint text={column.label}>{#snippet children(props)}<span {...props} class="truncate">{column.label}</span>{/snippet}</Hint><span class="inline-flex shrink-0" class:invisible={sort !== column.key} aria-hidden="true">{#if sort === column.key && !descending}<ArrowUp />{:else}<ArrowDown />{/if}</span></Button></Table.Head>{/each}
      </Table.Row></Table.Header>
      <Table.Body>{#each groups as group}<Table.Row><Table.Cell>{@render entity(group.name, group.detail)}</Table.Cell>
        {#each columns as column}<Table.Cell>{#if column.key === "tokens"}<TokenCell total={group.total} />{:else}<Hint text={display(group.total, column.key)}>{#snippet children(props)}<span {...props} class="block truncate">{display(group.total, column.key)}</span>{/snippet}</Hint>{/if}</Table.Cell>{/each}
      </Table.Row>{/each}</Table.Body>
    </Table.Root></div>
    <div class="overview-details-mobile">
      <div class="overview-details-sort"><span>排序</span><ToggleGroup.Root spacing={1} type="single" variant="segmented" size="xs" value={sort ?? ""} onValueChange={value => { sort = value ? value as Sort : null; descending = true; }} aria-label="明细排序">{#each columns as column}<ToggleGroup.Item value={column.key}>{column.label}</ToggleGroup.Item>{/each}</ToggleGroup.Root><Button variant="ghost" size="icon-xs" aria-label={sort ? descending ? "改为升序" : "取消排序" : "未排序"} disabled={!sort} onclick={() => { if (sort) changeSort(sort); }}>{#if descending}<ArrowDown />{:else}<ArrowUp />{/if}</Button></div>
      {#each groups as group}<article class="overview-detail-card"><header>{@render entity(group.name, group.detail)}</header><dl>{#each columns as column}<div><dt>{column.label}</dt><dd>{#if column.key === "tokens"}<TokenCell total={group.total} />{:else}{display(group.total, column.key)}{/if}</dd></div>{/each}</dl></article>{/each}
    </div>
  {:else}<p class="text-sm text-muted-foreground">当前范围暂无用量明细。</p>{/if}
  </div>
</Dialog.Content></Dialog.Root>

<style>
  :global(.overview-details-table){table-layout:fixed;font-variant-numeric:tabular-nums}
  .overview-details-desktop :global(th),.overview-details-desktop :global(td){text-align:right;font-size:11px;padding:14px 8px;white-space:nowrap}
  .overview-details-desktop :global(th:first-child),.overview-details-desktop :global(td:first-child){text-align:left}
  .overview-details-desktop :global(th button){font-size:11px;gap:2px}
  strong{font-weight:550}small{display:block;font-size:10px;color:var(--muted-foreground);margin-top:3px}
  .overview-details-mobile{display:none}
  @media(max-width:1100px){
    .overview-details-desktop{display:none}.overview-details-mobile{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px}
    .overview-details-sort{grid-column:1/-1;display:flex;flex-wrap:wrap;gap:8px;align-items:center;font-size:11px;color:var(--muted-foreground)}
    .overview-details-sort :global([data-slot=toggle-group]){flex-wrap:wrap;justify-content:flex-start}
    .overview-detail-card{border:1px solid var(--border);border-radius:var(--radius);padding:16px;min-width:0}
    .overview-detail-card header{min-width:0;font-size:12px}.overview-detail-card dl{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px;margin-top:16px}
    dt{color:var(--muted-foreground);font-size:10px}dd{font-size:12px;margin:3px 0 0;font-variant-numeric:tabular-nums}.overview-details-mobile :global(.overview-token-value){justify-content:flex-start}
  }
  @media(max-width:600px){.overview-details-mobile{grid-template-columns:1fr}}
</style>
