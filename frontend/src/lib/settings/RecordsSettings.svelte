<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { isMyGo } from "mygo-runtime";
  import { Trash2 } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import * as Dialog from "$lib/components/ui/dialog";
  import Choice from "$lib/providers/Choice.svelte";
  import { VelaService, type CleanupPreview } from "$lib/generated/mygo";
  import { errorMessage } from "./sections";
  /** 记录清理必须先预览再明确确认，只删除指定截止时间内已结束明细。 */
  let range = $state("30"),
    preview = $state<CleanupPreview | null>(null),
    busy = $state(false),
    error = $state(""),
    message = $state("");
  /** 清理范围使用结束时间；零表示全部已结束记录。 */
  const options = [
    { value: "30", label: "30 天以前" },
    { value: "7", label: "7 天以前" },
    { value: "0", label: "全部已结束记录" },
  ];
  const desktop = isMyGo();
  /** 预览固定截止时刻和数量，失败不打开确认框。 */
  async function inspect(): Promise<void> {
    busy = true;
    error = "";
    message = "";
    try {
      preview = await VelaService.previewRequestCleanup(Number(range));
    } catch (cause) {
      error = errorMessage(cause);
    } finally {
      busy = false;
    }
  }
  /** 确认后清理并重读预览，取消或失败均不伪造成功。 */
  async function cleanup(): Promise<void> {
    if (!preview || busy) return;
    busy = true;
    error = "";
    try {
      const count = await VelaService.cleanupRequestRecords(preview.before);
      const remaining = await VelaService.previewRequestCleanup(Number(range));
      message = `已清理 ${count} 条请求明细，当前范围剩余 ${remaining.count} 条。累计统计与配置保留。`;
      preview = null;
    } catch (cause) {
      error = errorMessage(cause);
    } finally {
      busy = false;
    }
  }
</script>

<section class="settings-card" aria-labelledby="cleanup-title">
  <div class="flex items-start gap-3 p-6">
    <Trash2 aria-hidden="true" class="size-5 text-muted-foreground" />
    <div>
      <h2 id="cleanup-title" class="text-[15px] font-semibold">使用记录</h2>
      <p class="mt-1 text-xs leading-relaxed text-muted-foreground">
        请求明细保留 30 天，正文保留 7 天；累计请求数、Token
        与估算费用持续保留。
      </p>
    </div>
  </div>
  <div class="flex flex-wrap items-center justify-between gap-4 px-6 pb-6">
    <div class="w-60">
      <Choice
        label="清理时间范围"
        bind:value={range}
        {options}
        disabled={!desktop || busy}
      />
    </div>
    <Button variant="outline" disabled={!desktop || busy} onclick={inspect}
      >预览清理</Button
    >
  </div>
  {#if !desktop}<p class="px-6 pb-4 text-xs text-muted-foreground">
      浏览器预览：请在桌面应用中清理真实请求记录。
    </p>{/if}
  {#if error}<Alert.Root variant="inline-error" class="px-6 pb-4"><Alert.Description>
      {error}
    </Alert.Description></Alert.Root>{/if}{#if message}<p class="px-6 pb-4 text-xs" role="status">
      {message}
    </p>{/if}
  <p
    class="border-t px-6 py-3.5 text-[11px] leading-relaxed text-muted-foreground"
  >
    只移除可查询明细；进行中请求、模型、供应商、Key、配置、验证报告及长期累计保留。
  </p>
</section>
<Dialog.Root
  open={!!preview}
  onOpenChange={(open) => {
    if (!open && !busy) preview = null;
  }}
  ><Dialog.Content
    ><Dialog.Header
      ><Dialog.Title>确认清理请求记录</Dialog.Title><Dialog.Description
        >将清理 {preview?.count ?? 0} 条已结束请求及其正文和诊断。此操作不可恢复，长期累计和配置不受影响。</Dialog.Description
      ></Dialog.Header
    >
    <div class="flex justify-end gap-2">
      <Button variant="outline" disabled={busy} onclick={() => (preview = null)}
        >取消</Button
      ><Button
        variant="destructive"
        disabled={busy || !preview?.count}
        onclick={cleanup}>{busy ? "正在清理…" : "确认清理"}</Button
      >
    </div></Dialog.Content
  ></Dialog.Root
>
