<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
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
  /** 换位缓出曲线：快速离开原位，末尾仅轻微回弹约 2%，避免机械感。 */
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
  /** 拖动会话，记录原顺序、浮动预览起点与当前指针位置，取消时恢复。 */
  type DragSession = {
    /** 拖动供应商的稳定身份。 */
    id: string;
    /** 发起本次拖动的指针。 */
    pointerId: number;
    /** 拖动开始前的完整顺序。 */
    before: ProviderOverview[];
    /** 原行在窗口中的初始坐标与宽度，单位为像素。 */
    left: number; top: number; width: number;
    /** 起始及当前指针坐标，单位为窗口像素。 */
    startX: number; startY: number; x: number; y: number;
  };
  let drag = $state<DragSession | null>(null);
  /** 松手吸附阶段：保持预览到目标槽位，再与真实行交叉淡变。 */
  type SettleSession = {
    /** 正在落位的供应商身份。 */
    id: string;
    /** 原浮动行的窗口坐标、宽度与松手时的指针位置。 */
    left: number; top: number; width: number; x: number; y: number; startX: number; startY: number;
    /** 目标槽位的窗口坐标，单位为像素。 */
    targetLeft: number; targetTop: number;
    /** 是否已从松手位置切换到目标位置，用于触发 CSS 过渡。 */
    arrived: boolean;
    /** 是否开始淡出，供真实行同步淡入。 */
    leaving: boolean;
  };
  let settling = $state<SettleSession | null>(null);
  /** 指针更新合并到下一绘制帧，避免高频 pointermove 造成重复布局与抖动。 */
  let pointerFrame = 0;
  let pendingPointer: { x: number; y: number } | null = null;
  /** 落位过渡的淡出与清理定时器，取消或卸载时统一释放。 */
  let settleFrame = 0;
  let settleFadeTimer = 0;
  let settleDoneTimer = 0;
  /** 浮动预览对应的供应商与当前落位编号。 */
  const previewID = $derived(drag?.id ?? settling?.id ?? "");
  const dragItem = $derived(ordered.find((item) => item.provider.id === previewID));
  const dragIndex = $derived(ordered.findIndex((item) => item.provider.id === previewID));
  /** 松手淡出期间先把真实行内容放出来，与预览同步交叉过渡。 */
  const activeRowID = $derived(drag?.id ?? (settling && !settling.leaving ? settling.id : ""));
  /** 浮动预览跟随指针双向位移；正常动效下加入轻微倾斜和放大。 */
  const dragPreviewStyle = $derived.by(() => {
    const current = drag ?? settling;
    if (!current) return "";
    const dx = current.x - current.startX;
    const dy = current.y - current.startY;
    let transform: string;
    if (settling?.arrived) {
      const targetX = settling.targetLeft - settling.left;
      const targetY = settling.targetTop - settling.top;
      transform = `perspective(900px) translate3d(${targetX}px, ${targetY}px, 0) rotateX(0deg) rotateY(0deg) rotateZ(0deg) scale(1)`;
    } else if (!prefersReducedMotion.current) {
      transform = `perspective(900px) translate3d(${dx}px, ${dy}px, 0) rotateX(1.5deg) rotateY(${Math.max(-3, Math.min(3, dx * 0.02))}deg) rotateZ(${Math.max(-1.8, Math.min(1.8, dx * 0.012))}deg) scale(1.025)`;
    } else {
      transform = `translate3d(${dx}px, ${dy}px, 0)`;
    }
    return `left:${current.left}px;top:${current.top}px;width:${current.width}px;transform:${transform}`;
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

  /** 上下方向键每次移动一位并保存；Escape 取消正在进行的拖动。 */
  function keydown(event: KeyboardEvent, id: string): void {
    if (event.key === "Escape" && drag) { event.preventDefault(); cancel(); return; }
    if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return;
    event.preventDefault();
    if (saving || drag || settling) return;
    const before = [...ordered];
    const source = ordered.findIndex((item) => item.provider.id === id);
    move(id, source + (event.key === "ArrowUp" ? -1 : 1));
    void persist(before, id);
  }

  /** 按住整行捕获指针；真实行留在原位，拖动预览另行浮起。 */
  function start(event: PointerEvent, id: string): void {
    if (event.button !== 0 || !event.isPrimary || saving || drag || settling) return;
    event.preventDefault();
    const row = event.currentTarget as HTMLButtonElement;
    row.focus({ preventScroll: true });
    const rect = row.getBoundingClientRect();
    drag = { id, pointerId: event.pointerId, before: [...ordered],
      left: rect.left, top: rect.top, width: rect.width,
      startX: event.clientX, startY: event.clientY, x: event.clientX, y: event.clientY };
    list.setPointerCapture(event.pointerId);
  }

  /** 指针越过相邻行的布局中线才让位；使用布局坐标避免 FLIP 动画导致反复换位。 */
  function pointermove(event: PointerEvent): void {
    if (!drag || event.pointerId !== drag.pointerId) return;
    event.preventDefault();
    pendingPointer = { x: event.clientX, y: event.clientY };
    if (!pointerFrame) pointerFrame = requestAnimationFrame(applyPointerFrame);
  }

  /** 每帧只读取一次布局并更新落点，指针位置和列表让位共享同一绘制节奏。 */
  function applyPointerFrame(): void {
    pointerFrame = 0;
    const point = pendingPointer;
    pendingPointer = null;
    if (!drag || !point) return;
    drag.x = point.x;
    drag.y = point.y;
    const rows = Array.from(list.children) as HTMLElement[];
    const listTop = list.getBoundingClientRect().top;
    const source = ordered.findIndex((item) => item.provider.id === drag?.id);
    let target = source;
    for (const [index, row] of rows.entries()) {
      if (index === source) continue;
      const midpoint = listTop + row.offsetTop + row.offsetHeight / 2;
      if (index < source && point.y < midpoint) { target = index; break; }
      if (index > source && point.y > midpoint) target = index;
    }
    move(drag.id, target);
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
    pendingPointer = null;
  }

  /** 清理松手吸附定时器，保存失败或组件卸载时立即结束合成过渡。 */
  function clearSettle(): void {
    if (settleFrame) cancelAnimationFrame(settleFrame);
    if (settleFadeTimer) clearTimeout(settleFadeTimer);
    if (settleDoneTimer) clearTimeout(settleDoneTimer);
    settleFrame = 0;
    settleFadeTimer = 0;
    settleDoneTimer = 0;
    settling = null;
  }

  /** 松手后让浮动行落到目标槽位，再与真实行内容交叉淡变。 */
  function startSettle(origin: DragSession, id: string): void {
    if (prefersReducedMotion.current) return;
    const target = Array.from(list.querySelectorAll<HTMLButtonElement>("[data-order-id]"))
      .find((row) => row.dataset.orderId === id)?.getBoundingClientRect();
    if (!target) return;
    settling = {
      id,
      left: origin.left, top: origin.top, width: origin.width,
      x: origin.x, y: origin.y, startX: origin.startX, startY: origin.startY,
      targetLeft: target.left, targetTop: target.top,
      arrived: false, leaving: false,
    };
    settleFrame = requestAnimationFrame(() => {
      settleFrame = 0;
      if (settling) settling.arrived = true;
    });
    settleFadeTimer = window.setTimeout(() => {
      settleFadeTimer = 0;
      if (settling) settling.leaving = true;
    }, 170);
    settleDoneTimer = window.setTimeout(() => {
      settleDoneTimer = 0;
      settling = null;
    }, 270);
  }

  /** 结束指针捕获；先清空会话，避免释放捕获事件误触发取消。 */
  function release(): void {
    const pointerId = drag?.pointerId;
    clearPointerFrame();
    drag = null;
    if (pointerId !== undefined && list.hasPointerCapture(pointerId)) list.releasePointerCapture(pointerId);
  }

  /** 松手才保存；先等最后落点进入 DOM，再让浮动行吸附到目标槽位。 */
  async function pointerup(event: PointerEvent): Promise<void> {
    if (!drag || drag.pointerId !== event.pointerId) return;
    flushPointerFrame();
    const origin = { ...drag };
    const { before, id } = origin;
    release();
    await tick();
    if (Math.hypot(origin.x - origin.startX, origin.y - origin.startY) >= 4) {
      startSettle(origin, id);
    }
    void retainFocus(id);
    void persist(before, id);
  }

  /** 指针取消、窗口失焦、Escape 或捕获丢失均回退，不保存草稿。 */
  function cancel(event?: PointerEvent): void {
    if (!drag || (event && event.pointerId !== drag.pointerId)) return;
    const { before, id } = drag;
    clearSettle();
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
      <Badge variant={item.health.state === "abnormal" ? "destructive" : "secondary"}>
        {healthLabels[item.health.state]}{item.health.percent === null ? "" : ` ${item.health.percent}%`}
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
      <div role="listitem" class="order-slot"
        animate:flip={{ duration: prefersReducedMotion.current || activeRowID === item.provider.id ? 0 : 240, easing: springSettle }}>
        <Button variant="row" size="row"
          class="order-row"
          data-order-id={item.provider.id}
          data-inactive={!item.provider.enabled}
          data-dragging={activeRowID === item.provider.id}
          aria-disabled={saving}
          aria-describedby="provider-order-help"
          aria-label={`${item.provider.name}，健康${healthLabels[item.health.state]}${item.health.percent === null ? '' : ` ${item.health.percent}%`}${item.health.reason ? `，${item.health.reason}` : ''}，${item.provider.enabled ? '启用' : '停用'}，第 ${index + 1} 位，共 ${ordered.length} 位`}
          onpointerdown={(event) => start(event, item.provider.id)}
          onkeydown={(event) => keydown(event, item.provider.id)}>
          {@render providerRowContents(item, index)}
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
{#if dragItem && dragIndex >= 0}
  <div
    class="drag-preview"
    data-inactive={!dragItem.provider.enabled}
    data-settling={settling !== null}
    data-leaving={settling?.leaving}
    style={dragPreviewStyle}
    aria-hidden="true">
    <div class="drag-preview-surface">
      {@render providerRowContents(dragItem, dragIndex)}
    </div>
  </div>
{/if}
<p class="sr-only" role="status" aria-live="polite" aria-atomic="true">{announcement}</p>

<style>
  .order-list { position: relative; }
  .order-slot { border-bottom: 1px solid var(--border); }
  .order-slot:last-child { border-bottom: 0; }
  .order-list :global(.order-row) { cursor:grab; touch-action:none; }
  .order-list :global(.order-row[data-dragging="true"]) {
    cursor: grabbing;
    border-radius: 0;
    background: var(--accent);
    box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--primary) 45%, var(--border));
  }
  .order-list :global(.order-row > *) { transition: opacity 130ms ease; }
  .order-list :global(.order-row[data-dragging="true"] > *) { opacity: 0; }
  .drag-preview {
    position: fixed;
    z-index: 80;
    pointer-events: none;
    transform-origin: center;
    will-change: transform;
    transition: opacity 90ms ease;
  }
  .drag-preview[data-settling="true"] {
    transition:
      transform 200ms cubic-bezier(0.16, 1, 0.3, 1),
      opacity 90ms ease;
  }
  .drag-preview[data-leaving="true"] {
    opacity: 0;
  }
  .drag-preview-surface {
    display: flex;
    align-items: center;
    gap: 10px;
    min-height: 58px;
    box-sizing: border-box;
    padding: 12px 16px;
    border: 1px solid color-mix(in srgb, var(--primary) 68%, var(--border));
    border-radius: 10px;
    background: color-mix(in srgb, var(--card) 90%, var(--accent));
    box-shadow:
      0 22px 42px -18px color-mix(in srgb, var(--primary) 48%, transparent),
      0 12px 20px -14px var(--foreground);
    color: var(--foreground);
    animation: drag-preview-in 140ms cubic-bezier(0.2, 0.8, 0.2, 1);
    transition: box-shadow 180ms ease;
  }
  .drag-preview[data-settling="true"] .drag-preview-surface {
    box-shadow:
      0 12px 26px -18px color-mix(in srgb, var(--primary) 34%, transparent),
      0 6px 14px -12px var(--foreground);
  }
  .drag-preview[data-inactive="true"] .drag-preview-surface { opacity: 0.72; }
  @keyframes drag-preview-in {
    from {
      opacity: 0.55;
      box-shadow: 0 8px 18px -14px color-mix(in srgb, var(--primary) 30%, transparent);
      transform: scale(0.985);
    }
  }
  .order-index { flex-shrink: 0; width: 22px; color: var(--muted-foreground); font-size: 11px; font-variant-numeric: tabular-nums; }
  .provider-mark { display: grid; place-content: center; flex-shrink: 0; width: 30px; height: 30px; border: 1px solid var(--border); border-radius: 8px; background: var(--muted); color: var(--primary); font-family: Georgia, serif; }
  .order-copy { display: flex; align-items: center; flex-wrap: wrap; gap: 7px; min-width: 0; }
  .order-copy strong { font-size: 13px; font-weight: 600; overflow-wrap: anywhere; }
  .health { display: inline-flex; }
  @media (prefers-reduced-motion: reduce) {
    .order-list :global(.order-row),
    .order-list :global(.order-row > *),
    .drag-preview,
    .drag-preview-surface { transition: none; }
    .drag-preview-surface { animation: none; }
  }
</style>
