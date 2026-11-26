<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { isMyGo } from "mygo-runtime";
  import { onMount, untrack } from "svelte";
  import * as InputGroup from "$lib/components/ui/input-group";
  import { Button } from "$lib/components/ui/button";
  import * as Dialog from "$lib/components/ui/dialog";
  import Choice from "$lib/providers/Choice.svelte";
  import { VelaService, type CleanupPreview } from "$lib/generated/mygo";
  import { errorMessage } from "./sections";
  /** 两个区块使用相同的预览和确认交互，各自调用对应存储。 */
  let { body = false }: { body?: boolean } = $props();
  const title = $derived(body ? "请求正文" : "使用记录");
  const label = $derived(body ? "份正文" : "条请求明细");
  const description = $derived(body ? "设为 0 天后停止保存新正文。清理正文后仍可查看明细、计费和执行链路。" : "清理明细后，累计用量、费用和正文文件仍可保留。");
  const desktop = isMyGo();
  let days = $state<number | undefined>(undefined);
  let saved = $state<number | undefined>(undefined);
  let range = $state("30");
  let preview = $state<(CleanupPreview & { days?: number }) | null>(null);
  let busy = $state(false);
  let error = $state("");
  let message = $state("");
  const options = $derived([
    { value: "30", label: "30 天以前" },
    { value: "7", label: "7 天以前" },
    { value: "0", label: body ? "全部正文" : "全部已结束记录" },
  ]);

  onMount(() => {
    const isBody = untrack(() => body);
    if (isBody) range = "7";
    if (!desktop) {
      days = saved = isBody ? 7 : 30;
      return;
    }
    void VelaService.requestRetention(isBody).then(value => { days = saved = value; }).catch(cause => { error = errorMessage(cause); });
  });

  /** 按存储类型读取固定截止时间的真实预览。 */
  function countBefore(days: number): Promise<CleanupPreview> {
    return body ? VelaService.previewRequestDetailsCleanup(days) : VelaService.previewRequestCleanup(days);
  }

  /** 预览删除，或保存保留天数并在缩短前提示影响。 */
  async function prepare(save: boolean): Promise<void> {
    busy = true;
    error = "";
    message = "";
    try {
      if (!save) {
        preview = await countBefore(Number(range));
      } else if (days !== undefined) {
        if (!Number.isInteger(days) || days < (body ? 0 : 1) || days > 3650) {
          error = `请输入 ${body ? 0 : 1}–3650 之间的整数天数。`;
        } else if (saved !== undefined && days > 0 && (saved === 0 || days < saved)) {
          preview = { ...await countBefore(days), days };
        } else {
          await persist(days, 0);
        }
      }
    } catch (cause) {
      error = errorMessage(cause);
    } finally {
      busy = false;
    }
  }

  /** 保存成功后更新当前显示的期限。 */
  async function persist(value: number, before: number): Promise<void> {
    await VelaService.saveRequestRetention(body, value, before);
    saved = value;
    message = "保留天数已保存。";
  }

  /** 执行用户确认的截止时间，两个入口独立处理。 */
  async function confirm(): Promise<void> {
    if (!preview || busy) return;
    busy = true;
    error = "";
    try {
      if (preview.days !== undefined) {
        await persist(preview.days, preview.before);
      } else {
        const count = body ? await VelaService.cleanupRequestDetails(preview.before) : await VelaService.cleanupRequestRecords(preview.before);
        message = `已清理 ${count} ${label}。`;
      }
      preview = null;
    } catch (cause) {
      error = errorMessage(cause);
    } finally {
      busy = false;
    }
  }
</script>

<section class="grid gap-4 px-5 py-4 @lg:grid-cols-[minmax(0,1fr)_auto] @lg:items-center" aria-label={title}>
  <div class="min-w-0">
    <h3 class="text-sm font-semibold">{title}</h3>
    <p class="mt-1 max-w-sm text-xs leading-relaxed text-muted-foreground">{description}</p>
  </div>
  <div class="flex flex-wrap items-end gap-x-5 gap-y-3">
    <div class="grid gap-1.5">
      <label for={body ? "body-retention" : "record-retention"} class="text-xs font-medium text-muted-foreground">保留天数</label>
      <div class="flex items-center gap-2">
        <InputGroup.Root class="w-24">
          <InputGroup.Input id={body ? "body-retention" : "record-retention"} aria-label={`${title}保留天数`} type="number" min={body ? 0 : 1} max={3650} step={1} bind:value={days} disabled={!desktop || busy || saved === undefined} />
          <InputGroup.Addon align="inline-end">天</InputGroup.Addon>
        </InputGroup.Root>
        <Button variant="outline" disabled={!desktop || busy || days === saved || days === undefined} onclick={() => prepare(true)}>保存</Button>
      </div>
    </div>
    <div class="grid gap-1.5">
      <span class="text-xs font-medium text-muted-foreground">清理范围</span>
      <div class="flex items-center gap-2">
        <div class="w-36">
          <Choice label={`${title}清理时间范围`} bind:value={range} {options} disabled={!desktop || busy} />
        </div>
        <Button variant="outline" disabled={!desktop || busy} onclick={() => prepare(false)}>预览清理</Button>
      </div>
    </div>
  </div>
  {#if error}
    <Alert.Root variant="inline-error" class="@lg:col-span-2"><Alert.Description>{error}</Alert.Description></Alert.Root>
  {:else if message}
    <p class="text-xs text-muted-foreground @lg:col-span-2" role="status">{message}</p>
  {/if}
</section>

<Dialog.Root open={!!preview} onOpenChange={(open) => { if (!open && !busy) preview = null; }}>
  <Dialog.Content>
    <Dialog.Header>
      <Dialog.Title>{preview?.days !== undefined ? `将${title}保留天数改为 ${preview.days} 天？` : `清理${title}？`}</Dialog.Title>
      <Dialog.Description>将删除 {preview?.count ?? 0} {label}，删除后无法恢复。</Dialog.Description>
    </Dialog.Header>
    {#if error}<Alert.Root variant="inline-error"><Alert.Description>{error}</Alert.Description></Alert.Root>{/if}
    <div class="flex justify-end gap-2">
      <Button variant="outline" disabled={busy} onclick={() => (preview = null)}>取消</Button>
      <Button variant="destructive" disabled={busy || (preview?.days === undefined && !preview?.count)} onclick={confirm}>
        {busy ? "处理中…" : preview?.days !== undefined ? "确认修改" : "确认清理"}
      </Button>
    </div>
  </Dialog.Content>
</Dialog.Root>
