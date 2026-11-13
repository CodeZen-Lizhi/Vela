<script lang="ts">
  import { Hint } from "$lib/components/ui/tooltip";
  import * as Select from "$lib/components/ui/select";
  /** 列头与顶部使用同一个 shadcn-svelte 菜单；共享 openID 保证一次只打开一个。 */
  let {
    id,
    title,
    options,
    multiple = false,
    column = false,
    value = $bindable(""),
    values = $bindable([]),
    openID = $bindable(""),
    disabled = false,
  }: {
    /** 控件身份、可访问名称与可选值。 */ id: string;
    title: string;
    options: { value: string; label: string }[];
    /** 多选时绑定 values，单选绑定 value；空单选为全部。 */ multiple?: boolean;
    /** 列头使用共享 Select 紧凑变体，窗口缩放时保持同一入口。 */ column?: boolean;
    value?: string;
    values?: string[];
    /** 所有筛选共用打开身份；读取前禁用避免空数据假象。 */ openID?: string;
    disabled?: boolean;
  } = $props();
  /** 列头保留字段名，以高亮与悬停说明展示已选条件。 */
  const selected = $derived(multiple ? values.length > 0 : value !== "");
  const selectionLabel = $derived(multiple
    ? values.map((item) => options.find((option) => option.value === item)?.label ?? item).join("、")
    : (options.find((item) => item.value === value)?.label ?? value));
</script>

{#if multiple}
  <Select.Root
    type="multiple"
    bind:value={values}
    open={openID === id}
    onOpenChange={(open) => (openID = open ? id : "")}
    {disabled}
  >
    <Hint text={column ? (selected ? `${title}：${selectionLabel}` : `筛选${title}`) : undefined}>{#snippet children(hintProps0)}<Select.Trigger {...hintProps0}
      variant={column ? "column" : "default"}
      data-filter-active={selected}

      aria-label={`${title}筛选`}
      ><span class="min-w-0 truncate"
        >{title}{!column && values.length ? ` · ${values.length}` : ""}</span
      ></Select.Trigger
    >{/snippet}</Hint>
    <Select.Content align="start"
      ><Select.Group
        >{#each options as item}<Select.Item
            value={item.value}
            label={item.label}>{item.label}</Select.Item
          >{/each}</Select.Group
      ></Select.Content
    >
  </Select.Root>
{:else}
  <Select.Root
    type="single"
    bind:value
    open={openID === id}
    onOpenChange={(open) => (openID = open ? id : "")}
    {disabled}
  >
    <Hint text={column ? (selected ? `${title}：${selectionLabel}` : `筛选${title}`) : undefined}>{#snippet children(hintProps1)}<Select.Trigger {...hintProps1}
      variant={column ? "column" : "default"}
      data-filter-active={selected}

      aria-label={`${title}筛选`}
      ><span class="min-w-0 truncate"
        >{column || !value ? title : selectionLabel}</span
      ></Select.Trigger
    >{/snippet}</Hint>
    <Select.Content align="start"
      ><Select.Group
        ><Select.Item value="" label={`全部${title}`}>全部{title}</Select.Item
        >{#each options.filter((item) => item.value) as item}<Select.Item
            value={item.value}
            label={item.label}>{item.label}</Select.Item
          >{/each}</Select.Group
      ></Select.Content
    >
  </Select.Root>
{/if}
