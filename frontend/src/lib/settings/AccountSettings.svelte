<script lang="ts">
  import { untrack } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import * as InputGroup from "$lib/components/ui/input-group";
  import { VelaService, type AccountRefresh } from "$lib/generated/mygo";
  import { errorMessage, type Notify } from "./sections";

  /** 全局账户刷新：所有已连接供应商共用的定时开关与分钟间隔。 */
  let {
    saved = $bindable(),
    desktop,
    notify,
  }: {
    /** 已保存的账户刷新设置。 */
    saved: AccountRefresh;
    /** 是否运行在 Vela 桌面窗口内。 */
    desktop: boolean;
    /** 保存成功反馈。 */
    notify: Notify;
  } = $props();

  /** 开关与间隔先编辑草稿，取消时恢复已保存值。 */
  let enabled = $state(untrack(() => saved.enabled));
  let minutes = $state<number | null>(untrack(() => saved.minutes));
  /** busy 表示正在保存；error 是校验或保存失败的原因，失败时保留草稿。 */
  let busy = $state(false);
  let error = $state("");
  /** 摘要只反映已保存配置，不把草稿显示为已生效。 */
  const summary = $derived(
    saved.enabled ? `每 ${saved.minutes} 分钟刷新` : "定时刷新已关闭",
  );
  /** 草稿与已保存值不同时才允许保存或取消。 */
  const changed = $derived(enabled !== saved.enabled || minutes !== saved.minutes);

  /** 间隔必须是至少 1 分钟的整数；关闭时保留已填写的间隔。 */
  async function save(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    error = "";
    if (typeof minutes !== "number" || !Number.isInteger(minutes) || minutes < 1) {
      error = "请输入至少 1 分钟的整数刷新间隔。";
      return;
    }
    const next = { enabled, minutes };
    busy = true;
    try {
      await VelaService.saveAccountRefresh(next);
      saved = next;
      notify("已保存全局账户刷新设置");
    } catch (cause) {
      error = errorMessage(cause);
    } finally {
      busy = false;
    }
  }

  /** 丢弃草稿，恢复已保存的全局配置。 */
  function cancel(): void {
    enabled = saved.enabled;
    minutes = saved.minutes;
    error = "";
  }
</script>

<form class="settings-card" novalidate onsubmit={save} aria-label="全局账户设置">
  <div class="settings-heading">
    <div>
      <h2>账户信息刷新</h2>
      <p>所有已连接的供应商共用，统一查询余额、倍率与分组名称。</p>
    </div>
    <span class="tag">{summary}</span>
  </div>
  <label class="setting-row tall border-t" for="account-auto-refresh">
    <span
      ><strong>定时刷新</strong><small>按设定间隔更新账户信息。</small></span
    >
    <Checkbox
      id="account-auto-refresh"
      bind:checked={enabled}
      disabled={!desktop || busy}
    />
  </label>
  <div class="setting-row tall">
    <label for="account-refresh-minutes"
      ><strong>刷新间隔</strong><small
        >{enabled ? "所有已连接账户使用同一间隔。" : "开启定时刷新后生效。"}</small
      ></label
    >
    <InputGroup.Root class="w-36 shrink-0">
      <InputGroup.Input
        id="account-refresh-minutes"
        type="number"
        min="1"
        step="1"
        required
        disabled={!desktop || !enabled || busy}
        bind:value={minutes}
      />
      <InputGroup.Addon align="inline-end" class="text-xs font-normal">分钟</InputGroup.Addon>
    </InputGroup.Root>
  </div>
  <div class="border-t px-6 py-5">
    <span class="text-[10px] font-semibold tracking-widest text-muted-foreground"
      >刷新内容</span
    >
    <div class="my-2 flex flex-wrap gap-1.5">
      {#each ["余额", "倍率", "分组名称"] as item}<span
          class="rounded-[5px] bg-accent px-2 py-1 text-[11px] text-accent-foreground"
          >{item}</span
        >{/each}
    </div>
    <p class="text-[11px] leading-relaxed text-muted-foreground">
      各供应商的接入方式与查询字段在供应商「账户信息」中配置；可返回的内容以上游支持为准。供应商接入后按此设置定时刷新。
    </p>
  </div>
  {#if error}<p class="mx-6 mb-4 text-xs text-destructive" role="alert">
      {error}
    </p>{/if}
  <div class="flex justify-end gap-2 border-t px-6 py-4">
    <Button variant="outline" size="sm" disabled={!changed || busy} onclick={cancel}
      >取消</Button
    >
    <Button type="submit" size="sm" disabled={!desktop || !changed || busy}
      >保存设置</Button
    >
  </div>
</form>
