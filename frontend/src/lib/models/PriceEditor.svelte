<script lang="ts">
  import { Label } from "$lib/components/ui/label";
  import * as InputGroup from "$lib/components/ui/input-group";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import * as Accordion from "$lib/components/ui/accordion";
  import * as Select from "$lib/components/ui/select";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Switch } from "$lib/components/ui/switch";
  import * as Field from "$lib/components/ui/field";
  import type { Mode, Pricing, Rate } from "$lib/generated/mygo";
  import RateFields from "./RateFields.svelte";
  import { groupNames, newRate } from "./display";
  /** 完整价格草稿，按实际计费语义分组，恢复由父级统一处理。 */
  let {
    pricing = $bindable(),
    readOnly = false,
    overrides = [],
    kind,
    source,
    manual = false,
    onrestore,
    openGroups = $bindable<string[]>(["base"]),
  }: {
    pricing: Pricing;
    /** 模型类型限制新建规则；来源快照使删除或恢复后仍能找回适用规则组。 */
    kind: string;
    source: Pricing;
    manual?: boolean;
    readOnly?: boolean;
    overrides?: string[];
    onrestore?: (group: string) => void;
    /** 父级校验失败时展开包含无效输入的规则组。 */
    openGroups?: string[];
  } = $props();
  /** 基础价格默认展开，其余规则按需查看和编辑。 */
  const prefix = $props.id();
  /** 只显示来源或本地已存在的规则；手动补充独立收起，避免空规则占据创建页。 */
  let supplement = $state(false);
  const visibleGroups = $derived(Object.entries(groupNames).filter(([group]) => {
    if (group === "base") return true;
    const key = group as "tiers" | "modes" | "schedules" | "images" | "videos";
    if (pricing[key].length || source[key].length) return true;
    return manual && supplement && (group === "images" ? kind === "image" : group === "videos" ? kind === "video" : ["tiers", "modes"].includes(group) ? kind === "chat" : true);
  }));
  const weekdays = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"];

  /** 当前编辑会话使用倍率的处理模式；独立于字段值，清空倍率时仍保持倍率模式。 */
  const multiplierModes = new WeakMap<Mode, boolean>();
  /** WeakMap 变化需要显式触发模板刷新，避免开关状态与字段显示不一致。 */
  let modePricingRevision = $state(0);
  /** 暂存切换前被隐藏的倍率值，关闭开关后再次开启可恢复。 */
  const multiplierDrafts = new WeakMap<Mode, number | null>();
  /** 暂存切换前被隐藏的显式价格与阶梯，关闭开关时原样恢复。 */
  const explicitDrafts = new WeakMap<Mode, Pick<Mode, "rates" | "tiers">>();

  /** 返回处理模式当前是否使用相对 Standard 倍率；已有来源值默认按其实际口径显示。 */
  function usesMultiplier(mode: Mode): boolean {
    void modePricingRevision;
    return multiplierModes.get(mode) ?? mode.multiplier !== null;
  }

  /** 在倍率与自定义价格之间切换，保留两侧草稿且只向持久化模型提交当前口径。 */
  function setMultiplierMode(mode: Mode, enabled: boolean): void {
    if (readOnly || usesMultiplier(mode) === enabled) return;
    if (enabled) {
      explicitDrafts.set(mode, {
        rates: structuredClone($state.snapshot(mode.rates)),
        tiers: structuredClone($state.snapshot(mode.tiers)),
      });
      mode.rates = [];
      mode.tiers = [];
      mode.multiplier = multiplierDrafts.get(mode) ?? null;
    } else {
      multiplierDrafts.set(mode, mode.multiplier);
      mode.multiplier = null;
      const explicit = explicitDrafts.get(mode);
      mode.rates =
        explicit?.rates ??
        structuredClone($state.snapshot(pricing.base)).filter(
          (rate: Rate) => rate.unit === "million_tokens",
        );
      mode.tiers = explicit?.tiers ?? [];
    }
    multiplierModes.set(mode, enabled);
    modePricingRevision += 1;
  }
</script>

<Accordion.Root type="multiple" bind:value={openGroups}>
  {#each visibleGroups as [group, label]}
    <Accordion.Item value={group} data-price-group={group}>
      <div class="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3">
        <Accordion.Trigger class="flex-1 py-3">
          <span>
            {group === "modes" ? "处理模式" : label}
            {#if overrides.includes(group)}<Badge variant="neutral">本地修改</Badge>{/if}
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
          <RateFields bind:rates={pricing.base} {readOnly} allowAdd={manual} {kind} />
        {:else if group === "tiers"}
          <div class="flex flex-col gap-4">
            {#each pricing.tiers as tier, i}
              <div class="rule-block">
                <Field.Field class="max-w-64"
                  ><Field.Label for={`${prefix}-field-0-${i}`}
                    >输入上下文超过（K Token）</Field.Label
                  ><Input
                    id={`${prefix}-field-0-${i}`}
                    type="number"
                    min="0.001"
                    required
                    step="0.001"
                    value={tier.above / 1000}
                    oninput={(event) => { if (event.currentTarget.value !== "") tier.above = Math.round(event.currentTarget.valueAsNumber * 1000); }}
                    readonly={readOnly}
                  /></Field.Field
                >
                <RateFields bind:rates={tier.rates} {readOnly} allowAdd={manual} {kind} />
                {#if !readOnly && manual}<Button
                    variant="ghost"
                    size="sm"
                    onclick={() =>
                      (pricing.tiers = pricing.tiers.filter((_, n) => n !== i))}
                    >移除阶梯</Button
                  >{/if}
              </div>
            {/each}
          </div>
          {#if !readOnly && manual}<Button
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
              <Field.FieldGroup
                class="flex flex-row flex-wrap items-start gap-3"
              >
                <Field.Field class="w-36 shrink-0"
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
                <Field.Field class="w-auto shrink-0 gap-2">
                  <Field.Label>计费方式</Field.Label>
                  <div class="mode-pricing-controls">
                    <div
                      class="mode-multiplier-toggle"
                      class:active={usesMultiplier(mode)}
                    >
                      <span>
                        <Field.Label for={`${prefix}-field-2-${i}`}
                          >Standard 倍率</Field.Label
                        ><small
                          >{usesMultiplier(mode)
                            ? "按倍率计算"
                            : "使用自定义价格"}</small
                        >
                      </span>
                      <Switch
                        id={`${prefix}-field-2-${i}`}
                        checked={usesMultiplier(mode)}
                        disabled={readOnly}
                        aria-label={`${mode.name === "priority"
                          ? "Priority"
                          : "Fast"} 使用 Standard 倍率`}
                        onCheckedChange={(enabled) =>
                          setMultiplierMode(mode, enabled)}
                      />
                    </div>
                    {#if usesMultiplier(mode)}<InputGroup.Root
                        class="mode-multiplier-input"
                      >
                        <InputGroup.Input
                          type="number"
                          min="0.000001"
                          step="any"
                          required
                          placeholder="例如 2"
                          aria-label={`${mode.name === "priority"
                            ? "Priority"
                            : "Fast"} Standard 倍率`}
                          value={mode.multiplier ?? ""}
                          readonly={readOnly}
                          oninput={(e) => {
                            const value = e.currentTarget.valueAsNumber;
                            mode.multiplier = Number.isFinite(value)
                              ? value
                              : null;
                          }}
                        />
                        <InputGroup.Addon align="inline-end">×</InputGroup.Addon>
                      </InputGroup.Root>{/if}
                  </div>
                </Field.Field>
              </Field.FieldGroup>
              {#if !usesMultiplier(mode)}<RateFields
                  bind:rates={mode.rates}
                  {readOnly} allowAdd={manual} {kind}
                />
                {#each mode.tiers as tier, n}<div class="rule-block">
                    <Field.Field
                      ><Field.Label for={`${prefix}-field-3-${i}-${n}`}
                        >模式上下文超过（K Token）</Field.Label
                      ><Input
                        id={`${prefix}-field-3-${i}-${n}`}
                        type="number"
                        min="0.001"
                        required
                        step="0.001"
                    value={tier.above / 1000}
                    oninput={(event) => { if (event.currentTarget.value !== "") tier.above = Math.round(event.currentTarget.valueAsNumber * 1000); }}
                        readonly={readOnly}
                      /></Field.Field
                    ><RateFields
                      bind:rates={tier.rates}
                      {readOnly} allowAdd={manual} {kind}
                    />{#if !readOnly && manual}<Button
                        variant="ghost"
                        size="sm"
                        onclick={() =>
                          (mode.tiers = mode.tiers.filter(
                            (_, index) => index !== n,
                          ))}>移除模式阶梯</Button
                      >{/if}
                  </div>{/each}
                {#if !readOnly && manual}<Button
                    variant="outline"
                    size="sm"
                    onclick={() =>
                      (mode.tiers = [
                        ...mode.tiers,
                        { above: 200000, rates: [newRate()] },
                      ])}>添加模式阶梯</Button
                  >{/if}
              {/if}
              {#if !readOnly && manual}<Button
                  variant="ghost"
                  size="sm"
                  onclick={() =>
                    (pricing.modes = pricing.modes.filter((_, n) => n !== i))}
                  >移除模式</Button
                >{/if}
            </div>
          {/each}
          {#if !readOnly && manual}<Button
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
              <Field.Set class="flex flex-row flex-wrap gap-3">
                <Field.Legend class="mb-2 text-sm">生效日期</Field.Legend
                >{#each weekdays as day, d}<Label variant="content"
                    class="flex items-center gap-1 text-sm"
                    ><Checkbox
                    aria-label={day}
                      checked={window.days.includes(d)}
                      disabled={readOnly}
                      onCheckedChange={(checked) =>
                        (window.days = checked
                          ? [...window.days, d]
                          : window.days.filter((n) => n !== d))}
                    />{day}</Label
                  >{/each}
              </Field.Set>
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
              {#if !readOnly && manual}<Button
                  variant="ghost"
                  size="sm"
                  onclick={() =>
                    (pricing.schedules = pricing.schedules.filter(
                      (_, n) => n !== i,
                    ))}>移除时段</Button
                >{/if}
            </div>
          {/each}
          {#if !readOnly && manual}<Button
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
              </Field.FieldGroup>{#if !readOnly && manual}<Button
                  variant="ghost"
                  size="sm"
                  onclick={() =>
                    (pricing.images = pricing.images.filter((_, n) => n !== i))}
                  >移除图片档位</Button
                >{/if}
            </div>{/each}
          {#if !readOnly && manual}<Button
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
              </Field.FieldGroup>{#if !readOnly && manual}<Button
                  variant="ghost"
                  size="sm"
                  onclick={() =>
                    (pricing.videos = pricing.videos.filter((_, n) => n !== i))}
                  >移除视频档位</Button
                >{/if}
            </div>{/each}
          {#if !readOnly && manual}<Button
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

{#if manual && !readOnly}<Button variant="ghost" size="sm" class="mt-2" onclick={() => (supplement = !supplement)}>{supplement ? "收起补充规则" : "补充价格规则"}</Button>{/if}

<style>
  /* 倍率输入与胶囊开关同排，避免短下拉框被拉到半行宽。 */
  .mode-pricing-controls {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
  }
  /* 胶囊开关同时显示当前计价口径，避免用户误把空倍率理解为未知价格。 */
  .mode-multiplier-toggle {
    display: flex;
    width: 210px;
    min-height: 36px;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 4px 7px 4px 13px;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--muted);
    transition:
      border-color 160ms ease,
      background-color 160ms ease;
  }
  .mode-multiplier-toggle > span {
    display: flex;
    min-width: 0;
    flex-direction: column;
    gap: 1px;
  }

  .mode-multiplier-toggle small {
    color: var(--muted-foreground);
    font-size: 11px;
    line-height: 1.2;
  }
  .mode-multiplier-toggle.active {
    border-color: color-mix(in oklch, var(--primary) 35%, var(--border));
    background: color-mix(in oklch, var(--primary) 7%, var(--background));
  }
  :global(.mode-multiplier-input) {
    position: relative;
    flex: 0 0 104px;
  }

</style>
