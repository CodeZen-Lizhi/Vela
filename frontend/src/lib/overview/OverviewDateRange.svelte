<script module lang="ts">
  /** 总览顶部可选范围；custom 表示通过日历端点选出的日期区间。 */
  export type OverviewRange = "today" | "week" | "month" | "all" | "custom";
</script>

<script lang="ts">
  import { Hint } from "$lib/components/ui/tooltip";
  import { parseDate, type DateValue } from "@internationalized/date";
  import { CalendarDays } from "@lucide/svelte";
  import * as ToggleGroup from "$lib/components/ui/toggle-group";
  import * as Popover from "$lib/components/ui/popover";
  import { RangeCalendar } from "$lib/components/ui/range-calendar";

  /** 组件只维护选择器交互，统计范围和统计时区仍由总览页面拥有。 */
  let {
    range,
    start,
    end,
    min,
    max,
    timezone,
    disabled = false,
    onPresetChange,
    onDateChange,
  }: {
    /** 当前时间范围口径。 */ range: OverviewRange;
    /** 当前范围开始日，格式为 YYYY-MM-DD。 */ start: string;
    /** 当前范围结束日，格式为 YYYY-MM-DD。 */ end: string;
    /** 可选最早日，格式为 YYYY-MM-DD。 */ min: string;
    /** 可选最晚日，格式为 YYYY-MM-DD。 */ max: string;
    /** 统计快照所属时区，仅用于说明。 */ timezone: string;
    /** 快照尚未加载时禁止切换范围。 */ disabled?: boolean;
    /** 选择快捷范围后通知页面切换口径。 */ onPresetChange: (range: Exclude<OverviewRange, "custom">) => void;
    /** 两个端点都选中后通知页面应用自定义日期。 */ onDateChange: (start: string, end: string) => void;
  } = $props();

  /** 快捷范围沿用总览既有业务口径，不因控件形态变化而扩展。 */
  const presets: Array<{ value: Exclude<OverviewRange, "custom">; label: string }> = [
    { value: "today", label: "今天" },
    { value: "week", label: "7 天" },
    { value: "month", label: "30 天" },
    { value: "all", label: "全部" },
  ];
  /** 自定义入口打开日历，完整端点选择后关闭。 */
  let open = $state(false);
  /** 日历允许先落一个端点；只有两端齐全时才把范围交给页面。 */
  let calendarValue = $state<{ start: DateValue | undefined; end: DateValue | undefined }>({
    start: undefined,
    end: undefined,
  });

  /** 页面范围变化后同步日历；打开期间不回写，保留用户的第一个端点。 */
  $effect(() => {
    if (open) return;
    calendarValue = { start: parseDate(start), end: parseDate(end) };
  });

  /** 双向同步分段控件状态，日历关闭后仍选中已应用的自定义范围。 */
  let selectedRange = $derived<OverviewRange>(open ? "custom" : range);

  const selectedLabel = $derived(
    range === "custom" ? formatDateRange(start, end, max) : presets.find(item => item.value === range)?.label ?? "选择时间",
  );

  /** 快捷范围立即应用并关闭弹出层。 */
  function selectPreset(value: Exclude<OverviewRange, "custom">): void {
    open = false;
    onPresetChange(value);
  }

  /** 保留起点或终点未完成的日历交互；完整区间才更新统计。 */
  function selectDates(value: { start: DateValue | undefined; end: DateValue | undefined }): void {
    calendarValue = value;
    if (!value.start || !value.end) return;
    onDateChange(value.start.toString(), value.end.toString());
    open = false;
  }

  /** 与统计日字符串直接转换，避免本地 Date 在跨时区时偏移一天。 */
  function formatDate(value: string, latest: string): string {
    const [year, month, day] = value.split("-");
    const date = `${Number(month)}月${Number(day)}日`;
    return year === latest.slice(0, 4) ? date : `${year}年${date}`;
  }

  /** 同年区间可省略重复年份，跨年时两个端点都显示年份。 */
  function formatDateRange(rangeStart: string, rangeEnd: string, latest: string): string {
    if (rangeStart === rangeEnd) return formatDate(rangeStart, latest);
    return `${formatDate(rangeStart, latest)} — ${formatDate(rangeEnd, latest)}`;
  }
</script>

<ToggleGroup.Root
  type="single"
  variant="segmented"
  size="sm"
  spacing={1}
  bind:value={selectedRange}
  onValueChange={value => { if (value && value !== "custom") selectPreset(value as Exclude<OverviewRange, "custom">); }}
  {disabled}
  class="max-w-full flex-wrap"
  aria-label="统计时间范围"
>
  {#each presets as preset}
    <ToggleGroup.Item value={preset.value}>{preset.label}</ToggleGroup.Item>
  {/each}
  <Popover.Root bind:open>
    <Popover.Trigger>
      {#snippet child({ props })}
        <!-- Popover 的开关状态不应覆盖分段选项的选中状态。 -->
        <ToggleGroup.Item {...props} data-state={selectedRange === "custom" ? "on" : "off"} value="custom" aria-label={`自定义时间范围，当前为${selectedLabel}`}>
          <CalendarDays data-icon="inline-start" aria-hidden="true" />
          自定义
        </ToggleGroup.Item>
      {/snippet}
    </Popover.Trigger>
    <Popover.Content align="end" sideOffset={8} class="w-[18rem] max-w-[calc(100vw-2rem)] gap-2 p-0">
      <Popover.Header class="px-3 pt-3">
        <Popover.Title class="text-foreground text-sm font-semibold tracking-normal normal-case">选择时间范围</Popover.Title>
        <Popover.Description class="text-xs">先选第一天，再选最后一天</Popover.Description>
      </Popover.Header>
      <RangeCalendar
        class="w-full p-2 [--cell-size:1.75rem]"
        value={calendarValue}
        onValueChange={selectDates}
        minValue={parseDate(min)}
        maxValue={parseDate(max)}
        locale="zh-CN"
        weekdayFormat="narrow"
        weekStartsOn={1}
        fixedWeeks
        calendarLabel="统计日期范围"
      />
      <footer class="border-border flex min-w-0 items-center justify-between gap-3 border-t px-3 py-2 text-[10px] text-muted-foreground">
        <span>{formatDateRange(start, end, max)}</span>
        <Hint text={timezone}>{#snippet children(hintProps)}<span {...hintProps} class="truncate">{timezone}</span>{/snippet}</Hint>
      </footer>
    </Popover.Content>
  </Popover.Root>
</ToggleGroup.Root>
