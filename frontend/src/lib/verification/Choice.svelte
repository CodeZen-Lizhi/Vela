<script lang="ts">
  import BaseChoice from "$lib/providers/Choice.svelte";
  /** 复用共享 Choice，以函数绑定向草稿所有者传递选择，不维护第二份状态。 */
  let {
    value,
    options,
    label,
    disabled = false,
    onValueChange,
  }: {
    /** 当前业务值，空串表示未选。 */ value: string;
    /** 稳定选项值与展示名称。 */ options: { value: string; label: string }[];
    /** 可访问名称与禁用状态。 */ label: string;
    disabled?: boolean;
    /** 用户选择后通知草稿所有者。 */ onValueChange: (value: string) => void;
  } = $props();
</script>

<BaseChoice
  bind:value={() => value, (next) => onValueChange(next)}
  {options}
  {label}
  {disabled}
/>
