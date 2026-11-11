<script lang="ts">
  import * as Field from "$lib/components/ui/field";
  import { Input } from "$lib/components/ui/input";
  import type { ProxyChoice } from "$lib/generated/mygo";
  import Choice from "./Choice.svelte";
  /** 出口只维护当前记录的选择，跟随模式不复制上级地址。 */
  let { value = $bindable(), forKey = false }: { value: ProxyChoice; forKey?: boolean } = $props();
  /** Key 默认跟随供应商，供应商默认跟随全局；二者均可直接指定出口。 */
  const options = $derived([
    { value: forKey ? "provider" : "global", label: forKey ? "跟随供应商" : "跟随全局" },
    { value: "direct", label: "直连" },
    { value: "custom", label: "自定义代理地址" },
  ]);
</script>
<Field.Field>
  <Field.Label>网络代理</Field.Label>
  <Choice bind:value={value.mode} {options} label="网络代理方式" />
  {#if value.mode === "custom"}<Input aria-label="自定义代理地址" bind:value={value.url} placeholder="http://127.0.0.1:7890" required />{/if}
</Field.Field>
