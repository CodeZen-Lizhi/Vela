<script lang="ts">
  import { onMount } from "svelte";
  import { isMyGo } from "mygo-runtime";
  import { Download, RefreshCw } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import { Progress } from "$lib/components/ui/progress";
  import * as Alert from "$lib/components/ui/alert";
  import * as Dialog from "$lib/components/ui/dialog";
  import { VelaService, type Status as UpdatesStatus } from "$lib/generated/mygo";
  import { errorMessage } from "./sections";

  /** 应用更新仅由用户触发，离开面板不取消已经开始的下载。 */
  const desktop = isMyGo();
  /** updateState 来自宿主；busy 只表示当前界面操作尚未返回，避免重复点击。 */
  let updateState = $state<UpdatesStatus | null>(null);
  let busy = $state(false);
  let error = $state("");
  let confirming = $state(false);
  /** 未知总大小时保持不确定进度，不伪造完成百分比。 */
  const progress = $derived(updateState && updateState.total > 0 ? Math.min(100, updateState.downloaded / updateState.total * 100) : null);

  /** 读取状态；失败明确显示错误，不能展示为没有更新。 */
  async function load(): Promise<void> {
    updateState = await VelaService.applicationUpdate();
  }
  /** 检查固定官方源，失败后保留可重试入口。 */
  async function check(): Promise<void> {
    if (busy) return;
    busy = true; error = "";
    try { updateState = await VelaService.checkApplicationUpdate(); }
    catch (cause) { error = errorMessage(cause); }
    finally { busy = false; }
  }
  /** 启动宿主下载并立即读取状态，下载结果由后续状态查询显示。 */
  async function download(): Promise<void> {
    if (busy) return;
    busy = true; error = "";
    try { await VelaService.downloadApplicationUpdate(); await load(); }
    catch (cause) { error = errorMessage(cause); }
    finally { busy = false; }
  }
  /** 二次确认后才进入正常退出和重启，不在下载结束时自动打断网关。 */
  async function restart(): Promise<void> {
    if (busy) return;
    busy = true; error = "";
    try { await VelaService.restartApplicationUpdate(); confirming = false; await load(); }
    catch (cause) { error = errorMessage(cause); }
    finally { busy = false; }
  }
  /** 用 MB 展示下载字节，原始精确进度仍交给共享 Progress 的可访问属性。 */
  function megabytes(bytes: number): string { return `${(bytes / 1048576).toFixed(1)} MB`; }
  onMount(() => { if (desktop) void load().catch(cause => error = errorMessage(cause)); });
  $effect(() => {
    if (updateState?.phase !== "downloading") return;
    let disposed = false;
    let reading = false;
    const timer = setInterval(async () => {
      if (reading) return;
      reading = true;
      try {
        const next = await VelaService.applicationUpdate();
        if (!disposed) { updateState = next; error = ""; }
      } catch (cause) { if (!disposed) error = errorMessage(cause); }
      finally { reading = false; }
    }, 500);
    return () => { disposed = true; clearInterval(timer); };
  });
</script>

<section class="settings-card mt-5" aria-labelledby="application-update-title">
  <div class="settings-heading">
    <div>
      <h2 id="application-update-title">应用更新</h2>
      <p>当前版本 {updateState?.currentVersion || import.meta.env.VITE_VELA_VERSION} · 手动检查、下载并确认重启</p>
    </div>
    {#if updateState?.phase === "available"}
      <Button variant="outline" size="sm" disabled={busy} onclick={download}><Download aria-hidden="true" />下载更新</Button>
    {:else if updateState?.phase === "ready"}
      <Button size="sm" disabled={busy} onclick={() => confirming = true}>重启并更新</Button>
    {:else}
      <Button variant="outline" size="sm" disabled={!desktop || (updateState !== null && !updateState.enabled) || busy || ["checking", "downloading", "restarting"].includes(updateState?.phase ?? "")} onclick={check}>
        <RefreshCw aria-hidden="true" />{busy || updateState?.phase === "checking" ? "正在检查…" : updateState?.phase === "downloading" ? "正在下载…" : updateState?.phase === "restarting" ? "正在重启…" : "检查更新"}
      </Button>
    {/if}
  </div>
  <div class="flex flex-col gap-3 px-6 pb-5 text-sm" aria-live="polite">
    {#if !desktop}<p class="text-muted-foreground">请在桌面应用中检查更新。</p>
    {:else if updateState && !updateState.enabled}<p class="text-muted-foreground">当前为开发版、只读位置或安装目录不可写。请使用 DMG 安装到“应用程序”后更新。</p>
    {:else if updateState?.phase === "latest"}<p class="text-muted-foreground">当前已是最新可用版本。</p>
    {:else if updateState?.version}<p>新版本 {updateState.version}{updateState.phase === "ready" ? " 已下载并校验完成" : ""}</p>{/if}
    {#if updateState?.phase === "downloading"}
      <Progress value={progress} max={100} aria-label="更新下载进度" />
      <p class="text-xs text-muted-foreground">{megabytes(updateState.downloaded)}{updateState.total > 0 ? ` / ${megabytes(updateState.total)} · ${Math.floor(progress ?? 0)}%` : " · 正在下载并校验…"}</p>
    {/if}
    {#if updateState?.notes}<p class="max-h-48 overflow-y-auto whitespace-pre-wrap text-xs text-muted-foreground">{updateState.notes}</p>{/if}
    {#if error || updateState?.error}<Alert.Root variant="destructive"><Alert.Description>{error || updateState?.error}</Alert.Description></Alert.Root>{/if}
  </div>
</section>
<Dialog.Root bind:open={confirming}>
  <Dialog.Content>
    <Dialog.Header><Dialog.Title>重启并更新 Vela？</Dialog.Title><Dialog.Description>网关将短暂停止，进行中的请求会按正常退出流程收尾。重启后打开 {updateState?.version}，现有配置和记录保留。</Dialog.Description></Dialog.Header>
    <Dialog.Footer><Button variant="outline" disabled={busy} onclick={() => confirming = false}>稍后</Button><Button disabled={busy} onclick={restart}>重启并更新</Button></Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
