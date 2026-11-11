<script lang="ts">
  import { Trash2 } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import * as Select from "$lib/components/ui/select";

  /**
   * 记录管理：保留规则说明与手动清理入口；使用记录接入前没有可清理的数据，控件保持禁用。
   * ranges 是可清理的时间范围（键为天数，0 表示全部已结束记录），range 是当前选中的范围。
   */
  const ranges: Record<string, string> = {
    "30": "30 天以前",
    "7": "7 天以前",
    "0": "全部已结束记录",
  };
  let range = $state("30");
</script>

<section class="settings-card" aria-labelledby="cleanup-title">
  <div class="flex items-start gap-3 p-6">
    <span
      class="grid size-[34px] shrink-0 place-items-center rounded-lg border text-muted-foreground"
      ><Trash2 class="size-[18px]" aria-hidden="true" /></span
    >
    <div>
      <h2 id="cleanup-title" class="text-[15px] font-semibold">使用记录</h2>
      <p class="mt-1 text-xs leading-relaxed text-muted-foreground">
        请求明细保留 30 天，累计请求数、Token 与费用持续保留。
      </p>
    </div>
  </div>
  <div class="flex flex-col gap-4 px-6 pb-6 sm:flex-row sm:items-center sm:gap-7">
    <div class="grid gap-1.5 text-xs sm:w-60">
      <span id="cleanup-range-label">时间范围</span>
      <Select.Root type="single" bind:value={range} disabled>
        <Select.Trigger aria-labelledby="cleanup-range-label" class="w-full"
          >{ranges[range]}</Select.Trigger
        >
        <Select.Content
          ><Select.Group
            >{#each Object.entries(ranges) as [value, label]}<Select.Item {value}
                >{label}</Select.Item
              >{/each}</Select.Group
          ></Select.Content
        >
      </Select.Root>
    </div>
    <div class="flex flex-1 items-center justify-between gap-4">
      <div>
        <strong class="text-[22px] font-medium tabular-nums"
          >—<small class="text-[11px] font-normal text-muted-foreground"
            > 暂无可清理记录</small
          ></strong
        >
        <p class="mt-1 text-[11px] text-muted-foreground">
          清理只移除可查询的请求明细，保留累计统计与全部配置。
        </p>
      </div>
      <Button variant="outline" size="sm" disabled>预览清理</Button>
    </div>
  </div>
  <p class="border-t px-6 py-3.5 text-[11px] leading-relaxed text-muted-foreground">
    使用记录尚未接入，当前没有请求明细；接入后在此预览并确认清理，到期记录自动清理。
  </p>
</section>
