<script lang="ts">
  import type { Model, Rate, Tier } from "$lib/generated/mygo";
  import { Badge } from "$lib/components/ui/badge";
  import { Separator } from "$lib/components/ui/separator";
  import * as Table from "$lib/components/ui/table";
  import ModelCapabilities from "./ModelCapabilities.svelte";
  import PriceSummary from "./PriceSummary.svelte";
  import {
    dateLabel,
    kinds,
    modelInitial,
    rateNames,
    unitLabel,
  } from "./display";

  /** 只读详情按原型展示身份、能力与已提供价格，不复用编辑表单。 */
  let { model }: { model: Model } = $props();
  /** 当前本地有效规则，包含已保存的修改。 */
  const pricing = $derived(model.pricing);
  /** 生效星期使用与后端相同的周日到周六顺序。 */
  const weekdays = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"];

  /** 与价格概览保持两位美元小数，小额单价保留有效精度。 */
  function money(value: number): string {
    return `$${value.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 8 })}`;
  }

  /** Token 上限使用紧凑单位，未知值由调用处明确展示。 */
  function tokens(value: number): string {
    return value >= 1_000_000
      ? `${Number((value / 1_000_000).toFixed(2))}M`
      : value >= 1000
        ? `${Number((value / 1000).toFixed(2))}K`
        : String(value);
  }
  /** 阶梯下界不包含，上界包含；最后一档没有上界。 */
  function tierRange(tiers: Tier[], index: number): string {
    const lower = tiers[index]!.above;
    const next = tiers[index + 1];
    return next
      ? `> ${tokens(lower)} 且 ≤ ${tokens(next.above)}`
      : `> ${tokens(lower)}`;
  }
</script>

{#snippet rates(items: Rate[])}
  <dl class="detail-rates">
    {#each items as rate}
      <div>
        <dt>{rateNames[rate.key] ?? rate.key}</dt>
        <dd>{money(rate.price)}</dd>
        <small>{unitLabel(rate.unit)}</small>
      </div>
    {/each}
  </dl>
{/snippet}

{#snippet tiers(items: Tier[])}
  {#each items as tier, index}
    <div class="detail-tier">
      <p>{tierRange(items, index)} Token</p>
      {@render rates(tier.rates)}
    </div>
  {/each}
{/snippet}

<div class="model-details">
  <header class="detail-identity">
    <span
      class="model-brand"
      data-brand={model.canonicalId.split("/")[0]}
      aria-hidden="true">{modelInitial(
        model.name,
        model.providerName,
        !model.providerId,
      )}</span
    >
    <div>
      <p class="detail-eyebrow">{model.providerName}</p>
      <h2>{model.name}</h2>
      <code>{model.modelId}</code>
    </div>
  </header>

  <dl class="detail-metadata">
    <div>
      <dt>模型类型</dt>
      <dd>
        <Badge variant="outline">{kinds[model.kind] ?? model.kind}</Badge>
      </dd>
    </div>
    <div>
      <dt>上下文</dt>
      <dd
        title={model.context
          ? `${model.context.toLocaleString()} Token`
          : undefined}
      >
        {model.context ? tokens(model.context) : "未知"}
      </dd>
    </div>
    <div>
      <dt>最大输出</dt>
      <dd>未知</dd>
    </div>
    <div>
      <dt>价格来源</dt>
      <dd>
        {model.providerId ? `models.dev · ${model.providerName}` : '手动创建'}{model.providerId && !model.sourceAvailable
          ? "（来源已下架，保留本地配置）"
          : ""}
      </dd>
    </div>
    <div>
      <dt>系列</dt>
      <dd>{model.family || "未知"}</dd>
    </div>
    <div>
      <dt>最近同步</dt>
      <dd>{dateLabel(model.syncedAt)}</dd>
    </div>
  </dl>

  <Separator orientation="horizontal" class="h-px" />
  {#if model.capabilities.length}
    <section
      class="detail-capabilities"
      aria-labelledby="detail-capabilities-title"
    >
      <h3 id="detail-capabilities-title">模型能力</h3>
      <ModelCapabilities values={model.capabilities} />
    </section>
  {/if}

  <section class="detail-pricing" aria-labelledby="detail-price-title">
    <header class="detail-price-heading">
      <h3 id="detail-price-title">目录价格</h3>
      <PriceSummary
        {pricing}
        kind={model.kind}
        modified={model.overrides.length > 0}
      />
    </header>
    {#if pricing.base.length}
      {@render rates(pricing.base)}
    {:else}
      <p class="detail-note">
        未提供基础单价；其余已提供规则见下方，缺失值保持未知。
      </p>
    {/if}

    {#if pricing.base.some((rate) => rate.unit === "million_tokens")}
      <ol class="detail-billing" aria-label="价格规则顺序">
        <li>
          {pricing.tiers.length ? "按完整输入量选择上下文档位" : "取基础单价"}
        </li>
        {#if pricing.modes.length}<li>
            按 Fast / Priority 模式取倍率或显式价格
          </li>{/if}
        {#if pricing.schedules.length}<li>按请求时刻应用分时倍率</li>{/if}
      </ol>
    {/if}

    {#if pricing.tiers.length || pricing.modes.length}
      <div class="detail-rule-grid">
        {#if pricing.tiers.length}
          <section class="detail-rule">
            <header>
              <h4>上下文阶梯</h4>
              <p>完整输入量命中一档，单位为 Token</p>
            </header>
            <p class="detail-note">
              ≤ {tokens(pricing.tiers[0]!.above)} Token 使用基础价格。
            </p>
            {@render tiers(pricing.tiers)}
          </section>
        {/if}
        {#if pricing.modes.length}
          <section class="detail-rule">
            <header>
              <h4>请求处理模式</h4>
              <p>同一次请求选择一种处理方式</p>
            </header>
            {#each pricing.modes as mode}
              <div class="detail-mode">
                <div class="detail-rule-title">
                  <strong>{mode.name}</strong
                  >{#if mode.multiplier !== null}<Badge variant="secondary"
                      >{mode.multiplier}×</Badge
                    >{:else}<span>显式价格</span>{/if}
                </div>
                {#if mode.rates.length}{@render rates(mode.rates)}{/if}
                {#if mode.tiers.length}{@render tiers(mode.tiers)}{/if}
              </div>
            {/each}
          </section>
        {/if}
      </div>
    {/if}

    {#if pricing.schedules.length}
      <section class="detail-rule detail-rule-inset">
        <header>
          <h4>时段价格</h4>
          <p>{pricing.baseline || "基础价格"} · 未命中时段使用 1×</p>
        </header>
        <div class="detail-periods">
          {#each pricing.schedules as window}
            <div>
              <div class="detail-rule-title">
                <strong>{window.start}–{window.end}</strong><Badge
                  variant="secondary">{window.multiplier}×</Badge
                >
              </div>
              <p>
                {window.timezone} · {window.days.length === 7
                  ? "每天"
                  : window.days.map((day) => weekdays[day]).join("、")}
              </p>
              {#if window.excludedDates.length}<p>
                  排除日期：{window.excludedDates.join("、")}
                </p>{/if}
            </div>
          {/each}
        </div>
      </section>
    {/if}

    {#if pricing.images.length}
      <section class="detail-rule detail-rule-inset">
        <header>
          <h4>按张计费矩阵</h4>
          <p>按尺寸、质量或像素区间计价 · 美元 / 张</p>
        </header>
        <Table.Root>
          <Table.Header
            ><Table.Row
              ><Table.Head>尺寸 / 像素区间</Table.Head><Table.Head
                >质量</Table.Head
              ><Table.Head data-align="number">单价</Table.Head></Table.Row
            ></Table.Header
          >
          <Table.Body
            >{#each pricing.images as row}<Table.Row
                ><Table.Cell
                  >{row.size ||
                    `${row.minPixels || 0}–${row.maxPixels || "不限"} px`}</Table.Cell
                ><Table.Cell>{row.quality || "未指定"}</Table.Cell><Table.Cell
                  data-align="number">{money(row.price)}</Table.Cell
                ></Table.Row
              >{/each}</Table.Body
          >
        </Table.Root>
      </section>
    {/if}
    {#if pricing.videos.length}
      <section class="detail-rule detail-rule-inset">
        <header>
          <h4>视频分辨率价</h4>
          <p>按分辨率与生成秒数计价 · 美元 / 秒</p>
        </header>
        <Table.Root>
          <Table.Header
            ><Table.Row
              ><Table.Head>分辨率</Table.Head><Table.Head data-align="number"
                >每秒价格</Table.Head
              ></Table.Row
            ></Table.Header
          >
          <Table.Body
            >{#each pricing.videos as row}<Table.Row
                ><Table.Cell>{row.resolution}</Table.Cell><Table.Cell
                  data-align="number">{money(row.price)}</Table.Cell
                ></Table.Row
              >{/each}</Table.Body
          >
        </Table.Root>
      </section>
    {/if}
    <p class="detail-note">
      以上为本地有效标准价，用于费用估算，不代表中转站实际扣款。来源未提供的价格保持未知，本地修改在同步时保留。
    </p>
  </section>
</div>

<style>
  .model-details {
    min-width: 0;
  }
  .detail-identity {
    display: flex;
    align-items: center;
    gap: 14px;
    margin-bottom: 22px;
  }
  .detail-identity > div {
    min-width: 0;
  }
  .detail-identity .model-brand {
    width: 44px;
    height: 44px;
  }
  .detail-eyebrow {
    color: var(--muted-foreground);
    font-size: 11px;
    letter-spacing: 0.08em;
    margin-bottom: 3px;
  }
  .detail-identity h2 {
    font-size: 22px;
    font-weight: 600;
    line-height: 1.4;
    overflow-wrap: anywhere;
  }
  .detail-identity code {
    display: block;
    color: var(--muted-foreground);
    font-size: 12px;
    margin-top: 3px;
    overflow-wrap: anywhere;
  }
  .detail-metadata {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 18px 24px;
    margin-bottom: 20px;
  }
  .detail-metadata dt,
  h3,
  .detail-rates dt {
    color: var(--muted-foreground);
    font-size: 11px;
    font-weight: 400;
  }
  .detail-metadata dd {
    margin: 6px 0 0;
    font-size: 13px;
    overflow-wrap: anywhere;
  }
  .detail-capabilities {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 16px 0;
  }
  .detail-capabilities :global(.model-capabilities) {
    max-width: none;
  }
  .detail-pricing {
    border: 1px solid var(--border);
    border-radius: 12px;
    overflow: hidden;
    background: var(--background);
  }
  .detail-price-heading {
    padding: 18px;
    background: linear-gradient(110deg, var(--accent), var(--background) 65%);
  }
  .detail-price-heading h3 {
    margin-bottom: 8px;
  }
  .detail-price-heading :global(.model-price) {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: end;
    column-gap: 16px;
  }
  .detail-price-heading :global(.price-amount) {
    grid-column: 1;
  }
  .detail-price-heading :global(.price-basis) {
    grid-column: 1;
  }
  .detail-price-heading :global(.price-amount strong) {
    font-size: 26px;
    letter-spacing: -0.5px;
  }
  .detail-price-heading :global(.price-traits) {
    grid-column: 2;
    grid-row: 1 / 3;
    justify-content: flex-end;
    max-width: 260px;
  }
  .detail-rates {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(130px, 1fr));
    border-top: 1px solid var(--border);
  }
  .detail-rates > div {
    padding: 13px 16px;
  }
  .detail-rates dd {
    font-size: 15px;
    font-weight: 600;
    margin: 5px 0 2px;
    font-variant-numeric: tabular-nums;
  }
  .detail-rates small {
    color: var(--muted-foreground);
    font-size: 10px;
  }
  .detail-billing {
    display: flex;
    flex-wrap: wrap;
    gap: 8px 20px;
    padding: 12px 16px;
    border-top: 1px solid var(--border);
    background: var(--muted);
    counter-reset: step;
    list-style: none;
  }
  .detail-billing li {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 11px;
    color: var(--muted-foreground);
  }
  .detail-billing li::before {
    counter-increment: step;
    content: counter(step);
    display: grid;
    place-items: center;
    width: 17px;
    height: 17px;
    border-radius: 50%;
    background: var(--accent);
    color: var(--primary);
    font-size: 10px;
  }
  .detail-rule-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(100%, 300px), 1fr));
    gap: 12px;
    padding: 12px;
  }
  .detail-rule {
    border: 1px solid var(--border);
    border-radius: 9px;
    overflow: hidden;
    min-width: 0;
  }
  .detail-rule > header {
    padding: 12px 14px;
    border-bottom: 1px solid var(--border);
  }
  .detail-rule h4 {
    font-size: 13px;
    font-weight: 600;
  }
  .detail-rule p {
    font-size: 11px;
    color: var(--muted-foreground);
    line-height: 1.6;
    overflow-wrap: anywhere;
  }
  .detail-rule header p {
    margin-top: 4px;
  }
  .detail-rule-inset {
    margin: 12px;
  }
  .detail-tier > p {
    padding: 10px 14px 0;
  }
  .detail-tier .detail-rates {
    border-top: 0;
  }
  .detail-mode + .detail-mode {
    border-top: 1px solid var(--border);
  }
  .detail-rule-title {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    font-size: 12px;
  }
  .detail-mode > .detail-rule-title {
    padding: 12px 14px;
  }
  .detail-periods {
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
    padding: 12px 14px;
  }
  .detail-periods > div {
    background: var(--muted);
    padding: 10px 12px;
    border-radius: 7px;
    flex: 1;
    min-width: min(100%, 230px);
  }
  .detail-periods p {
    margin-top: 5px;
  }
  .detail-note {
    padding: 14px 16px;
    color: var(--muted-foreground);
    font-size: 11px;
    line-height: 1.7;
  }
  @media (max-width: 600px) {
    .detail-metadata {
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 16px;
    }
    .detail-price-heading :global(.model-price) {
      grid-template-columns: 1fr;
    }
    .detail-price-heading :global(.price-traits) {
      grid-column: 1;
      grid-row: auto;
      justify-content: flex-start;
      margin-top: 10px;
    }
  }
</style>
