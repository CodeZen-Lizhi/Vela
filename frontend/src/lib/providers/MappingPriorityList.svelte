<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { Hint } from "$lib/components/ui/tooltip";
  import { onDestroy, tick, untrack } from "svelte";
  import { GripVertical, Trash2 } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import type { ProviderModelMapping } from "$lib/generated/mygo";
  import { errorMessage } from "$lib/settings/sections";

  /** 当前模型的映射列表：拖柄改序，松手提交；失败及取消均恢复操作前快照。 */
  let { mappings, disabled = false, oncommit, editable = false }: {
    /** 当前模型的映射快照，按优先级从小到大显示。 */
    mappings: ProviderModelMapping[];
    /** 父级只读或保存期间禁止修改。 */
    disabled?: boolean;
    /** 提交当前模型的完整映射，父级负责持久化或更新弹窗草稿；失败须抛出。 */
    oncommit: (mappings: ProviderModelMapping[]) => Promise<void>;
    /** 仅管理弹窗启用请求名输入，展开详情默认只读展示请求名。 */
    editable?: boolean;
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
  /** 当前指针会话及取消所需的原始快照。 */
  let drag = $state<{
    /** 正在拖动的行及指针身份。 */
    id: number; pointerID: number;
    /** 拖动前的顺序，取消时恢复。 */
    before: Row[];
  } | null>(null);
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
    drag = null;
    if (pointerID !== undefined && list?.hasPointerCapture(pointerID)) list.releasePointerCapture(pointerID);
  }

  /** 取消仅恢复本地顺序，不调用父级提交。 */
  function cancel(event?: PointerEvent): void {
    if (!drag || (event && event.pointerId !== drag.pointerID)) return;
    rows = drag.before;
    release();
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
    if (event.button !== 0 || !event.isPrimary || locked || drag) return;
    event.preventDefault();
    (event.currentTarget as HTMLButtonElement).focus({ preventScroll: true });
    drag = { id, pointerID: event.pointerId, before: [...rows] };
    list.setPointerCapture(event.pointerId);
  }

  /** 越过相邻行中线才让位，使用无动画布局避免来回抖动。 */
  function pointermove(event: PointerEvent): void {
    if (!drag || event.pointerId !== drag.pointerID) return;
    event.preventDefault();
    const source = rows.findIndex((row) => row.id === drag?.id);
    let target = source;
    const elements = list.querySelectorAll<HTMLElement>("[data-mapping-row]");
    for (const [index, row] of elements.entries()) {
      const rect = row.getBoundingClientRect();
      const midpoint = rect.top + rect.height / 2;
      if (index < source && event.clientY < midpoint) { target = index; break; }
      if (index > source && event.clientY > midpoint) target = index;
    }
    move(drag.id, target);
  }

  /** 松手时只提交变化后的顺序；单击拖柄不产生保存。 */
  function pointerup(event: PointerEvent): void {
    if (!drag || event.pointerId !== drag.pointerID) return;
    const before = drag.before;
    release();
    if (rows.some((row, index) => row.id !== before[index]?.id)) void commit(before);
  }

  /** 拖柄上下方向键每次移动一位并保存，无需额外可见按钮。 */
  function keydown(event: KeyboardEvent, id: number): void {
    if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return;
    event.preventDefault();
    if (locked || drag) return;
    const before = [...rows];
    const source = rows.findIndex((row) => row.id === id);
    if (move(id, source + (event.key === "ArrowUp" ? -1 : 1))) void commit(before);
  }

  /** 删除一条映射，确认策略由父级提交回调决定。 */
  function remove(id: number): void {
    if (locked || drag) return;
    const before = [...rows];
    rows = rows.filter((row) => row.id !== id);
    void commit(before);
  }

  /** 管理弹窗结束输入后提交新请求名，失败保留原名。 */
  function rename(id: number, value: string): void {
    if (locked || drag) return;
    const before = [...rows];
    const row = rows.find((item) => item.id === id);
    if (!row || row.mapping.requestName === value) return;
    rows = rows.map((item) => item.id === id ? { ...item, mapping: { ...item.mapping, requestName: value } } : item);
    void commit(before);
  }

  onDestroy(release);
</script>

<svelte:window onpointermove={pointermove} onpointerup={pointerup} onpointercancel={cancel}
  onblur={() => cancel()} onkeydown={(event) => { if (event.key === "Escape" && drag) { event.preventDefault(); cancel(); } }} />

<div class="mapping-priority-list" bind:this={list} role="list" aria-label="映射优先级" aria-busy={saving} onlostpointercapture={cancel}>
  {#each rows as row, index (row.id)}
    <div class="mapping-priority-row" data-mapping-row data-dragging={drag?.id === row.id} role="listitem">
      <span class="priority">P{index + 1}</span>
      <Button {...{ onpointerdown: (event: PointerEvent) => start(event, row.id), onkeydown: (event: KeyboardEvent) => keydown(event, row.id) }} variant="ghost" size="icon-xs" class="touch-none cursor-grab"
        disabled={locked} data-mapping-handle={row.id} aria-label={`拖动 ${row.mapping.requestName || '映射'} 调整优先级，或按上下方向键`}
          >
        <GripVertical aria-hidden="true" />
      </Button>
      <div class="request-name">
        {#if editable}
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
{#if failure}<Alert.Root variant="inline-error" class="mt-2"><Alert.Description>{failure}</Alert.Description></Alert.Root>{/if}
<p class="sr-only" role="status" aria-live="polite">{announcement}</p>

<style>
  .mapping-priority-list { display: grid; gap: 6px; min-width: 0; }
  .mapping-priority-row { display: flex; align-items: center; gap: 8px; min-width: 0; padding: 6px 10px; border: 1px solid var(--border); border-radius: var(--radius-lg); background: var(--background); }
  .mapping-priority-row[data-dragging="true"] { background: var(--muted); outline: 1px solid var(--ring); }
  .priority { flex-shrink: 0; color: var(--muted-foreground); font-size: 12px; font-variant-numeric: tabular-nums; }
  .request-name { flex: 1; min-width: 0; }
  .request-name code { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; }
</style>
