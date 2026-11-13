<script lang="ts">
  import * as ToggleGroup from "$lib/components/ui/toggle-group";
  import * as Alert from "$lib/components/ui/alert";
  import * as Empty from "$lib/components/ui/empty";
  import { Hint } from "$lib/components/ui/tooltip";
  import { LayoutGrid, Search } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import * as Select from "$lib/components/ui/select";
  import * as InputGroup from "$lib/components/ui/input-group";
  import { Badge } from "$lib/components/ui/badge";
  import type { CatalogEntry } from "$lib/generated/mygo";
  import ModelCapabilities from "./ModelCapabilities.svelte";
  import PriceSummary from "./PriceSummary.svelte";
  import { kinds } from "./display";
  import { searchCatalogEntries } from "./catalog-search";
  import anthropicLogo from "$lib/assets/providers/anthropic.svg";
  import openaiLogo from "$lib/assets/providers/openai.svg";
  import googleLogo from "$lib/assets/providers/google.svg";
  import xaiLogo from "$lib/assets/providers/xai.svg";
  import metaLogo from "$lib/assets/providers/meta.svg";
  import mistralLogo from "$lib/assets/providers/mistral.svg";
  import deepseekLogo from "$lib/assets/providers/deepseek.svg";
  import alibabaLogo from "$lib/assets/providers/alibaba.svg";
  import moonshotLogo from "$lib/assets/providers/moonshotai.svg";
  import zhipuLogo from "$lib/assets/providers/zhipuai.svg";
  import minimaxLogo from "$lib/assets/providers/minimax.svg";
  import volcengineLogo from "$lib/assets/providers/volcengine.svg";
  import tencentLogo from "$lib/assets/providers/tencent-tokenhub.svg";
  import stepfunLogo from "$lib/assets/providers/stepfun.svg";
  import xiaomiLogo from "$lib/assets/providers/xiaomi.svg";

  /** 目录只负责筛选与选择；创建仍由父级通过真实 Go 服务完成。 */
  let {
    catalog,
    loading,
    busy,
    error,
    onretry,
    onselect,
  }: {
    catalog: CatalogEntry[];
    loading: boolean;
    busy: boolean;
    error: string;
    onretry: () => void;
    onselect: (entry: CatalogEntry) => void;
  } = $props();
  /**
   * 厂商条只列热门官方厂商，顺序即展示顺序：id 为 LiteLLM 供应商标识，label 为界面名称，logo 为本地品牌图标。
   * 未列出的来源不在厂商条出现，仍可通过搜索选到。
   */
  const officialVendors = [
    { id: "anthropic", label: "Anthropic", logo: anthropicLogo },
    { id: "openai", label: "OpenAI", logo: openaiLogo },
    { id: "google", label: "Google", logo: googleLogo },
    { id: "xai", label: "xAI", logo: xaiLogo },
    { id: "meta", label: "Meta", logo: metaLogo },
    { id: "mistral", label: "Mistral", logo: mistralLogo },
    { id: "deepseek", label: "DeepSeek", logo: deepseekLogo },
    { id: "alibaba", label: "阿里通义", logo: alibabaLogo },
    { id: "moonshotai", label: "月之暗面", logo: moonshotLogo },
    { id: "zhipuai", label: "智谱", logo: zhipuLogo },
    { id: "minimax", label: "MiniMax", logo: minimaxLogo },
    { id: "volcengine", label: "火山方舟", logo: volcengineLogo },
    { id: "tencent-tokenhub", label: "腾讯混元", logo: tencentLogo },
    { id: "stepfun", label: "阶跃星辰", logo: stepfunLogo },
    { id: "xiaomi", label: "小米", logo: xiaomiLogo },
  ];
  /** 搜索词、所选厂商（空值表示尚未选择）与类型筛选；类型筛选始终生效。 */
  let query = $state(""),
    provider = $state(""),
    kind = $state("all");
  /** 卡片滚动区，筛选变化后回到顶部。 */
  let grid = $state<HTMLDivElement | null>(null);
  /** 当前目录中实际存在的官方厂商及其模型数量。 */
  const vendors = $derived.by(() => {
    const counts = new Map<string, number>();
    for (const entry of catalog)
      counts.set(entry.providerId, (counts.get(entry.providerId) ?? 0) + 1);
    return officialVendors
      .filter((vendor) => counts.has(vendor.id))
      .map((vendor) => ({ ...vendor, count: counts.get(vendor.id) ?? 0 }));
  });
  /** 未选择时默认第一家官方厂商；all 表示名单内全部厂商。 */
  const current = $derived(provider || vendors[0]?.id || "all");
  /** 有搜索词时跨全部来源匹配，否则按所选官方厂商浏览。 */
  const matches = $derived.by(() => {
    const scoped = query.trim()
      ? catalog
      : catalog.filter((entry) =>
          current === "all"
            ? !vendors.length || vendors.some((vendor) => vendor.id === entry.providerId)
            : entry.providerId === current,
        );
    return searchCatalogEntries(scoped, query, kind, kinds);
  });
  /** 选择厂商即回到按厂商浏览，同时清空跨来源搜索词。 */
  function pick(id: string): void {
    provider = id;
    query = "";
  }
  /** 提供商与模型 ID 共同定位目录条目，避免同名来源互相选中。 */
  function key(entry: CatalogEntry): string {
    return `${entry.providerId}\u0000${entry.modelId}`;
  }

  $effect(() => {
    void [query, current, kind];
    grid?.scrollTo({ top: 0 });
  });
</script>

<div class="catalog-dialog">
  <div class="catalog-filters">
    <InputGroup.Root
      ><InputGroup.Addon><Search /></InputGroup.Addon><InputGroup.Input
        aria-label="搜索在线模型"
        placeholder="搜索模型、ID 或提供商（覆盖全部来源）"
        bind:value={query}
      /></InputGroup.Root
    >
    <Select.Root type="single" bind:value={kind}
      ><Select.Trigger class="w-32" aria-label="目录模型类型"
        >{kinds[kind] ?? "全部类型"}</Select.Trigger
      ><Select.Content
        ><Select.Group
          ><Select.Item value="all">全部类型</Select.Item
          >{#each Object.entries(kinds) as [value, label]}<Select.Item {value}
              >{label}</Select.Item
            >{/each}</Select.Group
        ></Select.Content
      ></Select.Root
    >
  </div>
  {#if error}<Alert.Root variant="destructive"><Alert.Description>{error}</Alert.Description></Alert.Root>{/if}
  {#if loading}<Empty.Root role="status">正在加载在线目录…</Empty.Root>
  {:else if !catalog.length}<Empty.Root>
      <Empty.Description>在线目录尚未加载</Empty.Description>
      <Button variant="outline" onclick={onretry}>重新加载目录</Button>
    </Empty.Root>
  {:else}
    {#if vendors.length}<ToggleGroup.Root type="single" value={query.trim() ? "" : current} onValueChange={(next) => { if(next) pick(next); }} variant="segmented" size="sm" spacing={1} class="provider-nav" aria-label="按官方厂商筛选">
        {#each [{ id: "all", label: "全部", logo: "", count: vendors.reduce((sum, vendor) => sum + vendor.count, 0) }, ...vendors] as vendor (vendor.id)}
          <ToggleGroup.Item value={vendor.id}
            >{#if vendor.logo}<span
                class="provider-logo"
                style:--provider-logo={`url("${vendor.logo}")`}
                aria-hidden="true"
              ></span>{:else}<LayoutGrid aria-hidden="true" />{/if}<span
              >{vendor.label}</span><small>{vendor.count}</small></ToggleGroup.Item
          >
        {/each}
      </ToggleGroup.Root>{/if}
    <div class="result-caption">
      <span
        >{query.trim()
          ? "搜索结果 · 全部来源"
          : (vendors.find((vendor) => vendor.id === current)?.label ??
            (vendors.length ? "全部官方厂商" : "全部来源"))}</span
      ><span>{matches.length} 个模型</span>
    </div>
    <div class="catalog-grid scroll-quiet" bind:this={grid}>
      {#each matches.slice(0, 80) as entry (key(entry))}
        <Button
          variant="outline"
          size="catalog" class="catalog-option"
          disabled={busy || !!entry.problem}
          aria-label={`选择 ${entry.name}，来源 ${entry.providerName}`}
          onclick={() => onselect(entry)}
        >
          <div class="catalog-option-head">
            <span
              class="model-brand"
              data-brand={entry.canonicalId.split("/")[0]}
              aria-hidden="true"
              >{entry.providerName.slice(0, 1).toUpperCase()}</span
            >
            <div>
              <strong>{entry.name}</strong><span
                >{entry.providerName} · {kinds[entry.kind]} · 上下文 {entry.context
                  ? `${entry.context.toLocaleString()} Token`
                  : "未知"}</span
              ><Hint overflowOnly text={entry.modelId}>{#snippet children(hintProps0)}<code {...hintProps0} >{entry.modelId}</code>{/snippet}</Hint>
            </div>
            {#if entry.official}<Badge variant="secondary">官方</Badge>{/if}
          </div>
          <ModelCapabilities values={entry.capabilities} />
          {#if entry.warnings?.length}<Hint text={entry.warnings.join("；")}>{#snippet children(warningProps)}<span {...warningProps}><Badge variant="neutral">部分规则未导入</Badge></span>{/snippet}</Hint>{/if}
          {#if entry.problem}<p class="text-destructive">
              {entry.problem}
            </p>{:else}<PriceSummary
              pricing={entry.pricing}
              kind={entry.kind}
            />{/if}
        </Button>
      {/each}
      {#if !matches.length}<Empty.Root>
          <Empty.Title>没有匹配的模型</Empty.Title><span>尝试其他关键词、厂商或类型。</span>
        </Empty.Root>{/if}
      {#if matches.length > 80}<p class="catalog-more">
          显示前 80 项，请输入关键词缩小范围。
        </p>{/if}
    </div>
  {/if}
</div>

<style>
  /* 搜索与厂商条固定，只有卡片区随滚轮滚动。 */
  .catalog-dialog {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .catalog-filters {
    display: flex;
    align-items: center;
    gap: 8px;
    /* 与原型一致：筛选组保持紧凑，搜索框不随宽屏弹窗无限拉长。 */
    width: min(490px, 100%);
    flex-shrink: 0;
  }
  .catalog-filters > :global([data-slot="input-group"]) {
    flex: 1;
    min-width: 0;
  }
  :global(.provider-nav) {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 4px;
    flex-shrink: 0;
  }

  :global(.provider-nav) span {
    white-space: nowrap;
  }
  /* 单色品牌图标跟随文字颜色，选中态与深色外观无需额外下载资源。 */
  .provider-logo {
    width: 16px;
    height: 16px;
    flex-shrink: 0;
    background-color: currentColor;
    mask: var(--provider-logo) center / contain no-repeat;
    -webkit-mask: var(--provider-logo) center / contain no-repeat;
  }
  :global(.provider-nav) small {
    color: var(--muted-foreground);
    font-size: 11px;
  }
  .result-caption {
    display: flex;
    justify-content: space-between;
    flex-shrink: 0;
    padding-top: 10px;
    border-top: 1px solid var(--border);
    color: var(--muted-foreground);
    font-size: 11px;
  }
  .catalog-grid {
    flex: 1;
    min-height: 0;
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    align-content: start;
    gap: 10px;
    overflow-y: auto;
    padding: 2px 2px 24px;
  }

  .catalog-option-head {
    display: flex;
    align-items: center;
    gap: 9px;
    min-width: 0;
  }
  .catalog-option-head .model-brand {
    width: 30px;
    height: 30px;
    font-size: 16px;
  }
  .catalog-option-head > div {
    flex: 1;
    min-width: 0;
  }
  .catalog-option-head strong {
    display: block;
    font-size: 12px;
    overflow-wrap: anywhere;
  }
  .catalog-option-head span:not(.model-brand),
  .catalog-option-head code {
    display: block;
    margin-top: 2px;
    font-size: 10px;
    color: var(--muted-foreground);
  }
  .catalog-option-head code {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .catalog-grid :global(.model-capabilities) {
    max-width: none;
    gap: 4px;
  }
  .catalog-grid :global(.model-price) {
    margin-top: auto;
    border-top: 1px solid var(--border);
    padding-top: 9px;
  }
  .catalog-grid :global(.price-amount strong) {
    font-size: 13px;
  }
  .catalog-grid :global(.price-basis) {
    font-size: 10px;
  }
  .catalog-grid :global(.price-traits) {
    gap: 4px;
  }

  .catalog-more {
    grid-column: 1 / -1;
    font-size: 11px;
    color: var(--muted-foreground);
    padding: 8px;
  }
  @media (max-width: 640px) {
    .catalog-grid {
      grid-template-columns: 1fr;
    }
  }
</style>
