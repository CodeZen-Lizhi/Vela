<script lang="ts">
  import { untrack } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import { Switch } from "$lib/components/ui/switch";
  import * as InputGroup from "$lib/components/ui/input-group";
  import { VelaService, type AccountRefresh } from "$lib/generated/mygo";
  import { errorMessage, type Notify } from "./sections";

  /** 两组定时查询与启动/恢复开关独立保存；取消恢复全部草稿。 */
  let { saved = $bindable(), desktop, notify }: {
    /** 已保存的全局查询策略。 */
    saved: AccountRefresh;
    /** 桌面宿主可用时才允许保存。 */
    desktop: boolean;
    /** 仅主动保存设置反馈成功，后台查询不产生 toast。 */
    notify: Notify;
  } = $props();
  /** 本地草稿不因分类切换丢失；两个分钟数关闭时仍保留。 */
  let draft=$state(untrack(()=>({...saved})));
  /** 保存期间锁定表单，失败保留草稿。 */
  let busy=$state(false),error=$state("");
  /** 保存/取消仅在草稿改变时可用。 */
  const changed=$derived(JSON.stringify(draft)!==JSON.stringify(saved));
  /** 同时校验两组整数间隔，配置保存不主动发起上游查询。 */
  async function save(event:SubmitEvent):Promise<void>{
    event.preventDefault();error="";
    if(!Number.isInteger(draft.minutes)||draft.minutes<1||!Number.isInteger(draft.keyMinutes)||draft.keyMinutes<1){error="两组刷新间隔均需为至少 1 分钟的整数。";return;}
    busy=true;
    try{const next={...draft};await VelaService.saveAccountRefresh(next);saved=next;notify("已保存账户刷新设置");}catch(cause){error=errorMessage(cause);}finally{busy=false;}
  }
  /** 取消尚未保存的所有刷新策略改动。 */
  function cancel():void{draft={...saved};error="";}
</script>

<form class="settings-card" novalidate onsubmit={save} aria-label="全局账户设置">
  <div class="settings-heading"><div><h2>账户信息刷新</h2><p>余额与 Key 信息独立查询，三个开关默认关闭。</p></div></div>
  <fieldset disabled={!desktop||busy} class="contents">
    <label class="setting-row tall border-t" for="balance-auto-refresh"><span><strong>余额定时刷新</strong><small>只查询供应商余额，不刷新 Key 信息。</small></span><Switch id="balance-auto-refresh" bind:checked={draft.enabled}/></label>
    <div class="setting-row tall"><label for="balance-refresh-minutes"><strong>余额刷新间隔</strong><small>{saved.enabled?`当前每 ${saved.minutes} 分钟查询一次。`:"已关闭；开启后按分钟间隔查询。"}</small></label><InputGroup.Root class="w-36 shrink-0"><InputGroup.Input id="balance-refresh-minutes" type="number" min="1" step="1" disabled={!draft.enabled} bind:value={draft.minutes}/><InputGroup.Addon align="inline-end" class="text-xs">分钟</InputGroup.Addon></InputGroup.Root></div>
    <label class="setting-row tall border-t" for="key-auto-refresh"><span><strong>Key 信息定时刷新</strong><small>只查询跟随上游的名称、分组及倍率，手动值保持不变。</small></span><Switch id="key-auto-refresh" bind:checked={draft.keyEnabled}/></label>
    <div class="setting-row tall"><label for="key-refresh-minutes"><strong>Key 信息刷新间隔</strong><small>{saved.keyEnabled?`当前每 ${saved.keyMinutes} 分钟查询一次。`:"已关闭；可设置比余额更长的间隔。"}</small></label><InputGroup.Root class="w-36 shrink-0"><InputGroup.Input id="key-refresh-minutes" type="number" min="1" step="1" disabled={!draft.keyEnabled} bind:value={draft.keyMinutes}/><InputGroup.Addon align="inline-end" class="text-xs">分钟</InputGroup.Addon></InputGroup.Root></div>
    <label class="setting-row tall border-t" for="visible-auto-refresh"><span><strong>启动或恢复窗口时刷新</strong><small>从菜单栏、Dock 打开，或隐藏、最小化后恢复时，查询一次余额与 Key 信息。不受定时开关影响。</small></span><Switch id="visible-auto-refresh" bind:checked={draft.onVisible}/></label>
  </fieldset>
  <p class="border-t px-6 py-4 text-xs leading-relaxed text-muted-foreground">自动查询在后台执行，不打断当前页面；失败保留原值，不弹错误提示。手动刷新仍反馈该次失败。连接与字段来源在各供应商及 Key 中配置。</p>
  {#if error}<p class="mx-6 mb-4 text-xs text-destructive" role="alert">{error}</p>{/if}
  <div class="flex justify-end gap-2 border-t px-6 py-4"><Button variant="outline" size="sm" disabled={!changed||busy} onclick={cancel}>取消</Button><Button type="submit" size="sm" disabled={!desktop||!changed||busy}>保存设置</Button></div>
</form>
