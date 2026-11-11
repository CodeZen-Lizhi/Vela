<script lang="ts">
  import { onDestroy, tick } from "svelte";
  import { flip } from "svelte/animate";
  import { prefersReducedMotion } from "svelte/motion";
  import { ListOrdered } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import { VelaService, type ProviderOverview, type ProviderHealthState } from "$lib/generated/mygo";
  import { errorMessage, type Notify } from "$lib/settings/sections";

  /** 供应商顺序编辑器：整行拖动或方向键移动，保存失败恢复上次成功顺序。 */
  let { providers, notify }: {
    /** 后端快照中的全部供应商，包含停用项，已按保存顺序排列。 */
    providers: ProviderOverview[];
    /** 顺序保存成功后的轻量反馈。 */
    notify: Notify;
  } = $props();

  /** 健康分档只翻译后端结果，不根据百分比重新推导。 */
  const healthLabels: Record<ProviderHealthState, string> = {
    normal: "正常", degraded: "降级", abnormal: "异常", unavailable: "不可用",
  };
  /** 当前显示顺序；拖动期间为草稿，保存失败恢复。 */
  let ordered = $state<ProviderOverview[]>([]);
  /** 顺序保存期间禁止再次改序，但保留行焦点与 Tab 导航。 */
  let saving = $state(false);
  /** 最近一次顺序保存失败的原因。 */
  let saveError = $state("");
  /** 向辅助技术播报移动、保存及回退结果。 */
  let announcement = $state("");
  /** 列表作为稳定的指针捕获目标，行重排不会中断拖动。 */
  let list: HTMLDivElement;
  /** 拖动会话，记录原顺序与浮动行的窗口坐标，取消时恢复。 */
  let drag = $state<{
    /** 拖动供应商的稳定身份。 */
    id: string;
    /** 发起本次拖动的指针。 */
    pointerId: number;
    /** 拖动开始前的完整顺序。 */
    before: ProviderOverview[];
    /** 行在窗口中的初始左、上坐标与宽高，单位为像素。 */
    left: number; top: number; width: number; height: number;
    /** 起始及当前指针纵坐标，单位为窗口像素。 */
    startY: number; y: number;
  } | null>(null);

  $effect(() => { ordered = [...providers]; });

  /** 按身份恢复焦点；等待 DOM 重排后执行，不覆盖用户已经移走的焦点。 */
  async function retainFocus(id: string): Promise<void> {
    const focused = document.activeElement;
    if (!(focused instanceof HTMLElement) || focused.dataset.orderId !== id) return;
    await tick();
    if (document.activeElement !== focused && document.activeElement !== document.body) return;
    Array.from(list.querySelectorAll<HTMLButtonElement>("[data-order-id]"))
      .find((row) => row.dataset.orderId === id)?.focus({ preventScroll: true });
  }

  /** 仅对实际变化提交全部身份；失败恢复原顺序及仍停留在该行的焦点。 */
  async function persist(before: ProviderOverview[], id: string): Promise<void> {
    if (before.every((item, index) => item.provider.id === ordered[index]?.provider.id)) return;
    saving = true;
    saveError = "";
    try {
      await VelaService.saveSchedulingProviderOrder(ordered.map((item) => item.provider.id));
      announcement = "供应商顺序已保存";
      notify(announcement);
    } catch (cause) {
      ordered = before;
      saveError = errorMessage(cause);
      announcement = "保存失败，已恢复原顺序";
      void retainFocus(id);
    } finally {
      saving = false;
    }
  }

  /** 将供应商移到目标位置，保留身份键、焦点并播报新位置。 */
  function move(id: string, target: number): void {
    const next = [...ordered];
    const source = next.findIndex((item) => item.provider.id === id);
    if (source < 0 || target < 0 || target >= next.length || source === target) return;
    const [item] = next.splice(source, 1);
    if (!item) return;
    next.splice(target, 0, item);
    ordered = next;
    announcement = `${item.provider.name}，第 ${target + 1} 位，共 ${next.length} 位`;
    void retainFocus(id);
  }

  /** 上下方向键每次移动一位并保存；Escape 取消正在进行的拖动。 */
  function keydown(event: KeyboardEvent, id: string): void {
    if (event.key === "Escape" && drag) { event.preventDefault(); cancel(); return; }
    if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return;
    event.preventDefault();
    if (saving || drag) return;
    const before = [...ordered];
    const source = ordered.findIndex((item) => item.provider.id === id);
    move(id, source + (event.key === "ArrowUp" ? -1 : 1));
    void persist(before, id);
  }

  /** 按住整行开始拖动，捕获指针并将实际行浮起；占位行仍参与列表布局。 */
  function start(event: PointerEvent, id: string): void {
    if (event.button !== 0 || !event.isPrimary || saving || drag) return;
    event.preventDefault();
    const row = event.currentTarget as HTMLButtonElement;
    row.focus({ preventScroll: true });
    const rect = row.getBoundingClientRect();
    drag = { id, pointerId: event.pointerId, before: [...ordered],
      left: rect.left, top: rect.top, width: rect.width, height: rect.height,
      startY: event.clientY, y: event.clientY };
    list.setPointerCapture(event.pointerId);
  }

  /** 指针越过相邻行的布局中线才让位；使用布局坐标避免 FLIP 动画导致反复换位。 */
  function pointermove(event: PointerEvent): void {
    if (!drag || event.pointerId !== drag.pointerId) return;
    drag.y = event.clientY;
    const rows = Array.from(list.children) as HTMLElement[];
    const listTop = list.getBoundingClientRect().top;
    const source = ordered.findIndex((item) => item.provider.id === drag?.id);
    let target = source;
    for (const [index, row] of rows.entries()) {
      if (index === source) continue;
      const midpoint = listTop + row.offsetTop + row.offsetHeight / 2;
      if (index < source && event.clientY < midpoint) { target = index; break; }
      if (index > source && event.clientY > midpoint) target = index;
    }
    move(drag.id, target);
  }

  /** 结束指针捕获；先清空会话，避免释放捕获事件误触发取消。 */
  function release(): void {
    const pointerId = drag?.pointerId;
    drag = null;
    if (pointerId !== undefined && list.hasPointerCapture(pointerId)) list.releasePointerCapture(pointerId);
  }

  /** 松手才保存；回到起始位置或单击不会产生写入。 */
  function pointerup(event: PointerEvent): void {
    if (!drag || drag.pointerId !== event.pointerId) return;
    const { before, id } = drag;
    release();
    void retainFocus(id);
    void persist(before, id);
  }

  /** 指针取消、窗口失焦、Escape 或捕获丢失均回退，不保存草稿。 */
  function cancel(event?: PointerEvent): void {
    if (!drag || (event && event.pointerId !== drag.pointerId)) return;
    const { before, id } = drag;
    ordered = before;
    release();
    announcement = "已取消移动，恢复原顺序";
    void retainFocus(id);
  }

  onDestroy(() => { if (drag) release(); });
</script>

<svelte:window onpointermove={pointermove} onpointerup={pointerup} onpointercancel={cancel} onblur={() => cancel()} />

<section class="overflow-hidden rounded-xl border bg-card" aria-labelledby="provider-order-title" aria-busy={saving}>
  <header class="border-b px-5 py-4">
    <h2 id="provider-order-title" class="text-sm font-semibold">供应商优先顺序</h2>
    <p id="provider-order-help" class="mt-1 text-xs text-muted-foreground">
      按住任一行上下拖动，或聚焦后按上下方向键调整，第 01 位最优先。
    </p>
  </header>
  {#if saveError}<p role="alert" class="error-banner">供应商顺序保存失败，已恢复原顺序：{saveError}</p>{/if}
  <div class="order-list" bind:this={list} role="list" aria-label="供应商优先顺序" onlostpointercapture={cancel}>
    {#each ordered as item, index (item.provider.id)}
      <div role="listitem" class="order-slot" style:height={drag?.id === item.provider.id ? `${drag.height}px` : undefined}
        animate:flip={{ duration: prefersReducedMotion.current || drag?.id === item.provider.id ? 0 : 160 }}>
        <Button variant="ghost"
          class="order-row"
          data-order-id={item.provider.id}
          data-inactive={!item.provider.enabled}
          data-dragging={drag?.id === item.provider.id}
          aria-disabled={saving}
          aria-describedby="provider-order-help"
          aria-label={`${item.provider.name}，健康${healthLabels[item.health.state]}${item.health.percent === null ? '' : ` ${item.health.percent}%`}${item.health.reason ? `，${item.health.reason}` : ''}，${item.provider.enabled ? '启用' : '停用'}，第 ${index + 1} 位，共 ${ordered.length} 位`}
          style={drag?.id === item.provider.id ? `position:fixed;left:${drag.left}px;top:${drag.top + drag.y - drag.startY}px;width:${drag.width}px;z-index:50` : undefined}
          onpointerdown={(event) => start(event, item.provider.id)}
          onkeydown={(event) => keydown(event, item.provider.id)}>
          <span class="order-index">{String(index + 1).padStart(2, "0")}</span>
          <span class="provider-mark" aria-hidden="true">{item.provider.name.slice(0, 1)}</span>
          <span class="order-copy">
            <strong>{item.provider.name}</strong>
            <span class="health" data-state={item.health.state} title={item.health.reason || undefined}>
              <Badge variant={item.health.state === "abnormal" ? "destructive" : "secondary"}>
                {healthLabels[item.health.state]}{item.health.percent === null ? "" : ` ${item.health.percent}%`}
              </Badge>
            </span>
            <Badge variant={item.provider.enabled ? "outline" : "secondary"}>{item.provider.enabled ? "启用" : "停用"}</Badge>
          </span>
        </Button>
      </div>
    {/each}
  </div>
  {#if !ordered.length}
    <div class="flex flex-col items-center gap-2 px-6 py-14 text-center">
      <ListOrdered class="size-8 text-muted-foreground" />
      <h3 class="text-sm font-semibold">还没有供应商</h3>
      <p class="text-xs text-muted-foreground">添加供应商后即可设置调度顺序。</p>
    </div>
  {/if}
</section>
<p class="sr-only" role="status" aria-live="polite" aria-atomic="true">{announcement}</p>

<style>
  .order-list { position: relative; }
  .order-slot { border-bottom: 1px solid var(--border); }
  .order-slot:last-child { border-bottom: 0; }
  .order-list :global(.order-row) {
    display: flex; align-items: center; justify-content: flex-start; gap: 10px;
    width: 100%; height: auto; min-height: 58px; min-width: 0; padding: 12px 18px;
    border: 0; border-radius: 0; background: var(--card); text-align: left;
    white-space: normal; cursor: grab; touch-action: none; transition: background-color 140ms ease;
  }
  .order-list :global(.order-row:hover), .order-list :global(.order-row:focus-visible) { background: var(--muted); }
  .order-list :global(.order-row:focus-visible) { outline: 2px solid var(--ring); outline-offset: -2px; }
  .order-list :global(.order-row:active) { transform: none; }
  .order-list :global(.order-row[data-dragging="true"]) {
    cursor: grabbing; background: var(--accent); border-radius: 8px;
    box-shadow: 0 10px 28px -12px var(--foreground);
  }
  .order-list :global(.order-row[data-inactive="true"]) { opacity: .62; }
  .order-list :global(.order-row[aria-disabled="true"]) { cursor: wait; }
  .order-index { flex-shrink: 0; width: 22px; color: var(--muted-foreground); font-size: 11px; font-variant-numeric: tabular-nums; }
  .provider-mark { display: grid; place-content: center; flex-shrink: 0; width: 30px; height: 30px; border: 1px solid var(--border); border-radius: 8px; background: var(--muted); color: var(--primary); font-family: Georgia, serif; }
  .order-copy { display: flex; align-items: center; flex-wrap: wrap; gap: 7px; min-width: 0; }
  .order-copy strong { font-size: 13px; font-weight: 600; overflow-wrap: anywhere; }
  .health { display: inline-flex; }
  @media (max-width: 620px) { .order-list :global(.order-row) { padding: 11px 12px; gap: 8px; } }
  @media (prefers-reduced-motion: reduce) { .order-list :global(.order-row) { transition: none; } }
</style>
