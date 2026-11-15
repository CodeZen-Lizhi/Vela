<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { Hint } from "$lib/components/ui/tooltip";
  import { onDestroy, tick, untrack, type Snippet } from "svelte";
  import { flip } from "svelte/animate";
  import { prefersReducedMotion } from "svelte/motion";
  import { GripVertical, Trash2 } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import type { ProviderModelMapping } from "$lib/generated/mygo";
  import { errorMessage } from "$lib/settings/sections";
  import {
    clampDragOffset,
    dragTargetIndex,
    neighborWaveOffset,
    settleTiming,
    slotShiftForRow,
    type SortRowGeometry,
  } from "$lib/ordering/wave-sort";

  /** 当前模型的映射列表：拖柄改序，松手提交；失败及取消均恢复操作前快照。 */
  let { mappings, disabled = false, oncommit, editable = false, nameEditor }: {
    /** 当前模型的映射快照，按优先级从小到大显示。 */
    mappings: ProviderModelMapping[];
    /** 父级只读或保存期间禁止修改。 */
    disabled?: boolean;
    /** 提交当前模型的完整映射，父级负责持久化或更新弹窗草稿；失败须抛出。 */
    oncommit: (mappings: ProviderModelMapping[]) => Promise<void>;
    /** 仅管理弹窗启用请求名输入，展开详情默认只读展示请求名。 */
    editable?: boolean;
    /** 父级提供候选选择器；参数依次为当前名、其他行名称、禁用状态和改名回调。 */
    nameEditor?: Snippet<[string, string[], boolean, (value: string) => void]>;
  } = $props();

  /** 本地行身份独立于可编辑请求名，避免重排和输入导致控件重新挂载。 */
  type Row = {
    /** 当前组件内的稳定行身份。 */
    id: number;
    /** 不直接修改父级快照的映射副本。 */
    mapping: ProviderModelMapping;
  };
  /** 为每次传入的快照分配本地行身份。 */
  let nextID = 0;
  /** 展示顺序，拖动中为临时顺序。 */
  let rows = $state<Row[]>([]);
  /** 当前提交完成前锁定全部操作。 */
  let saving = $state(false);
  /** 提交失败原因，保留在当前列表内便于重试。 */
  let failure = $state("");
  /** 辅助技术读取的排序或取消反馈。 */
  let announcement = $state("");
  /** 稳定的指针捕获容器，不随列表改序替换。 */
  let list: HTMLDivElement;
  /** 键盘换位缓出曲线：快速离开原位，末尾仅轻微回弹约 2%。 */
  function springSettle(t: number): number {
    if (t <= 0) return 0;
    if (t >= 1) return 1;
    return 1 - Math.exp(-9 * t) * Math.cos(5.5 * t);
  }
  /** 拖动会话只记录纵向位移；真实行留在列表流中并直接跟手移动。 */
  type DragSession = {
    /** 拖动映射的稳定身份。 */
    id: number;
    /** 发起本次拖动的指针。 */
    pointerID: number;
    /** 拖动开始前的完整顺序。 */
    before: Row[];
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
  /** 松手落位的映射身份；保留到过渡结束后再允许下一次拖动。 */
  let settlingID = $state(-1);
  /** 本次落位的时长与曲线，由松手前的近似速度决定。 */
  let settleDuration = $state(140);
  let settleEase = $state("cubic-bezier(0.25, 0.8, 0.3, 1)");
  /** 指针更新合并到下一绘制帧，避免高频 pointermove 造成重复布局与抖动。 */
  let pointerFrame = 0;
  let pendingPointerY: number | null = null;
  /** 落位清理定时器，取消或卸载时统一释放。 */
  let settleTimer = 0;
  /** 当前目标槽位由被拖行中心与原始行中心共同计算，支持高度不同的映射行。 */
  const dragTarget = $derived(drag
    ? dragTargetIndex(drag.geometry, rows.findIndex((item) => item.id === drag?.id), drag.offset)
    : -1);
  /** 目标顺序只用于拖动中的优先级显示，实际列表在松手后才提交。 */
  const visualOrder = $derived.by(() => {
    if (!drag || dragTarget < 0) return rows.map((row) => row.id);
    const ids = rows.map((row) => row.id);
    const source = ids.indexOf(drag.id);
    if (source < 0 || source === dragTarget) return ids;
    const [id] = ids.splice(source, 1);
    if (id !== undefined) ids.splice(dragTarget, 0, id);
    return ids;
  });
  /** 父级禁用、内部保存和指针拖动时均禁止其他编辑。 */
  let locked = $derived(disabled || saving);

  $effect(() => {
    const snapshot = [...mappings].sort((a, b) => a.priority - b.priority);
    untrack(() => {
      cancel();
      // 回读新快照时复用仍存在的行身份，保存后拖柄焦点不会随重新挂载丢失。
      const available = [...rows];
      rows = snapshot.map((mapping) => {
        const index = available.findIndex((row) => row.mapping.requestName === mapping.requestName);
        const previous = index < 0 ? undefined : available.splice(index, 1)[0];
        return { id: previous?.id ?? nextID++, mapping: { ...mapping } };
      });
    });
  });
  $effect(() => { if (disabled) untrack(cancel); });

  /** 清除会话后释放捕获，防止 lostpointercapture 将正常松手视为取消。 */
  function release(): void {
    const pointerID = drag?.pointerID;
    clearPointerFrame();
    drag = null;
    if (pointerID !== undefined && list?.hasPointerCapture(pointerID)) list.releasePointerCapture(pointerID);
  }

  /** 取消仅恢复本地顺序，不调用父级提交。 */
  function cancel(event?: PointerEvent): void {
    if (!drag || (event && event.pointerId !== drag.pointerID)) return;
    const id = drag.id;
    rows = drag.before;
    clearSettle();
    release();
    void retainFocus(id);
    announcement = "已取消移动，恢复原顺序";
  }

  /** 统一连续编号并提交，出错恢复本次操作前的完整列表。 */
  async function commit(before: Row[]): Promise<void> {
    const values = rows.map((row, index) => ({ ...row.mapping, priority: index + 1 }));
    const focused = document.activeElement;
    const focusedID = focused instanceof HTMLElement ? focused.dataset.mappingHandle : undefined;
    saving = true;
    failure = "";
    try {
      await oncommit(values);
      announcement = "映射已更新";
    } catch (cause) {
      clearSettle();
      rows = before;
      failure = `映射保存失败，已恢复原内容：${errorMessage(cause)}`;
    } finally {
      saving = false;
      await tick();
      if (focusedID && (document.activeElement === document.body || document.activeElement === focused)) {
        Array.from(list.querySelectorAll<HTMLButtonElement>("[data-mapping-handle]"))
          .find((handle) => handle.dataset.mappingHandle === focusedID)?.focus({ preventScroll: true });
      }
    }
  }

  /** 按身份恢复焦点；等待 DOM 重排后执行，不覆盖用户已经移走的焦点。 */
  async function retainFocus(id: number): Promise<void> {
    const focused = document.activeElement;
    if (!(focused instanceof HTMLElement) || Number(focused.dataset.mappingHandle) !== id) return;
    await tick();
    if (document.activeElement !== focused && document.activeElement !== document.body) return;
    Array.from(list.querySelectorAll<HTMLButtonElement>("[data-mapping-handle]"))
      .find((row) => Number(row.dataset.mappingHandle) === id)?.focus({ preventScroll: true });
  }

  /** 移动本列表的一行，不改变其他模型的映射。 */
  function move(id: number, target: number): boolean {
    const source = rows.findIndex((row) => row.id === id);
    if (source < 0 || target < 0 || target >= rows.length || source === target) return false;
    const next = [...rows];
    const [row] = next.splice(source, 1);
    if (!row) return false;
    next.splice(target, 0, row);
    rows = next;
    announcement = `${row.mapping.requestName}，优先级 P${target + 1}`;
    return true;
  }

  /** 返回映射行在当前拖动帧中的位移与过渡；静止时完全恢复列表原样。 */
  function rowMotion(index: number, id: number): { transform: string; transition: string } {
    if (!drag) {
      if (settlingID !== id) return { transform: "", transition: "" };
      return {
        transform: "translate3d(0, 0, 0) scale(1)",
        transition: `transform ${settleDuration}ms ${settleEase}, box-shadow 130ms ease`,
      };
    }

    const source = rows.findIndex((row) => row.id === drag?.id);
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

  /** 仅拖柄启动拖动，触屏拖动不会触发页面滚动。 */
  function start(event: PointerEvent, id: number): void {
    if (event.button !== 0 || !event.isPrimary || locked || drag || settlingID >= 0) return;
    event.preventDefault();
    const handle = event.currentTarget as HTMLButtonElement;
    handle.focus({ preventScroll: true });
    const row = handle.closest<HTMLElement>("[data-mapping-row]");
    if (!row) return;
    const listRect = list.getBoundingClientRect();
    const geometry = Array.from(list.querySelectorAll<HTMLElement>(":scope > [data-mapping-row]"))
      .map((element) => {
        const rect = element.getBoundingClientRect();
        return { top: rect.top - listRect.top, height: rect.height };
      });
    drag = {
      id,
      pointerID: event.pointerId,
      before: [...rows],
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
    if (!drag || event.pointerId !== drag.pointerID) return;
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
    const source = rows.findIndex((item) => item.id === drag?.id);
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
    settlingID = -1;
  }

  /** 开始一次带速度的落位过渡；真实 DOM 在下一帧重排，让当前画面先保留到过渡属性生效。 */
  function beginSettle(id: number, flick: number): void {
    const timing = settleTiming(flick);
    settlingID = id;
    settleDuration = prefersReducedMotion.current ? 0 : Math.round(timing.duration);
    settleEase = timing.easing;
    if (settleTimer) clearTimeout(settleTimer);
    settleTimer = window.setTimeout(() => {
      settleTimer = 0;
      settlingID = -1;
    }, settleDuration + 40);
  }

  /** 松手才保存；先让落位过渡属性生效，再重排真实列表并保存。 */
  async function pointerup(event: PointerEvent): Promise<void> {
    if (!drag || drag.pointerID !== event.pointerId) return;
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
    const source = rows.findIndex((row) => row.id === id);
    if (source >= 0 && target >= 0 && source !== target) {
      const next = [...rows];
      const [row] = next.splice(source, 1);
      if (row) next.splice(target, 0, row);
      rows = next;
    }
    release();
    await tick();
    void retainFocus(id);
    if (rows.some((row, index) => row.id !== before[index]?.id)) void commit(before);
  }

  /** 拖柄上下方向键每次移动一位并保存，无需额外可见按钮。 */
  function keydown(event: KeyboardEvent, id: number): void {
    if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return;
    event.preventDefault();
    if (locked || drag || settlingID >= 0) return;
    const before = [...rows];
    const source = rows.findIndex((row) => row.id === id);
    if (move(id, source + (event.key === "ArrowUp" ? -1 : 1))) void commit(before);
  }

  /** 删除一条映射，确认策略由父级提交回调决定。 */
  function remove(id: number): void {
    if (locked || drag || settlingID >= 0) return;
    const before = [...rows];
    rows = rows.filter((row) => row.id !== id);
    void commit(before);
  }

  /** 管理弹窗结束输入后提交新请求名，失败保留原名。 */
  function rename(id: number, value: string): void {
    if (locked || drag || settlingID >= 0) return;
    const before = [...rows];
    const row = rows.find((item) => item.id === id);
    if (!row || row.mapping.requestName === value) return;
    rows = rows.map((item) => item.id === id ? { ...item, mapping: { ...item.mapping, requestName: value } } : item);
    void commit(before);
  }

  onDestroy(() => { clearSettle(); release(); });
</script>

<svelte:window onpointermove={pointermove} onpointerup={pointerup} onpointercancel={cancel}
  onblur={() => cancel()} onkeydown={(event) => { if (event.key === "Escape" && drag) { event.preventDefault(); cancel(); } }} />

<div class="mapping-priority-list" bind:this={list} role="list" aria-label="映射优先级" aria-busy={saving} onlostpointercapture={cancel}>
  {#each rows as row, index (row.id)}
    {@const motion = rowMotion(index, row.id)}
    <div class="mapping-priority-row" data-mapping-row={row.id}
      data-lifted={drag?.id === row.id && Math.abs(drag.offset) > 0.5}
      data-settling={settlingID === row.id}
      style:transform={motion.transform}
      style:transition={motion.transition}
      animate:flip={{ duration: prefersReducedMotion.current || settlingID >= 0 ? 0 : 240, easing: springSettle }} role="listitem">
      <span class="priority">P{visualOrder.indexOf(row.id) + 1}</span>
      <Button {...{ onpointerdown: (event: PointerEvent) => start(event, row.id), onkeydown: (event: KeyboardEvent) => keydown(event, row.id) }} variant="ghost" size="icon-xs" class="touch-none cursor-grab"
        disabled={locked} data-mapping-handle={row.id} aria-label={`拖动 ${row.mapping.requestName || '映射'} 调整优先级，或按上下方向键`}
          >
        <GripVertical aria-hidden="true" />
      </Button>
      <div class="request-name">
        {#if editable && nameEditor}
          {@render nameEditor(row.mapping.requestName, rows.filter((item) => item.id !== row.id).map((item) => item.mapping.requestName), locked || !!drag || settlingID >= 0, (name) => rename(row.id, name))}
        {:else if editable}
          <Input value={row.mapping.requestName} disabled={locked || !!drag} aria-label={`优先级 P${visualOrder.indexOf(row.id) + 1} 的请求名`}
            onchange={(event) => rename(row.id, event.currentTarget.value)} />
        {:else}
          <Hint overflowOnly text={row.mapping.requestName}>{#snippet children(hintProps1)}<code {...hintProps1} >{row.mapping.requestName}</code>{/snippet}</Hint>
        {/if}
      </div>
      <Button onclick={() => remove(row.id)} variant="ghost" size="icon-xs" disabled={locked || !!drag || settlingID >= 0}
        aria-label={`删除映射 ${row.mapping.requestName}`}  >
        <Trash2 aria-hidden="true" />
      </Button>
    </div>
  {/each}
</div>

{#if failure}<Alert.Root variant="inline-error" class="mt-2"><Alert.Description>{failure}</Alert.Description></Alert.Root>{/if}
<p class="sr-only" role="status" aria-live="polite">{announcement}</p>

<style>
  .mapping-priority-list { position: relative; display: grid; gap: 6px; min-width: 0; }
  .mapping-priority-row {
    position: relative;
    z-index: 1;
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    padding: 6px 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    background: var(--background);
    transform-origin: 50% 50%;
    will-change: transform;
  }
  .mapping-priority-row[data-lifted="true"] {
    z-index: 3;
    box-shadow:
      0 14px 26px color-mix(in srgb, var(--foreground) 13%, transparent),
      0 3px 8px color-mix(in srgb, var(--foreground) 5%, transparent);
  }
  .mapping-priority-row[data-settling="true"] { z-index: 2; }
  .priority { flex-shrink: 0; color: var(--muted-foreground); font-size: 12px; font-variant-numeric: tabular-nums; }
  .request-name { flex: 1; min-width: 0; }
  .request-name code { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; }
  @media (prefers-reduced-motion: reduce) {
    .mapping-priority-row { transition: none; }
  }
</style>
