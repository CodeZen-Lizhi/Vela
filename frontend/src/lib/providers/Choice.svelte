<script lang="ts">
  import * as Select from "$lib/components/ui/select";
  /** 供应商表单内统一的单选控件；选项值由所属业务字段约束。 */
  let { value = $bindable(""), options, label, disabled = false }: {
    value: string;
    options: { value: string; label: string }[];
    label: string;
    disabled?: boolean;
  } = $props();
  /** 下拉关闭时显示当前选项的中文名称。 */
  const selectedLabel = $derived(options.find((option) => option.value === value)?.label ?? "请选择");
</script>
<Select.Root type="single" bind:value {disabled}>
  <Select.Trigger aria-label={label} class="w-full"><span class="min-w-0 truncate" title={selectedLabel}>{selectedLabel}</span></Select.Trigger>
  <Select.Content><Select.Group>
    {#each options as option}<Select.Item value={option.value} label={option.label}>{option.label}</Select.Item>{/each}
  </Select.Group></Select.Content>
</Select.Root>
