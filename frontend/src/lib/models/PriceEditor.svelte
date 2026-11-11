<script lang="ts">
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import * as Accordion from "$lib/components/ui/accordion";
  import * as Select from "$lib/components/ui/select";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import * as Field from "$lib/components/ui/field";
  import type { Pricing } from "$lib/generated/mygo";
  import RateFields from "./RateFields.svelte";
  import { groupNames, newRate } from "./display";
  /** 完整价格草稿，按实际计费语义分组，恢复由父级统一处理。 */
  let {
    pricing = $bindable(),
    readOnly = false,
    overrides = [],
    onrestore,
    openGroups = $bindable<string[]>(["base"]),
  }: {
    pricing: Pricing;
    readOnly?: boolean;
    overrides?: string[];
    onrestore?: (group: string) => void;
    /** 父级校验失败时展开包含无效输入的规则组。 */
    openGroups?: string[];
  } = $props();
  /** 基础价格默认展开，其余规则按需查看和编辑。 */
  const prefix = $props.id();
  const weekdays = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"];
</script>

<Accordion.Root type="multiple" bind:value={openGroups}>
  {#each Object.entries(groupNames) as [group, label]}
    <Accordion.Item value={group} data-price-group={group}>
      <div class="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3">
        <Accordion.Trigger class="flex-1 py-3">
          <span>
            {label}
            {#if overrides.includes(group)}<span class="tag">本地修改</span
              >{/if}
          </span>
        </Accordion.Trigger>
        {#if !readOnly && onrestore}<Button
            variant="ghost"
            size="sm"
            onclick={() => onrestore?.(group)}>恢复来源值</Button
          >{/if}
      </div>
      <Accordion.Content>
        {#if group === "base"}
          <RateFields bind:rates={pricing.base} {readOnly} />
        {:else if group === "tiers"}
          <div class="flex flex-col gap-4">
            {#each pricing.tiers as tier, i}
              <div class="rule-block">
                <Field.Field
                  ><Field.Label for={`${prefix}-field-0-${i}`}
                    >输入上下文超过（Token）</Field.Label
                  ><Input
                    id={`${prefix}-field-0-${i}`}
                    type="number"
                    min="1"
                    required
                    bind:value={tier.above}
                    readonly={readOnly}
                  /></Field.Field
                >
                <RateFields bind:rates={tier.rates} {readOnly} />
                {#if !readOnly}<Button
                    variant="ghost"
                    size="sm"
                    onclick={() =>
                      (pricing.tiers = pricing.tiers.filter((_, n) => n !== i))}
                    >移除阶梯</Button
                  >{/if}
              </div>
            {/each}
          </div>
          {#if !readOnly}<Button
              variant="outline"
              size="sm"
              onclick={() =>
                (pricing.tiers = [
                  ...pricing.tiers,
                  { above: 200000, rates: [newRate()] },
                ])}>添加阶梯</Button
            >{/if}
        {:else if group === "modes"}
          {#each pricing.modes as mode, i}
            <div class="rule-block">
              <Field.FieldGroup class="grid gap-3 sm:grid-cols-2">
                <Field.Field
                  ><Field.Label for={`${prefix}-field-1-${i}`}
                    >处理模式</Field.Label
                  ><Select.Root
                    type="single"
                    bind:value={mode.name}
                    disabled={readOnly}
                    ><Select.Trigger
                      id={`${prefix}-field-1-${i}`}
                      class="w-full"
                      >{mode.name === "priority"
                        ? "Priority"
                        : "Fast"}</Select.Trigger
                    ><Select.Content
                      ><Select.Group
                        ><Select.Item value="fast">Fast</Select.Item
                        ><Select.Item value="priority">Priority</Select.Item
                        ></Select.Group
                      ></Select.Content
                    ></Select.Root
                  ></Field.Field
                >
                <Field.Field
                  ><Field.Label for={`${prefix}-field-2-${i}`}
                    >Standard 倍率（留空使用显式价格）</Field.Label
                  ><Input
                    id={`${prefix}-field-2-${i}`}
                    type="number"
                    min="0.000001"
                    step="any"
                    value={mode.multiplier ?? ""}
                    readonly={readOnly}
                    oninput={(e) => {
                      mode.multiplier =
                        e.currentTarget.value === ""
                          ? null
                          : Number(e.currentTarget.value);
                      if (mode.multiplier !== null) {
                        mode.rates = [];
                        mode.tiers = [];
                      }
                    }}
                  /></Field.Field
                >
              </Field.FieldGroup>
              {#if mode.multiplier === null}<RateFields
                  bind:rates={mode.rates}
                  {readOnly}
                />
                {#each mode.tiers as tier, n}<div class="rule-block">
                    <Field.Field
                      ><Field.Label for={`${prefix}-field-3-${i}-${n}`}
                        >模式上下文超过（Token）</Field.Label
                      ><Input
                        id={`${prefix}-field-3-${i}-${n}`}
                        type="number"
                        min="1"
                        required
                        bind:value={tier.above}
                        readonly={readOnly}
                      /></Field.Field
                    ><RateFields
                      bind:rates={tier.rates}
                      {readOnly}
                    />{#if !readOnly}<Button
                        variant="ghost"
                        size="sm"
                        onclick={() =>
                          (mode.tiers = mode.tiers.filter(
                            (_, index) => index !== n,
                          ))}>移除模式阶梯</Button
                      >{/if}
                  </div>{/each}
                {#if !readOnly}<Button
                    variant="outline"
                    size="sm"
                    onclick={() =>
                      (mode.tiers = [
                        ...mode.tiers,
                        { above: 200000, rates: [newRate()] },
                      ])}>添加模式阶梯</Button
                  >{/if}
              {/if}
              {#if !readOnly}<Button
                  variant="ghost"
                  size="sm"
                  onclick={() =>
                    (pricing.modes = pricing.modes.filter((_, n) => n !== i))}
                  >移除模式</Button
                >{/if}
            </div>
          {/each}
          {#if !readOnly}<Button
              variant="outline"
              size="sm"
              onclick={() =>
                (pricing.modes = [
                  ...pricing.modes,
                  { name: "fast", multiplier: 2, rates: [], tiers: [] },
                ])}>添加处理模式</Button
            >{/if}
        {:else if group === "schedules"}
          <Field.Field class="mb-3"
            ><Field.Label for={`${prefix}-field-4`}>基准价格口径</Field.Label
            ><Input
              id={`${prefix}-field-4`}
              placeholder="例如：低谷价格，高峰时段乘以下方倍率"
              bind:value={pricing.baseline}
              readonly={readOnly}
            /></Field.Field
          >
          {#each pricing.schedules as window, i}
            <div class="rule-block">
              <Field.FieldGroup class="grid gap-3 sm:grid-cols-2">
                <Field.Field
                  ><Field.Label for={`${prefix}-field-5-${i}`}>时区</Field.Label
                  ><Input
                    id={`${prefix}-field-5-${i}`}
                    bind:value={window.timezone}
                    readonly={readOnly}
                    required
                  /></Field.Field
                >
                <Field.Field
                  ><Field.Label for={`${prefix}-field-6-${i}`}
                    >时段倍率</Field.Label
                  ><Input
                    id={`${prefix}-field-6-${i}`}
                    type="number"
                    min="0.000001"
                    step="any"
                    bind:value={window.multiplier}
                    readonly={readOnly}
                    required
                  /></Field.Field
                >
                <Field.Field
                  ><Field.Label for={`${prefix}-field-7-${i}`}
                    >开始（包含）</Field.Label
                  ><Input
                    id={`${prefix}-field-7-${i}`}
                    type="time"
                    bind:value={window.start}
                    readonly={readOnly}
                    required
                  /></Field.Field
                >
                <Field.Field
                  ><Field.Label for={`${prefix}-field-8-${i}`}
                    >结束（不包含，可填 24:00）</Field.Label
                  ><Input
                    id={`${prefix}-field-8-${i}`}
                    placeholder="18:00"
                    bind:value={window.end}
                    readonly={readOnly}
                    required
                  /></Field.Field
                >
              </Field.FieldGroup>
              <fieldset class="flex flex-wrap gap-3">
                <legend class="mb-2 text-sm">生效日期</legend
                >{#each weekdays as day, d}<label
                    class="flex items-center gap-1 text-sm"
                    ><Checkbox
                    aria-label={day}
                      checked={window.days.includes(d)}
                      disabled={readOnly}
                      onCheckedChange={(checked) =>
                        (window.days = checked
                          ? [...window.days, d]
                          : window.days.filter((n) => n !== d))}
                    />{day}</label
                  >{/each}
              </fieldset>
              <Field.Field
                ><Field.Label for={`${prefix}-field-9-${i}`}
                  >例外日期（YYYY-MM-DD，逗号分隔）</Field.Label
                ><Input
                  id={`${prefix}-field-9-${i}`}
                  value={(window.excludedDates ?? []).join(", ")}
                  readonly={readOnly}
                  oninput={(e) =>
                    (window.excludedDates = e.currentTarget.value
                      .split(",")
                      .map((v) => v.trim())
                      .filter(Boolean))}
                /></Field.Field
              >
              {#if !readOnly}<Button
                  variant="ghost"
                  size="sm"
                  onclick={() =>
                    (pricing.schedules = pricing.schedules.filter(
                      (_, n) => n !== i,
                    ))}>移除时段</Button
                >{/if}
            </div>
          {/each}
          {#if !readOnly}<Button
              variant="outline"
              size="sm"
              onclick={() =>
                (pricing.schedules = [
                  ...pricing.schedules,
                  {
                    timezone: "Asia/Shanghai",
                    days: [1, 2, 3, 4, 5],
                    start: "09:00",
                    end: "18:00",
                    multiplier: 2,
                    excludedDates: [],
                  },
                ])}>添加时段</Button
            >{/if}
        {:else if group === "images"}
          {#each pricing.images as image, i}<div class="rule-block">
              <Field.FieldGroup class="grid gap-3 sm:grid-cols-2">
                <Field.Field
                  ><Field.Label for={`${prefix}-field-10-${i}`}
                    >尺寸</Field.Label
                  ><Input
                    id={`${prefix}-field-10-${i}`}
                    placeholder="1024x1024"
                    bind:value={image.size}
                    readonly={readOnly}
                  /></Field.Field
                >
                <Field.Field
                  ><Field.Label for={`${prefix}-field-11-${i}`}
                    >质量</Field.Label
                  ><Input
                    id={`${prefix}-field-11-${i}`}
                    placeholder="standard"
                    bind:value={image.quality}
                    readonly={readOnly}
                  /></Field.Field
                >
                <Field.Field
                  ><Field.Label for={`${prefix}-field-12-${i}`}
                    >最小像素（可选）</Field.Label
                  ><Input
                    id={`${prefix}-field-12-${i}`}
                    type="number"
                    min="0"
                    bind:value={image.minPixels}
                    readonly={readOnly}
                  /></Field.Field
                >
                <Field.Field
                  ><Field.Label for={`${prefix}-field-13-${i}`}
                    >最大像素（可选）</Field.Label
                  ><Input
                    id={`${prefix}-field-13-${i}`}
                    type="number"
                    min="0"
                    bind:value={image.maxPixels}
                    readonly={readOnly}
                  /></Field.Field
                >
                <Field.Field
                  ><Field.Label for={`${prefix}-field-14-${i}`}
                    >美元 / 张</Field.Label
                  ><Input
                    id={`${prefix}-field-14-${i}`}
                    type="number"
                    min="0"
                    step="any"
                    bind:value={image.price}
                    readonly={readOnly}
                    required
                  /></Field.Field
                >
              </Field.FieldGroup>{#if !readOnly}<Button
                  variant="ghost"
                  size="sm"
                  onclick={() =>
                    (pricing.images = pricing.images.filter((_, n) => n !== i))}
                  >移除图片档位</Button
                >{/if}
            </div>{/each}
          {#if !readOnly}<Button
              variant="outline"
              size="sm"
              onclick={() =>
                (pricing.images = [
                  ...pricing.images,
                  {
                    size: "1024x1024",
                    quality: "standard",
                    minPixels: 0,
                    maxPixels: 0,
                    price: 0,
                  },
                ])}>添加图片档位</Button
            >{/if}
        {:else if group === "videos"}
          {#each pricing.videos as video, i}<div class="rule-block">
              <Field.FieldGroup class="grid gap-3 sm:grid-cols-2">
                <Field.Field
                  ><Field.Label for={`${prefix}-field-15-${i}`}
                    >分辨率</Field.Label
                  ><Input
                    id={`${prefix}-field-15-${i}`}
                    placeholder="1080p"
                    bind:value={video.resolution}
                    readonly={readOnly}
                    required
                  /></Field.Field
                >
                <Field.Field
                  ><Field.Label for={`${prefix}-field-16-${i}`}
                    >美元 / 秒</Field.Label
                  ><Input
                    id={`${prefix}-field-16-${i}`}
                    type="number"
                    min="0"
                    step="any"
                    bind:value={video.price}
                    readonly={readOnly}
                    required
                  /></Field.Field
                >
              </Field.FieldGroup>{#if !readOnly}<Button
                  variant="ghost"
                  size="sm"
                  onclick={() =>
                    (pricing.videos = pricing.videos.filter((_, n) => n !== i))}
                  >移除视频档位</Button
                >{/if}
            </div>{/each}
          {#if !readOnly}<Button
              variant="outline"
              size="sm"
              onclick={() =>
                (pricing.videos = [
                  ...pricing.videos,
                  { resolution: "1080p", price: 0 },
                ])}>添加视频档位</Button
            >{/if}
        {/if}
        {#if group !== "base" && group !== "schedules" && pricing[group as "tiers" | "modes" | "images" | "videos"].length === 0}<p
            class="mt-2 text-sm text-muted-foreground"
          >
            未提供，保持未知
          </p>{/if}
      </Accordion.Content>
    </Accordion.Item>
  {/each}
</Accordion.Root>
