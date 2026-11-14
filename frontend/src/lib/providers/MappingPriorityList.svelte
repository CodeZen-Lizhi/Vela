<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { Hint } from "$lib/components/ui/tooltip";
  import { onDestroy, tick, untrack, type Snippet } from "svelte";
  import { flip } from "svelte/animate";
  import { prefersReducedMotion } from "svelte/motion";
  import { Portal } from "bits-ui";
  import { GripVertical, Trash2 } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import type { ProviderModelMapping } from "$lib/generated/mygo";
  import { errorMessage } from "$lib/settings/sections";

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
  /** 换位缓出曲线：快速离开原位，末尾仅轻微回弹约 2%，避免机械感。 */
  function springSettle(t: number): number {
    if (t <= 0) return 0;
    if (t >= 1) return 1;
    return 1 - Math.exp(-9 * t) * Math.cos(5.5 * t);
  }
  /** 拖动会话，记录原顺序、浮动预览起点与当前指针位置，取消时恢复。 */
  type DragSession = {
    /** 拖动映射的稳定身份。 */
    id: number;
    /** 发起本次拖动的指针。 */
    pointerID: number;
    /** 拖动开始前的完整顺序。 */
    before: Row[];
    /** 原行在窗口中的初始坐标与宽度，单位为像素。 */
    left: number; top: number; width: number;
    /** 起始及当前指针坐标，单位为窗口像素。 */
    startX: number; startY: number; x: number; y: number;
  };
  let drag = $state<DragSession | null>(null);
  /** 松手吸附阶段：保持预览到目标槽位，再与真实行交叉淡变。 */
  type SettleSession = {
    /** 正在落位的映射身份。 */
    id: number;
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
  /** 浮动预览对应的映射与当前落位编号。 */
  const previewID = $derived(drag?.id ?? settling?.id ?? -1);
  const dragItem = $derived(rows.find((item) => item.id === previewID));
  const dragIndex = $derived(rows.findIndex((item) => item.id === previewID));
  /** 松手淡出期间先把真实行内容放出来，与预览同步交叉过渡。 */
  const activeRowID = $derived(drag?.id ?? (settling && !settling.leaving ? settling.id : -1));
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

  /** 仅拖柄启动拖动，触屏拖动不会触发页面滚动。 */
  function start(event: PointerEvent, id: number): void {
    if (event.button !== 0 || !event.isPrimary || locked || drag || settling) return;
    event.preventDefault();
    const handle = event.currentTarget as HTMLButtonElement;
    handle.focus({ preventScroll: true });
    const row = handle.closest<HTMLElement>("[data-mapping-row]");
    if (!row) return;
    const rect = row.getBoundingClientRect();
    drag = { id, pointerID: event.pointerId, before: [...rows],
      left: rect.left, top: rect.top, width: rect.width,
      startX: event.clientX, startY: event.clientY, x: event.clientX, y: event.clientY };
    list.setPointerCapture(event.pointerId);
  }

  /** 指针越过相邻行的布局中线才让位；使用布局坐标避免 FLIP 动画导致反复换位。 */
  function pointermove(event: PointerEvent): void {
    if (!drag || event.pointerId !== drag.pointerID) return;
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
    const elements = Array.from(list.children) as HTMLElement[];
    const listTop = list.getBoundingClientRect().top;
    const source = rows.findIndex((item) => item.id === drag?.id);
    let target = source;
    for (const [index, row] of elements.entries()) {
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
  function startSettle(origin: DragSession, id: number): void {
    if (prefersReducedMotion.current) return;
    const target = Array.from(list.querySelectorAll<HTMLElement>("[data-mapping-row]"))
      .find((row) => Number(row.dataset.mappingRow) === id)?.getBoundingClientRect();
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

  /** 松手才保存；先等最后落点进入 DOM，再让浮动行吸附到目标槽位。 */
  async function pointerup(event: PointerEvent): Promise<void> {
    if (!drag || drag.pointerID !== event.pointerId) return;
    flushPointerFrame();
    const origin = { ...drag };
    const { before, id } = origin;
    release();
    await tick();
    if (Math.hypot(origin.x - origin.startX, origin.y - origin.startY) >= 4) {
      startSettle(origin, id);
    }
    void retainFocus(id);
    if (rows.some((row, index) => row.id !== before[index]?.id)) void commit(before);
  }

  /** 拖柄上下方向键每次移动一位并保存，无需额外可见按钮。 */
  function keydown(event: KeyboardEvent, id: number): void {
    if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return;
    event.preventDefault();
    if (locked || drag || settling) return;
    const before = [...rows];
    const source = rows.findIndex((row) => row.id === id);
    if (move(id, source + (event.key === "ArrowUp" ? -1 : 1))) void commit(before);
  }

  /** 删除一条映射，确认策略由父级提交回调决定。 */
  function remove(id: number): void {
    if (locked || drag || settling) return;
    const before = [...rows];
    rows = rows.filter((row) => row.id !== id);
    void commit(before);
  }

  /** 管理弹窗结束输入后提交新请求名，失败保留原名。 */
  function rename(id: number, value: string): void {
    if (locked || drag || settling) return;
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
    <div class="mapping-priority-row" data-mapping-row={row.id} data-dragging={activeRowID === row.id}
      animate:flip={{ duration: prefersReducedMotion.current || activeRowID === row.id ? 0 : 240, easing: springSettle }} role="listitem">
      <span class="priority">P{index + 1}</span>
      <Button {...{ onpointerdown: (event: PointerEvent) => start(event, row.id), onkeydown: (event: KeyboardEvent) => keydown(event, row.id) }} variant="ghost" size="icon-xs" class="touch-none cursor-grab"
        disabled={locked} data-mapping-handle={row.id} aria-label={`拖动 ${row.mapping.requestName || '映射'} 调整优先级，或按上下方向键`}
          >
        <GripVertical aria-hidden="true" />
      </Button>
      <div class="request-name">
        {#if editable && nameEditor}
          {@render nameEditor(row.mapping.requestName, rows.filter((item) => item.id !== row.id).map((item) => item.mapping.requestName), locked || !!drag || !!settling, (name) => rename(row.id, name))}
        {:else if editable}
          <Input value={row.mapping.requestName} disabled={locked || !!drag} aria-label={`优先级 P${index + 1} 的请求名`}
            onchange={(event) => rename(row.id, event.currentTarget.value)} />
        {:else}
          <Hint overflowOnly text={row.mapping.requestName}>{#snippet children(hintProps1)}<code {...hintProps1} >{row.mapping.requestName}</code>{/snippet}</Hint>
        {/if}
      </div>
      <Button onclick={() => remove(row.id)} variant="ghost" size="icon-xs" disabled={locked || !!drag}
        aria-label={`删除映射 ${row.mapping.requestName}`}  >
        <Trash2 aria-hidden="true" />
      </Button>
    </div>
  {/each}
</div>

{#if dragItem && dragIndex >= 0}
  <Portal>
    <div class="drag-preview" data-settling={settling !== null} data-leaving={settling?.leaving} style={dragPreviewStyle} aria-hidden="true">
      <div class="mapping-priority-row drag-preview-surface">
        <span class="priority">P{dragIndex + 1}</span>
        <span class="preview-icon"><GripVertical /></span>
        <div class="request-name">
          {#if editable}<Input value={dragItem.mapping.requestName} readonly tabindex={-1} />
          {:else}<code>{dragItem.mapping.requestName}</code>{/if}
        </div>
        <span class="preview-icon"><Trash2 /></span>
      </div>
    </div>
  </Portal>
{/if}
{#if failure}<Alert.Root variant="inline-error" class="mt-2"><Alert.Description>{failure}</Alert.Description></Alert.Root>{/if}
<p class="sr-only" role="status" aria-live="polite">{announcement}</p>

<style>
  .mapping-priority-list { position: relative; display: grid; gap: 6px; min-width: 0; }
  .mapping-priority-row { display: flex; align-items: center; gap: 8px; min-width: 0; padding: 6px 10px; border: 1px solid var(--border); border-radius: var(--radius-lg); background: var(--background); }
  .mapping-priority-row[data-dragging="true"] { background: var(--accent); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--primary) 45%, var(--border)); }
  .priority { flex-shrink: 0; color: var(--muted-foreground); font-size: 12px; font-variant-numeric: tabular-nums; }
  .request-name { flex: 1; min-width: 0; }
  .request-name code { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; }
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
    gap: 8px;
    min-height: 38px;
    box-sizing: border-box;
    padding: 6px 10px;
    border: 1px solid color-mix(in srgb, var(--primary) 68%, var(--border));
    border-radius: var(--radius-lg);
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
  @keyframes drag-preview-in {
    from {
      opacity: 0.55;
      box-shadow: 0 8px 18px -14px color-mix(in srgb, var(--primary) 30%, transparent);
      transform: scale(0.985);
    }
  }
  .mapping-priority-row > * { transition: opacity 130ms ease; }
  .mapping-priority-row[data-dragging="true"] > * { opacity: 0; }
  .preview-icon { display: grid; place-items: center; width: 24px; height: 24px; flex-shrink: 0; }
  .preview-icon :global(svg) { width: 12px; height: 12px; }
  @media (prefers-reduced-motion: reduce) {
    .mapping-priority-row > *, .drag-preview, .drag-preview-surface { transition: none; animation: none; }
  }
</style>
