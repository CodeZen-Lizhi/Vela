<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { onDestroy, tick } from "svelte";
  import { flip } from "svelte/animate";
  import { prefersReducedMotion } from "svelte/motion";
  import { ListOrdered, ShieldAlert } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import { VelaService, type ProviderOverview, type ProviderHealthState } from "$lib/generated/mygo";
  import { errorMessage, type Notify } from "$lib/settings/sections";
  import {
    clampDragOffset,
    dragTargetIndex,
    neighborWaveOffset,
    settleTiming,
    slotShiftForRow,
    type SortRowGeometry,
  } from "$lib/ordering/wave-sort";

  /** 供应商顺序编辑器：整行拖动或方向键移动，保存失败恢复上次成功顺序。 */
  let { providers, notify }: {
    /** 后端快照中的全部供应商，包含停用项，已按保存顺序排列。 */
    providers: ProviderOverview[];
    /** 顺序保存成功后的轻量反馈。 */
    notify: Notify;
  } = $props();

  /** 健康分档只翻译后端结果，不根据百分比重新推导。 */
  const healthLabels: Record<ProviderHealthState, string> = {
    normal: "正常", degraded: "降级", abnormal: "异常", circuit: "熔断", unavailable: "不可用",
  };
  /** 键盘换位缓出曲线：快速离开原位，末尾仅轻微回弹约 2%。 */
  function springSettle(t: number): number {
    if (t <= 0) return 0;
    if (t >= 1) return 1;
    return 1 - Math.exp(-9 * t) * Math.cos(5.5 * t);
  }
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
  /** 拖动会话只记录纵向位移；真实行留在列表流中并直接跟手移动。 */
  type DragSession = {
    /** 拖动供应商的稳定身份。 */
    id: string;
    /** 发起本次拖动的指针。 */
    pointerId: number;
    /** 拖动开始前的完整顺序。 */
    before: ProviderOverview[];
    /** 原行及列表在拖动开始时的纵向布局，单位为像素。 */
    geometry: SortRowGeometry[];
    /** 拖动开始时的指针纵坐标，单位为窗口像素。 */
    startY: number;
    /** 当前相对原位的纵向位移，单位为像素。 */
    offset: number;
    /** 最近一次位移和近似甩动速度，用于松手落位时长。 */
    lastOffset: number; flick: number;
  };
  let drag = $state<DragSession | null>(null);
  /** 松手落位的供应商身份；保留到过渡结束后再允许下一次拖动。 */
  let settlingID = $state("");
  /** 本次落位的时长与曲线，由松手前的近似速度决定。 */
  let settleDuration = $state(140);
  let settleEase = $state("cubic-bezier(0.25, 0.8, 0.3, 1)");
  /** 指针更新合并到下一绘制帧，避免高频 pointermove 造成重复布局与抖动。 */
  let pointerFrame = 0;
  let pendingPointerY: number | null = null;
  /** 落位清理定时器，取消或卸载时统一释放。 */
  let settleTimer = 0;

  /** 当前目标槽位由被拖行中心与原始行中心共同计算，支持高度不同的供应商行。 */
  const dragTarget = $derived(drag
    ? dragTargetIndex(drag.geometry, ordered.findIndex((item) => item.provider.id === drag?.id), drag.offset)
    : -1);
  /** 目标顺序只用于拖动中的序号显示，实际列表在松手后才提交。 */
  const visualOrder = $derived.by(() => {
    if (!drag || dragTarget < 0) return ordered.map((item) => item.provider.id);
    const ids = ordered.map((item) => item.provider.id);
    const source = ids.indexOf(drag.id);
    if (source < 0 || source === dragTarget) return ids;
    const [id] = ids.splice(source, 1);
    if (id) ids.splice(dragTarget, 0, id);
    return ids;
  });

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
      clearSettle();
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

  /** 返回供应商行在当前拖动帧中的位移与过渡；静止时完全恢复列表原样。 */
  function rowMotion(index: number, id: string): { transform: string; transition: string } {
    if (!drag) {
      if (settlingID !== id) return { transform: "", transition: "" };
      return {
        transform: "translate3d(0, 0, 0) scale(1)",
        transition: `transform ${settleDuration}ms ${settleEase}, box-shadow 130ms ease`,
      };
    }

    const source = ordered.findIndex((item) => item.provider.id === drag?.id);
    const target = dragTarget;
    const active = source === index;
    const followed = active && Math.abs(drag.offset) > 0.5;
    if (active) {
      return {
        transform: `translate3d(0, ${drag.offset.toFixed(1)}px, 0) scale(${followed ? "1.018" : "1"})`,
        transition: settlingID === id && !prefersReducedMotion.current
          ? `transform ${settleDuration}ms ${settleEase}, box-shadow 130ms ease`
          : "none",
      };
    }

    const sourceGeometry = drag.geometry[source];
    const nextGeometry = drag.geometry[source + 1];
    const pitch = sourceGeometry
      ? sourceGeometry.height + Math.max(0, (nextGeometry?.top ?? sourceGeometry.top + sourceGeometry.height) - sourceGeometry.top - sourceGeometry.height)
      : 0;
    const shifted = slotShiftForRow(source, target, index, pitch);
    const wave = shifted === 0 ? neighborWaveOffset(source, index, drag.offset) : { value: 0, delay: 0 };
    const y = shifted + wave.value;
    return {
      transform: y === 0 ? "" : `translate3d(0, ${y.toFixed(1)}px, 0)`,
      transition: prefersReducedMotion.current
        ? "none"
        : `transform 130ms cubic-bezier(0.25, 0.8, 0.3, 1) ${wave.delay}ms, box-shadow 130ms ease`,
    };
  }

  /** 上下方向键每次移动一位并保存；Escape 取消正在进行的拖动。 */
  function keydown(event: KeyboardEvent, id: string): void {
    if (event.key === "Escape" && drag) { event.preventDefault(); cancel(); return; }
    if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return;
    event.preventDefault();
    if (saving || drag || settlingID) return;
    const before = [...ordered];
    const source = ordered.findIndex((item) => item.provider.id === id);
    move(id, source + (event.key === "ArrowUp" ? -1 : 1));
    void persist(before, id);
  }

  /** 按住整行捕获指针；记录原始布局后直接移动真实行。 */
  function start(event: PointerEvent, id: string): void {
    if (event.button !== 0 || !event.isPrimary || saving || drag || settlingID) return;
    event.preventDefault();
    const row = event.currentTarget as HTMLButtonElement;
    row.focus({ preventScroll: true });
    const listRect = list.getBoundingClientRect();
    const geometry = Array.from(list.querySelectorAll<HTMLElement>(":scope > [data-order-slot]"))
      .map((slot) => {
        const rect = slot.getBoundingClientRect();
        return { top: rect.top - listRect.top, height: rect.height };
      });
    drag = {
      id,
      pointerId: event.pointerId,
      before: [...ordered],
      geometry,
      startY: event.clientY,
      offset: 0,
      lastOffset: 0,
      flick: 0,
    };
    list.setPointerCapture(event.pointerId);
  }

  /** 指针越过相邻行的布局中线才让位；使用布局坐标避免 FLIP 动画导致反复换位。 */
  function pointermove(event: PointerEvent): void {
    if (!drag || event.pointerId !== drag.pointerId) return;
    event.preventDefault();
    pendingPointerY = event.clientY;
    if (!pointerFrame) pointerFrame = requestAnimationFrame(applyPointerFrame);
  }

  /** 每帧只读取一次布局并更新落点，指针位置和列表让位共享同一绘制节奏。 */
  function applyPointerFrame(): void {
    pointerFrame = 0;
    const pointerY = pendingPointerY;
    pendingPointerY = null;
    if (!drag || pointerY === null) return;
    const source = ordered.findIndex((item) => item.provider.id === drag?.id);
    const next = clampDragOffset(drag.geometry, source, pointerY - drag.startY);
    drag.flick = drag.flick * 0.55 + Math.abs(next - drag.lastOffset) * 0.45;
    drag.lastOffset = next;
    drag.offset = next;
  }

  /** 在松手前补结算最后一帧，避免快速抬手时漏掉最后一个落点。 */
  function flushPointerFrame(): void {
    if (pointerFrame) cancelAnimationFrame(pointerFrame);
    pointerFrame = 0;
    applyPointerFrame();
  }

  /** 清理尚未执行的指针帧，取消拖动或卸载时不再更新旧会话。 */
  function clearPointerFrame(): void {
    if (pointerFrame) cancelAnimationFrame(pointerFrame);
    pointerFrame = 0;
    pendingPointerY = null;
  }

  /** 清理松手落位定时器，保存失败或组件卸载时立即结束过渡状态。 */
  function clearSettle(): void {
    if (settleTimer) clearTimeout(settleTimer);
    settleTimer = 0;
    settlingID = "";
  }

  /** 开始一次带速度的落位过渡；真实 DOM 在下一帧重排，让当前画面先保留到过渡属性生效。 */
  function beginSettle(id: string, flick: number): void {
    const timing = settleTiming(flick);
    settlingID = id;
    settleDuration = prefersReducedMotion.current ? 0 : Math.round(timing.duration);
    settleEase = timing.easing;
    if (settleTimer) clearTimeout(settleTimer);
    settleTimer = window.setTimeout(() => {
      settleTimer = 0;
      settlingID = "";
    }, settleDuration + 40);
  }

  /** 结束指针捕获；先清空会话，避免释放捕获事件误触发取消。 */
  function release(): void {
    const pointerId = drag?.pointerId;
    clearPointerFrame();
    drag = null;
    if (pointerId !== undefined && list.hasPointerCapture(pointerId)) list.releasePointerCapture(pointerId);
  }

  /** 松手才保存；先让落位过渡属性生效，再重排真实列表并保存。 */
  async function pointerup(event: PointerEvent): Promise<void> {
    if (!drag || drag.pointerId !== event.pointerId) return;
    flushPointerFrame();
    const session = drag;
    const target = dragTarget;
    const moved = Math.abs(session.offset) > 3;
    const before = session.before;
    const id = session.id;

    if (!moved) {
      release();
      void retainFocus(id);
      return;
    }

    beginSettle(id, session.flick);
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    const source = ordered.findIndex((item) => item.provider.id === id);
    if (source >= 0 && target >= 0 && source !== target) {
      const next = [...ordered];
      const [item] = next.splice(source, 1);
      if (item) next.splice(target, 0, item);
      ordered = next;
    }
    release();
    await tick();
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

  onDestroy(() => {
    clearPointerFrame();
    clearSettle();
    if (drag) release();
  });
</script>

<svelte:window onpointermove={pointermove} onpointerup={pointerup} onpointercancel={cancel} onblur={() => cancel()} />

{#snippet providerRowContents(item: ProviderOverview, index: number)}
  <span class="order-index">{String(index + 1).padStart(2, "0")}</span>
  <span class="provider-mark" aria-hidden="true">{item.provider.name.slice(0, 1)}</span>
  <span class="order-copy">
    <strong>{item.provider.name}</strong>
    <span class="health" data-state={item.health.state}>
      <Badge variant={item.health.state === "normal" ? "positive" : item.health.state === "degraded" ? "warning" : item.health.state === "abnormal" || item.health.state === "circuit" ? "negative" : "neutral"}>
        {#if item.health.state === "circuit"}<ShieldAlert aria-hidden="true" />{/if}{healthLabels[item.health.state]}
      </Badge>
    </span>
    <Badge variant={item.provider.enabled ? "outline" : "secondary"}>{item.provider.enabled ? "启用" : "停用"}</Badge>
  </span>
{/snippet}

<section class="overflow-hidden rounded-xl border bg-card" aria-labelledby="provider-order-title" aria-busy={saving}>
  <header class="border-b px-5 py-4">
    <h2 id="provider-order-title" class="text-sm font-semibold">供应商优先顺序</h2>
    <p id="provider-order-help" class="mt-1 text-xs text-muted-foreground">
      按住任一行上下拖动，或聚焦后按上下方向键调整，第 01 位最优先。
    </p>
  </header>
  {#if saveError}<Alert.Root variant="destructive"><Alert.Description>供应商顺序保存失败，已恢复原顺序：{saveError}</Alert.Description></Alert.Root>{/if}
  <div class="order-list" bind:this={list} role="list" aria-label="供应商优先顺序" onlostpointercapture={cancel}>
    {#each ordered as item, index (item.provider.id)}
      {@const motion = rowMotion(index, item.provider.id)}
      <div role="listitem" class="order-slot" data-order-slot
        data-lifted={drag?.id === item.provider.id && Math.abs(drag.offset) > 0.5}
        data-settling={settlingID === item.provider.id}
        style:transform={motion.transform}
        style:transition={motion.transition}
        animate:flip={{ duration: prefersReducedMotion.current || settlingID ? 0 : 240, easing: springSettle }}>
        <Button variant="row" size="row"
          class="order-row"
          data-order-id={item.provider.id}
          data-inactive={!item.provider.enabled}
          data-lifted={drag?.id === item.provider.id && Math.abs(drag.offset) > 0.5}
          aria-disabled={saving}
          aria-describedby="provider-order-help"
          aria-label={`${item.provider.name}，健康${healthLabels[item.health.state]}${item.health.reason ? `，${item.health.reason}` : ''}，${item.provider.enabled ? '启用' : '停用'}，第 ${index + 1} 位，共 ${ordered.length} 位`}
          onpointerdown={(event) => start(event, item.provider.id)}
          onkeydown={(event) => keydown(event, item.provider.id)}>
          {@render providerRowContents(item, visualOrder.indexOf(item.provider.id))}
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
  .order-slot {
    position: relative;
    z-index: 1;
    border-bottom: 1px solid var(--border);
    transform-origin: 50% 50%;
    will-change: transform;
  }
  .order-slot:last-child { border-bottom: 0; }
  .order-slot[data-lifted="true"] {
    z-index: 3;
  }
  .order-slot[data-settling="true"] {
    z-index: 2;
  }
  .order-list :global(.order-row) {
    cursor: grab;
    touch-action: none;
  }
  .order-list :global(.order-row[data-lifted="true"]) {
    cursor: grabbing;
    border-radius: var(--radius-lg);
    background: var(--card);
    box-shadow:
      0 14px 26px color-mix(in srgb, var(--foreground) 13%, transparent),
      0 3px 8px color-mix(in srgb, var(--foreground) 5%, transparent);
  }
  .order-index { flex-shrink: 0; width: 22px; color: var(--muted-foreground); font-size: 11px; font-variant-numeric: tabular-nums; }
  .provider-mark { display: grid; place-content: center; flex-shrink: 0; width: 30px; height: 30px; border: 1px solid var(--border); border-radius: 8px; background: var(--muted); color: var(--primary); font-family: Georgia, serif; }
  .order-copy { display: flex; align-items: center; flex-wrap: wrap; gap: 7px; min-width: 0; }
  .order-copy strong { font-size: 13px; font-weight: 600; overflow-wrap: anywhere; }
  .health { display: inline-flex; }
  @media (prefers-reduced-motion: reduce) {
    .order-slot,
    .order-list :global(.order-row) { transition: none; }
  }
</style>
