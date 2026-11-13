<script lang="ts">
  import { Label } from "$lib/components/ui/label";
  import * as Alert from "$lib/components/ui/alert";
  import { untrack } from "svelte";
  import { Activity, RotateCcw, ShieldAlert, TrendingUp, Wallet } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import { Switch } from "$lib/components/ui/switch";
  import * as InputGroup from "$lib/components/ui/input-group";
  import { VelaService, type AttentionSettings } from "$lib/generated/mygo";
  import { errorMessage, type Notify } from "./sections";

  /** 总览提醒偏好：四类事项的展示开关与统一的余额阈值，保存前不改变生效规则。 */
  let {
    saved = $bindable(),
    desktop,
    notify,
  }: {
    /** 已保存的提醒偏好。 */
    saved: AttentionSettings;
    /** 是否运行在 Vela 桌面窗口内。 */
    desktop: boolean;
    /** 保存成功反馈。 */
    notify: Notify;
  } = $props();

  /** 四个开关的键名，与提醒偏好字段一一对应。 */
  type Toggle = "balance" | "difference" | "health" | "rate";
  /** 四类关注事项只控制展示，不在这里配置采集或验证任务。 */
  const options: { id: Toggle; title: string; description: string; icon: typeof Wallet }[] = [
    { id: "balance", title: "供应商余额不足", description: "余额低于设定金额时展示", icon: Wallet },
    { id: "difference", title: "模型验证存在差异", description: "沿用最新一次模型验证结论", icon: ShieldAlert },
    { id: "health", title: "供应商健康异常", description: "供应商降级、异常或不可用时展示", icon: Activity },
    { id: "rate", title: "Key 倍率变化", description: "升高、降低都展示，标为已读后移除", icon: TrendingUp },
  ];

  /** 草稿独立于已保存值；阈值输入清空时为 null。 */
  let draft = $state<Omit<AttentionSettings, "balanceThreshold"> & { balanceThreshold: number | null }>(
    untrack(() => ({ ...saved })),
  );
  /** busy 表示正在保存；error 是校验或保存失败的原因，失败时保留草稿。 */
  let busy = $state(false);
  let error = $state("");
  /** 已保存偏好中开启的提醒数量，用于标题旁的摘要。 */
  const enabledCount = $derived(
    [saved.balance, saved.difference, saved.health, saved.rate].filter(Boolean).length,
  );
  /** 任一开关或阈值与已保存值不同时才允许保存。 */
  const changed = $derived(
    options.some((option) => draft[option.id] !== saved[option.id]) ||
      draft.balanceThreshold !== saved.balanceThreshold,
  );

  /** 余额提醒关闭时阈值不参与校验，沿用已保存的阈值。 */
  async function save(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    error = "";
    const typed = draft.balanceThreshold;
    const valid = typeof typed === "number" && Number.isFinite(typed) && typed >= 0;
    if (!valid && draft.balance) {
      error = "请填写不小于 0 的余额阈值。";
      return;
    }
    const next: AttentionSettings = {
      ...draft,
      balanceThreshold: valid ? typed : saved.balanceThreshold,
    };
    busy = true;
    try {
      await VelaService.saveAttentionSettings(next);
      saved = next;
      draft = { ...next };
      notify("已保存总览提醒设置");
    } catch (cause) {
      error = errorMessage(cause);
    } finally {
      busy = false;
    }
  }

  /** 恢复四项开启和默认余额阈值，仍需保存后才生效。 */
  function restoreDefaults(): void {
    draft = { balance: true, difference: true, health: true, rate: true, balanceThreshold: 10 };
    error = "";
  }
</script>

<section class="settings-card" aria-labelledby="attention-title">
  <header class="px-6 py-5 text-[13px]">
    <span id="attention-title">总览提醒</span><span
      class="ml-3 text-[11px] text-muted-foreground"
      >{enabledCount ? `${enabledCount} 项开启` : "已关闭"}</span
    >
  </header>
  <form class="px-6 pb-6" novalidate onsubmit={save}>
    <p class="mb-5 text-xs leading-relaxed text-muted-foreground">
      只关注已启用的供应商、上游 Key 和可调用模型；全部关闭后总览不再显示该模块。
    </p>
    <div class="border-t">
      {#each options as option}
        {@const Icon = option.icon}
        <div class="attention-row">
          <span class="attention-icon"
            ><Icon class="size-[17px]" strokeWidth={1.7} aria-hidden="true" /></span
          >
          <div class="min-w-0">
            <strong id={`attention-${option.id}`} class="text-[13px] font-medium"
              >{option.title}</strong
            >
            <p class="mt-1 text-xs leading-relaxed text-muted-foreground">
              {option.description}
            </p>
            {#if option.id === "balance" && draft.balance}
              <Label variant="content" class="mt-3 flex flex-wrap items-center gap-2 text-xs text-muted-foreground"
                >低于
                <InputGroup.Root class="w-32">
                  <InputGroup.Addon>$</InputGroup.Addon>
                  <InputGroup.Input
                    aria-label="余额提醒阈值"
                    type="number"
                    min="0"
                    step="0.01"
                    disabled={!desktop || busy}
                    bind:value={draft.balanceThreshold}
                  />
                </InputGroup.Root>
                <span>USD</span>
              </Label>
            {:else if option.id === "balance"}
              <p class="mt-1 text-[10px] text-muted-foreground">
                余额阈值将在重新开启余额提醒后生效
              </p>
            {/if}
          </div>
          <Switch
            class="mt-1"
            aria-labelledby={`attention-${option.id}`}
            bind:checked={draft[option.id]}
            disabled={!desktop || busy}
          />
        </div>
      {/each}
    </div>
    <p class="mt-4 text-[11px] leading-relaxed text-muted-foreground">
      仅控制总览展示，不会开启余额采集、模型验证或健康检查。总览接入后按这里的偏好显示。
    </p>
    {#if error}<Alert.Root variant="inline-error" class="mt-3"><Alert.Description>{error}</Alert.Description></Alert.Root>{/if}
    <div class="mt-5 flex flex-wrap items-center justify-end gap-2">
      <Button
        variant="ghost"
        size="sm"
        class="mr-auto text-muted-foreground"
        disabled={!desktop || busy}
        onclick={restoreDefaults}><RotateCcw />恢复默认</Button
      >
      <Button type="submit" size="sm" disabled={!desktop || !changed || busy}
        >保存设置</Button
      >
    </div>
  </form>
</section>
