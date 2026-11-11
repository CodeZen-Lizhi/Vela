<script lang="ts">
  import * as Field from "$lib/components/ui/field";
  import * as RadioGroup from "$lib/components/ui/radio-group";
  import type { ProxyChoice } from "$lib/generated/mygo";
  /** 局部出口只保存跟随或直连选择；横向布局与供应商设置表单共用标签列。 */
  let { value = $bindable(), forKey = false, horizontal = false }: {
    /** 当前供应商或 Key 的出口草稿。 */
    value: ProxyChoice;
    /** Key 跟随供应商，供应商跟随全局。 */
    forKey?: boolean;
    /** 宽窗口按固定标签列排列，窄窗口由表单样式自动堆叠。 */
    horizontal?: boolean;
  } = $props();
  /** 实例身份供单选项标签和描述关联，避免多个弹窗重名。 */
  const id = $props.id();
  /** 两个选项表达互斥的出口配置，不复制上级地址。 */
  const options = $derived([
    { value: forKey ? "provider" : "global", label: forKey ? "跟随供应商" : "跟随全局" },
    { value: "direct", label: "不使用代理" },
  ]);
</script>
<Field.Field orientation={horizontal ? "horizontal" : "vertical"} class={horizontal ? "provider-setting-row" : ""}>
  <Field.Label id={`${id}-label`}>网络代理</Field.Label>
  <Field.Content>
    <RadioGroup.Root bind:value={() => value.mode, (mode) => value.mode = mode as ProxyChoice["mode"]} aria-labelledby={`${id}-label`} aria-describedby={forKey ? `${id}-description` : undefined} class="flex min-h-9 flex-wrap items-center gap-x-6 gap-y-3">
      {#each options as option}<div class="flex items-center gap-2"><RadioGroup.Item id={`${id}-${option.value}`} value={option.value}/><Field.Label for={`${id}-${option.value}`} class="font-normal text-xs">{option.label}</Field.Label></div>{/each}
    </RadioGroup.Root>
    {#if forKey}<Field.Description id={`${id}-description`}>跟随供应商的出口设置，或为当前 Key 单独直连。</Field.Description>{/if}
  </Field.Content>
</Field.Field>
