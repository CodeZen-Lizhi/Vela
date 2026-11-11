<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import * as Select from "$lib/components/ui/select";
  import { Trash2 } from "@lucide/svelte";
  import * as Field from "$lib/components/ui/field";
  import type { Rate } from "$lib/generated/mygo";
  import { rateNames, unitLabel, newRate, money } from "./display";
  import { cacheMultiplier, updateRate } from "./rate-edit";

  /** 同档价格横向编辑；可换算缓存显示倍率，零或未知输入价时保留单价。手动模型允许补充维度。 */
  let { rates = $bindable<Rate[]>([]), readOnly = false, allowAdd = false, kind = "chat" }:
    { rates: Rate[]; readOnly?: boolean; allowAdd?: boolean; kind?: string } = $props();
  /** 实例表单标识与待添加维度，不改变已存在价格项的身份。 */
  const prefix = $props.id();
  let addition = $state("");
  /** 按模型类型限制新增维度；已有数据始终保留显示。 */
  const available = $derived(Object.entries(rateNames).filter(([key]) =>
    !rates.some((rate) => rate.key === key) &&
    (kind === "image" ? ["input", "image_input", "image_output", "per_image"].includes(key)
      : kind === "video" ? ["input", "per_second"].includes(key)
      : kind === "vector" ? ["input", "per_request"].includes(key)
      : !["per_image", "per_second"].includes(key))));
</script>

<Field.FieldGroup class="rate-fields">
  {#each rates as rate, i}
    {@const multiplier = cacheMultiplier(rate, rates)}
    <Field.Field class="min-w-0 gap-1.5">
      <Field.Label for={`${prefix}-price-${i}`}>
        {rate.key === "cache_write" && !rates.some((item) => item.key === "cache_write_1h") ? "缓存创建" : rateNames[rate.key] ?? rate.key}
        <span class="text-muted-foreground">（{multiplier !== null ? "倍率" : rate.unit === "million_tokens" ? "$/M" : unitLabel(rate.unit)}）</span>
      </Field.Label>
      <div class="flex items-center gap-1">
        <Input id={`${prefix}-price-${i}`} type="number" min="0" step="any" required
          value={multiplier === null ? rate.price : Number(multiplier.toPrecision(12))} readonly={readOnly}
          oninput={(event) => {
            const value = event.currentTarget.valueAsNumber;
            if (Number.isFinite(value) && value >= 0) updateRate(rates, rate, value);
          }} />
        {#if allowAdd && !readOnly}<Button variant="ghost" size="icon-xs" aria-label={`移除${rateNames[rate.key]}价格`}
          onclick={() => (rates = rates.filter((_, n) => n !== i))}><Trash2 /></Button>{/if}
      </div>
      {#if multiplier !== null}<span class="text-xs text-muted-foreground">{money(rate.price)} / 百万 Token</span>{/if}
    </Field.Field>
  {/each}
</Field.FieldGroup>
{#if rates.length === 0}<p class="text-xs text-muted-foreground">未提供价格，保持未知</p>{/if}
{#if rates.some((rate) => cacheMultiplier(rate, rates) !== null)}<p class="mt-2 text-xs text-muted-foreground">缓存倍率以当前档输入单价为基准</p>{/if}
{#if allowAdd && !readOnly && available.length}
  <div class="mt-3 flex items-center gap-2">
    <Select.Root type="single" bind:value={addition}><Select.Trigger aria-label="新增价格维度">{rateNames[addition] ?? "选择价格项"}</Select.Trigger><Select.Content><Select.Group>
      {#each available as [value, label]}<Select.Item {value}>{label}</Select.Item>{/each}
    </Select.Group></Select.Content></Select.Root>
    <Button variant="outline" size="sm" disabled={!available.some(([key]) => key === addition)} onclick={() => {rates = [...rates, newRate(addition)]; addition = "";}}>添加价格项</Button>
  </div>
{/if}

<style>
  :global(.rate-fields) { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 12px; align-items: start; }
  @media (max-width: 640px) { :global(.rate-fields) { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
</style>
