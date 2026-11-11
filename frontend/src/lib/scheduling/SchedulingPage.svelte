<script lang="ts">
  import { onMount } from "svelte";
  import { isMyGo } from "mygo-runtime";
  import {
    Check,
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
  <div>
    <h1 class="text-2xl font-semibold tracking-tight">调度</h1>
    <p class="mt-2 text-sm text-muted-foreground">
      选择调度策略并调整供应商优先顺序。
    </p>
  </div>
  {#if !desktop}<p class="feedback">
      浏览器预览 · 调度策略的读取与保存请在 Vela 桌面窗口中进行。
    </p>{/if}
  {#if loadError}<div role="alert" class="error-banner">
      调度配置读取失败：{loadError}<Button
        variant="ghost"
        size="sm"
        onclick={() => void load()}>重试</Button
      >
    </div>{/if}
  {#if saveError}<p role="alert" class="error-banner">
      调度策略保存失败，仍为原策略：{saveError}
    </p>{/if}

  <section class="overflow-hidden rounded-xl border bg-card" aria-label="调度策略">
    {#if desktop && !loadError && selected === null}
      <p class="p-16 text-center text-sm text-muted-foreground">
        正在读取调度策略…
      </p>
    {:else}
      <div class="strategy-list">
        {#each strategies as item (item.value)}
          <button
            type="button"
            class="strategy-option"
            class:chosen={selected === item.value}
            aria-pressed={selected === item.value}
            disabled={!desktop || saved === null}
            onclick={() => void choose(item)}
          >
            <span class="strategy-icon"><item.icon class="size-4" /></span>
            <span class="min-w-0 flex-1">
              <strong>{item.name}</strong>
              <span class="strategy-description">{item.description}</span>
              <small>{item.scenario}</small>
            </span>
            <span class="strategy-check" aria-hidden="true"
              >{#if selected === item.value}<Check class="size-3" />{/if}</span
            >
          </button>
        {/each}
      </div>
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
  .strategy-list {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 12px;
    padding: 16px;
  }
  .strategy-option {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    min-width: 0;
    padding: 16px 14px;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--background);
    text-align: left;
    cursor: pointer;
  }
  .strategy-option:hover:not(:disabled) {
    border-color: color-mix(in srgb, var(--primary) 35%, var(--border));
    background: var(--muted);
  }
  .strategy-option:focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 2px;
  }
  .strategy-option.chosen {
    border-color: color-mix(in srgb, var(--primary) 60%, var(--border));
    background: var(--accent);
  }
  .strategy-option:disabled {
    cursor: default;
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
  .strategy-option.chosen .strategy-icon {
    background: var(--card);
    color: var(--primary);
  }
  .strategy-option strong {
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
  .strategy-option small {
    display: block;
    margin-top: 8px;
    color: var(--muted-foreground);
    font-size: 11px;
    opacity: 0.8;
  }
  .strategy-check {
    display: grid;
    place-content: center;
    width: 18px;
    height: 18px;
    flex-shrink: 0;
    border: 1px solid var(--input);
    border-radius: 50%;
  }
  .strategy-option.chosen .strategy-check {
    border-color: var(--primary);
    background: var(--primary);
    color: var(--primary-foreground);
  }
  @media (max-width: 900px) {
    .strategy-list {
      grid-template-columns: 1fr;
    }
  }
</style>
