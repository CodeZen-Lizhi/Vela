<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import * as Select from "$lib/components/ui/select";
  import { Trash2 } from "@lucide/svelte";
  import * as Field from "$lib/components/ui/field";
  import type { Rate } from "$lib/generated/mygo";
  import { rateNames, rateUnit, unitLabel, newRate } from "./display";
  /** 价格行编辑器；调用者持有完整草稿，切换维度同时更新价格单位。 */
  const prefix = $props.id();
  let {
    rates = $bindable<Rate[]>([]),
    readOnly = false,
  }: { rates: Rate[]; readOnly?: boolean } = $props();
</script>

<div class="flex flex-col gap-2">
  {#each rates as rate, i}
    <Field.FieldGroup class="rate-row">
      <Field.Field>
        <Field.Label for={`${prefix}-rate-key-${i}`}>计费维度</Field.Label>
        <Select.Root
          type="single"
          value={rate.key}
          disabled={readOnly}
          onValueChange={(value) => {
            rate.key = value;
            rate.unit = rateUnit(value);
          }}
        >
          <Select.Trigger id={`${prefix}-rate-key-${i}`} class="w-full"
            >{rateNames[rate.key] ?? rate.key}</Select.Trigger
          >
          <Select.Content
            ><Select.Group
              >{#each Object.entries(rateNames) as [value, label]}<Select.Item
                  {value}>{label}</Select.Item
                >{/each}</Select.Group
            ></Select.Content
          >
        </Select.Root>
      </Field.Field>
      <Field.Field>
        <Field.Label for={`${prefix}-rate-price-${i}`}
          >{unitLabel(rate.unit)}</Field.Label
        >
        <Input
          id={`${prefix}-rate-price-${i}`}
          type="number"
          min="0"
          step="any"
          required
          bind:value={rate.price}
          readonly={readOnly}
        />
      </Field.Field>
      {#if !readOnly}<Button
          variant="ghost"
          size="icon-sm"
          aria-label={`移除${rateNames[rate.key]}价格`}
          onclick={() => (rates = rates.filter((_, n) => n !== i))}
          ><Trash2 /></Button
        >{/if}
    </Field.FieldGroup>
  {/each}
  {#if rates.length === 0}<p class="text-sm text-muted-foreground">
      未提供，保持未知
    </p>{/if}
  {#if !readOnly}<Button
      variant="outline"
      size="sm"
      class="self-start"
      onclick={() =>
        (rates = [
          ...rates,
          newRate(
            Object.keys(rateNames).find(
              (k) => !rates.some((r) => r.key === k),
            ) ?? "input",
          ),
        ])}>添加价格项</Button
    >{/if}
</div>
