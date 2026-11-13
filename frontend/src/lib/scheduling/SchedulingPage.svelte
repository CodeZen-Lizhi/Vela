<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import * as ToggleGroup from "$lib/components/ui/toggle-group";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import { onMount } from "svelte";
  import { isMyGo } from "mygo-runtime";
  import {
    CircleDollarSign,
    ListOrdered,
    Route,
  } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import ProviderOrder from "./ProviderOrder.svelte";
  import { VelaService, type ProviderOverview, type SchedulingStrategy } from "$lib/generated/mygo";
  import { errorMessage, type Notify } from "$lib/settings/sections";

  /** 调度页：选择全局调度策略并查看供应商优先顺序；点击策略即保存，失败时回到上次成功保存的策略。 */
  let {
    notify,
  }: {
    /** 保存成功后的轻量反馈。 */
    notify: Notify;
  } = $props();

  /** 三种策略的取值、名称、行为说明与适用场景；取值与 Go 侧一致，说明须与网关的实际排序一致。 */
  const strategies: {
    value: SchedulingStrategy;
    name: string;
    icon: typeof Route;
    description: string;
    scenario: string;
  }[] = [
    {
      value: "cache_affinity",
      name: "缓存亲和",
      icon: Route,
      description:
        "同一会话优先沿用上次成功的上游，提高提示缓存命中；没有可沿用的上游时按供应商顺序。",
      scenario: "连续对话与 Agent 任务",
    },
    {
      value: "fixed_order",
      name: "固定顺序",
      icon: ListOrdered,
      description: "始终按供应商顺序主备，不做会话亲和。",
      scenario: "明确偏好某条稳定线路",
    },
    {
      value: "cost_based",
      name: "成本优先",
      icon: CircleDollarSign,
      description: "兼容候选中 Key 倍率低的优先；倍率在供应商的 Key 配置中维护。",
      scenario: "高频轻量任务与成本敏感调用",
    },
  ];

  /** 是否运行在 Vela 桌面窗口内；浏览器预览不伪造策略或供应商数据。 */
  const desktop = isMyGo();
  /** saved 是最后一次成功读取或保存的策略，null 表示尚未读到；pending 是正在保存的策略。 */
  let saved = $state<SchedulingStrategy | null>(null);
  let pending = $state<SchedulingStrategy | null>(null);
  /** 快照中的全部供应商，包含停用项；null 表示尚未读取。 */
  let providers = $state<ProviderOverview[] | null>(null);
  /** loadError、saveError 分别是读取与最近一次保存失败的原因，空字符串表示没有。 */
  let loadError = $state("");
  let saveError = $state("");
  /** 界面上显示为选中的策略：保存进行中显示所选项，否则显示已保存值。 */
  const selected = $derived(pending ?? saved);

  /** 同一次快照读取策略与全部供应商；失败时提供重试，不把失败显示为空列表。 */
  async function load(): Promise<void> {
    if (!desktop) return;
    loadError = "";
    try {
      const snapshot = await VelaService.schedulingSnapshot();
      saved = snapshot.policy.strategy;
      providers = snapshot.providers;
    } catch (cause) {
      loadError = errorMessage(cause);
    }
  }

  /** 点击即保存所选策略；成功后才更新已保存值，失败时选中项回到上次成功的策略并显示原因。 */
  async function choose(item: (typeof strategies)[number]): Promise<void> {
    if (pending || saved === null || item.value === saved) return;
    pending = item.value;
    saveError = "";
    try {
      await VelaService.saveSchedulingStrategy(item.value);
      saved = item.value;
      notify(`已切换为${item.name}`);
    } catch (cause) {
      saveError = errorMessage(cause);
    } finally {
      pending = null;
    }
  }

  onMount(() => {
    void load();
  });
</script>

<div class="flex flex-col gap-5">
  <PageHeader title="调度" description="选择调度策略并调整供应商优先顺序。" />
  {#if !desktop}<Alert.Root variant="info"><Alert.Description>
      浏览器预览 · 调度策略的读取与保存请在 Vela 桌面窗口中进行。
    </Alert.Description></Alert.Root>{/if}
  {#if loadError}<Alert.Root variant="destructive"><Alert.Description>
      调度配置读取失败：{loadError}<Button
        variant="ghost"
        size="sm"
        onclick={() => void load()}>重试</Button
      >
    </Alert.Description></Alert.Root>{/if}
  {#if saveError}<Alert.Root variant="destructive"><Alert.Description>
      调度策略保存失败，仍为原策略：{saveError}
    </Alert.Description></Alert.Root>{/if}

  <section class="overflow-hidden rounded-xl border bg-card" aria-label="调度策略">
    {#if desktop && !loadError && selected === null}
      <p class="p-16 text-center text-sm text-muted-foreground">
        正在读取调度策略…
      </p>
    {:else}
      <ToggleGroup.Root type="single" value={selected ?? ""} onValueChange={(next) => { const item = strategies.find((item) => item.value === next); if(item) void choose(item); }} variant="card" size="choice" spacing={3} class="strategy-list" aria-label="调度策略" disabled={!desktop || saved === null}>
        {#each strategies as item (item.value)}
          <ToggleGroup.Item value={item.value} class="strategy-option">

            <span class="strategy-icon"><item.icon class="size-4" /></span>
            <span class="strategy-copy min-w-0 flex-1">
              <strong>{item.name}</strong>
              <span class="strategy-description">{item.description}</span>
              <small>{item.scenario}</small>
            </span>
            <span class="strategy-check" data-selected={selected === item.value} aria-hidden="true"></span>
          </ToggleGroup.Item>
        {/each}
      </ToggleGroup.Root>
    {/if}
  </section>

  {#if providers !== null}
    <ProviderOrder {providers} {notify} />
  {:else if desktop && !loadError}
    <p class="text-sm text-muted-foreground">正在读取供应商顺序…</p>
  {/if}

  <p class="settings-note">
    调度策略已保存在本机；网关接入后按此策略调度。
  </p>
</div>

<style>
  :global(.strategy-list) {
    display: grid;
    width: 100%;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 12px;
    padding: 16px;
  }

  .strategy-icon {
    display: grid;
    place-content: center;
    width: 32px;
    height: 32px;
    flex-shrink: 0;
    border-radius: 8px;
    background: var(--secondary);
    color: var(--muted-foreground);
  }
  .strategy-check {
    display: grid;
    place-content: center;
    width: 18px;
    height: 18px;
    flex-shrink: 0;
    border: 1.5px solid color-mix(in srgb, var(--muted-foreground) 55%, transparent);
    border-radius: 999px;
    transition:
      border-color 180ms ease,
      box-shadow 180ms ease;
  }
  .strategy-check::after {
    width: 8px;
    height: 8px;
    border-radius: 999px;
    background: var(--primary);
    content: "";
    opacity: 0;
    transform: scale(0.35);
    transition:
      opacity 140ms ease,
      transform 200ms cubic-bezier(0.2, 0.8, 0.2, 1);
  }
  .strategy-check[data-selected="true"] {
    border-color: var(--primary);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--primary) 10%, transparent);
  }
  .strategy-check[data-selected="true"]::after {
    opacity: 1;
    transform: scale(1);
  }

  :global(.strategy-option) strong {
    display: block;
    font-size: 13px;
    font-weight: 600;
  }
  .strategy-description {
    display: block;
    margin-top: 6px;
    color: var(--muted-foreground);
    font-size: 12px;
    line-height: 1.6;
  }
  :global(.strategy-option) small {
    display: block;
    margin-top: 8px;
    color: var(--muted-foreground);
    font-size: 11px;
    opacity: 0.8;
  }

  /* 最小桌面窗口仍保留三列，图标与选中标记在上，说明使用完整卡片宽度。 */
  @media (max-width: 1100px) {
    :global(.strategy-option) {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      grid-template-rows: 32px 1fr;
      gap: 10px;
      padding: 14px 12px;
    }
    :global(.strategy-option > .strategy-copy) {
      grid-column: 1 / -1;
      grid-row: 2;
    }
    .strategy-check {
      grid-column: 2;
      grid-row: 1;
      align-self: center;
    }
  }
  @media (max-width: 600px) {
    :global(.strategy-list) {
      grid-template-columns: 1fr;
    }
  }
</style>
