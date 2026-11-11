<script lang="ts">
  import { tick } from "svelte";
  import { ChevronDown, ChevronUp, SlidersHorizontal } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Badge } from "$lib/components/ui/badge";
  import * as Field from "$lib/components/ui/field";
  import * as Collapsible from "$lib/components/ui/collapsible";
  import * as Select from "$lib/components/ui/select";
  import type { Model } from "$lib/generated/mygo";
  import ModelCapabilities from "./ModelCapabilities.svelte";
  import PriceSummary from "./PriceSummary.svelte";
  import PriceEditor from "./PriceEditor.svelte";
  import { kinds } from "./display";

  /** 编辑仅修改父级独立草稿；保存与来源恢复仍由真实业务入口负责。 */
  let {
    model = $bindable(),
    busy,
    error,
    onsave,
    onrestore,
    onclose,
    mode = "edit",
    onback,
  }: {
    model: Model;
    busy: boolean;
    error: string;
    onsave: (event: SubmitEvent) => Promise<void>;
    onrestore: (group: string) => void;
    onclose: () => void;
    /** 创建时复用名称和价格表单，手动模式额外开放 ID 与类型。 */
    mode?: "edit" | "online" | "manual";
    /** 返回目录仅丢弃创建草稿，不写入数据库。 */
    onback?: () => void;
  } = $props();
  /** 价格规则按需展开，初始界面保留原型的紧凑资料布局。 */
  let pricesOpen = $state(false);
  /** 无效输入的提示和所属规则组，继续输入或重新提交时清除。 */
  let validationError = $state("");
  let openGroups = $state<string[]>(["base"]);
  const prefix = $props.id();

  /** 先展开无效字段再报告校验，避免折叠区必填失败时保存没有反馈。 */
  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    const invalid = form.querySelector<HTMLInputElement>("input:invalid");
    validationError = "";
    if (invalid) {
      const group = invalid.closest<HTMLElement>("[data-price-group]")?.dataset.priceGroup;
      validationError = invalid.type === "number" && invalid.validity.valueMissing
        ? "请输入价格或规则数值，填写完整后再保存。"
        : "请检查并填写标记的字段。";
      if (group) {
        pricesOpen = true;
        openGroups = [...new Set([...openGroups, group])];
      }
      await tick();
      invalid.scrollIntoView({ block: "center" });
      invalid.focus();
      invalid.reportValidity();
      return;
    }
    await onsave(event);
  }
</script>

<form
  onsubmit={submit}
  oninput={() => (validationError = "")}
  novalidate
  class="model-edit-form"
>
  <div class="model-edit-body scroll-quiet">
    <div class="model-origin">
      <span
        class="model-brand"
        data-brand={model.canonicalId.split("/")[0]}
        aria-hidden="true">{model.providerName.slice(0, 1).toUpperCase()}</span
      >
      <div><strong>{model.providerName}</strong><code>{model.modelId}</code></div>
    </div>
    <Field.FieldGroup class="grid gap-4 sm:grid-cols-2">
      <Field.Field
        ><Field.Label for={`${prefix}-name`}>显示名称</Field.Label><Input
          id={`${prefix}-name`}
          bind:value={model.name}
          maxlength={80}
          required
        /></Field.Field
      >
      <Field.Field
        ><Field.Label for={`${prefix}-id`}>模型 ID</Field.Label><Input
          id={`${prefix}-id`}
          bind:value={model.modelId}
          readonly={mode !== "manual"}
          required
          maxlength={256}
        /></Field.Field
      >
      {#if mode === "manual"}
        <Field.Field><Field.Label for={`${prefix}-kind`}>模型类型</Field.Label>
          <Select.Root type="single" bind:value={model.kind}><Select.Trigger id={`${prefix}-kind`} class="w-full">{kinds[model.kind]}</Select.Trigger><Select.Content><Select.Group>{#each Object.entries(kinds) as [value, label]}<Select.Item {value}>{label}</Select.Item>{/each}</Select.Group></Select.Content></Select.Root>
        </Field.Field>
      {:else}
      <Field.Field
        ><Field.Label for={`${prefix}-family`}>系列</Field.Label><Input
          id={`${prefix}-family`}
          value={model.family || "未知"}
          readonly
          class="bg-muted"
        /></Field.Field
      >
      <Field.Field
        ><Field.Label for={`${prefix}-context`}>上下文</Field.Label><Input
          id={`${prefix}-context`}
          value={model.context
            ? `${model.context.toLocaleString()} Token`
            : "未知"}
          readonly
          class="bg-muted"
        /></Field.Field
      >
      {/if}
    </Field.FieldGroup>
    <section class="model-edit-overview" aria-label="模型能力与价格概览">
      <div>
        <h3>模型能力</h3>
        <ModelCapabilities values={model.capabilities} />
      </div>
      <div>
        <h3>目录价格</h3>
        <PriceSummary
          pricing={model.pricing}
          kind={model.kind}
          modified={model.overrides.length > 0}
        />
      </div>
    </section>
    <div class="model-edit-type">
      <Badge variant="outline">{kinds[model.kind]}</Badge><span
        >{model.providerId ? `价格来源 models.dev · ${model.providerName}` : '手动创建'}</span
      >
    </div>
    <Collapsible.Root bind:open={pricesOpen}>
      <Collapsible.Trigger>
        {#snippet child({ props })}<Button
            {...props}
            variant="outline"
            class="w-full justify-between"
            ><span class="flex items-center gap-2"
              ><SlidersHorizontal data-icon="inline-start" />编辑价格规则</span
            >{#if pricesOpen}<ChevronUp />{:else}<ChevronDown />{/if}</Button
          >{/snippet}
      </Collapsible.Trigger>
      <Collapsible.Content class="pt-3"
        ><PriceEditor
          bind:pricing={model.pricing}
          overrides={model.overrides}
          onrestore={mode === "edit" ? onrestore : undefined}
          bind:openGroups
        /></Collapsible.Content
      >
    </Collapsible.Root>
  </div>
  {#if validationError || error}<p role="alert" class="error-banner model-edit-error">
      {validationError || error}
    </p>{/if}
  <footer class="model-edit-footer">
    {#if onback}<Button variant="ghost" disabled={busy} onclick={onback}>返回目录</Button>{/if}
    <Button variant="outline" disabled={busy} onclick={onclose}>取消</Button
    ><Button type="submit" disabled={busy}
      >{busy ? "正在保存…" : mode === "edit" ? "保存修改" : "创建模型"}</Button
    >
  </footer>
</form>

<style>
  /* 资料与价格规则在内容区滚动，错误提示和操作栏固定在底部保持可见。 */
  .model-edit-form {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-height: 0;
  }
  .model-edit-body {
    display: flex;
    flex-direction: column;
    gap: 16px;
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 24px;
  }
  .model-edit-error {
    flex-shrink: 0;
    margin: 0 24px 12px;
  }
  .model-origin {
    display: flex;
    align-items: center;
    gap: 11px;
    padding: 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--accent);
  }
  .model-origin .model-brand {
    width: 32px;
    height: 32px;
    font-size: 17px;
  }
  .model-origin > div {
    min-width: 0;
  }
  .model-origin strong {
    display: block;
    font-size: 12px;
    font-weight: 600;
  }
  .model-origin code {
    display: block;
    margin-top: 3px;
    color: var(--muted-foreground);
    font-size: 11px;
    overflow-wrap: anywhere;
  }
  .model-edit-overview {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 18px;
    padding: 14px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--muted);
  }
  .model-edit-overview h3 {
    color: var(--muted-foreground);
    font-size: 11px;
    font-weight: 500;
    margin-bottom: 9px;
  }
  .model-edit-overview :global(.model-capabilities) {
    max-width: none;
  }
  .model-edit-overview :global(.price-amount strong) {
    font-size: 16px;
  }
  .model-edit-type {
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
    align-items: center;
    font-size: 11px;
    color: var(--muted-foreground);
  }
  .model-edit-footer {
    display: flex;
    justify-content: flex-end;
    flex-shrink: 0;
    gap: 8px;
    border-top: 1px solid var(--border);
    padding: 14px 24px;
  }
  @media (max-width: 600px) {
    .model-edit-overview {
      grid-template-columns: 1fr;
    }
  }
</style>
