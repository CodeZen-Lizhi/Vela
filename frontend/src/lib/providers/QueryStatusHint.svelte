<script lang="ts">
  import type { Snippet } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import * as Tooltip from "$lib/components/ui/tooltip";
  import type { QueryStatus } from "$lib/generated/mygo";

  /** 余额和倍率只在悬停或键盘聚焦时展示本地查询状态，不发起上游请求。 */
  let { label, mode, status, available, children }: {
    /** 数值的完整可访问说明。 */
    label: string;
    /** 区分自动查询、手动倍率、未配置余额与本地状态读取失败。 */
    mode: "query" | "manual" | "unconfigured" | "unavailable";
    /** 最近查询结果及最后成功时间；手动或未配置时不使用。 */
    status?: QueryStatus;
    /** 是否有可显示的有效数值，零也是有效值。 */
    available: boolean;
    /** 列表原有数值或倍率标签。 */
    children: Snippet;
  } = $props();
  /** Go 零时间表示从未成功，不能格式化成公历元年。 */
  const lastSuccess = $derived(status?.lastSuccess && !status.lastSuccess.startsWith("0001-") ? new Date(status.lastSuccess).toLocaleString() : "尚无成功记录");
</script>

<Tooltip.Root delayDuration={300}>
  <Tooltip.Trigger>
    {#snippet child({ props })}<Button {...props} variant="ghost" size="inline" aria-label={`${label}，查看查询状态`}>{@render children()}</Button>{/snippet}
  </Tooltip.Trigger>
  <Tooltip.Content variant="detail" side="bottom" align="start" sideOffset={4}>
    <div class="flex max-w-72 flex-col gap-2">
      {#if mode === "manual"}
        <span>手动设置，不参与上游同步</span>
      {:else if mode === "unconfigured"}
        <span>未配置余额查询</span>
      {:else if mode === "unavailable" || !status}
        <span>查询状态暂不可用，请重新读取</span>
      {:else}
        <span class="font-medium" class:text-destructive={status.state === "failed"} class:text-positive={status.state === "success"}>最近查询：{status.state === "success" ? "成功" : status.state === "failed" ? "失败" : "尚未查询"}</span>
        <span class="text-muted-foreground">上次成功：{lastSuccess}</span>
        {#if status.state === "failed"}
          {#if status.error}<span>失败原因：{status.error}</span>{/if}
          <span class="border-t border-border pt-2 text-muted-foreground">{available ? "当前保留上次有效值" : "当前没有有效值"}</span>
        {/if}
      {/if}
    </div>
  </Tooltip.Content>
</Tooltip.Root>
