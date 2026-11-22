<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { tick } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Badge } from "$lib/components/ui/badge";
  import * as Field from "$lib/components/ui/field";
  import * as Select from "$lib/components/ui/select";
  import type { Model } from "$lib/generated/mygo";
  import ModelCapabilities from "./ModelCapabilities.svelte";
  import PriceEditor from "./PriceEditor.svelte";
  import { kinds, modelInitial } from "./display";

  /** 编辑仅修改父级独立草稿；模型 ID 仅创建时可改，保存与来源恢复由真实业务入口负责。 */
  let {
    model = $bindable(),
    busy,
    error,
    onsave,
    onrestore,
    onclose,
    mode = "edit",
    sourceUpdated = false,
  }: {
    model: Model;
    /** 来源更新时说明保存合并规则，不替换当前草稿。 */
    sourceUpdated?: boolean;
    busy: boolean;
    error: string;
    onsave: (event: SubmitEvent) => Promise<void>;
    onrestore: (group: string) => void;
    onclose: () => void;
    /** 创建时复用名称、模型 ID 和价格表单；手动模式额外开放类型，编辑模式模型 ID 只读。 */
    mode?: "edit" | "online" | "manual";
  } = $props();
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
        aria-hidden="true">{modelInitial(
          model.name,
          model.providerName,
          !model.providerId,
        )}</span
      >
      <div>
        <strong>{model.providerName}</strong>
        {#if model.providerId}<code>{model.sourceModelId}</code>{/if}
      </div>
    </div>
    <Field.FieldGroup class="grid gap-3 sm:grid-cols-2">
      <Field.Field class="gap-1.5"
        ><Field.Label for={`${prefix}-name`}>显示名称</Field.Label><Input
          id={`${prefix}-name`}
          bind:value={model.name}
          maxlength={80}
          required
        /></Field.Field
      >
      <Field.Field class="gap-1.5">
        <Field.Label for={`${prefix}-id`}>模型 ID</Field.Label>
        <Input
          id={`${prefix}-id`}
          bind:value={model.modelId}
          readonly={mode === "edit"}
          aria-describedby={mode === "edit" ? `${prefix}-id-note` : undefined}
          required
          maxlength={256}
        />
        {#if mode === "edit"}<Field.Description id={`${prefix}-id-note`}>创建后不可修改。</Field.Description>{/if}
      </Field.Field>
      {#if mode === "manual"}
        <Field.Field class="gap-1.5"><Field.Label for={`${prefix}-kind`}>模型类型</Field.Label>
          <Select.Root type="single" bind:value={model.kind}><Select.Trigger id={`${prefix}-kind`} class="w-full">{kinds[model.kind]}</Select.Trigger><Select.Content><Select.Group>{#each Object.entries(kinds) as [value, label]}<Select.Item {value}>{label}</Select.Item>{/each}</Select.Group></Select.Content></Select.Root>
        </Field.Field>
      {/if}
    </Field.FieldGroup>
    <div class="model-edit-type">
      <Badge variant="outline">{kinds[model.kind]}</Badge>
      {#if model.context}<span>上下文 {model.context.toLocaleString()} Token</span>{/if}
      {#if model.family}<span>{model.family}</span>{/if}
      <ModelCapabilities values={model.capabilities} />
    </div>
    {#if sourceUpdated}<Alert.Root variant="info"><Alert.Description>来源价格已更新，保存时未修改部分采用最新来源值，本地修改保留</Alert.Description></Alert.Root>{/if}
    {#each model.warnings ?? [] as warning}<Alert.Root variant="info"><Alert.Description>{warning}</Alert.Description></Alert.Root>{/each}
    <PriceEditor
      bind:pricing={model.pricing}
      source={model.sourcePricing}
      kind={model.kind}
      manual={!model.providerId}
      baseAlwaysVisible={mode !== "online"}
      overrides={model.overrides}
      onrestore={mode === "edit" && model.providerId ? onrestore : undefined}
      bind:openGroups
    />
    <p class="text-xs text-muted-foreground">{model.providerId ? `价格来源 ${model.source || "历史目录"} · ${model.providerName}` : "手动创建 · 未填写价格保持未知"}</p>
  </div>
  {#if validationError || error}<Alert.Root variant="destructive" class="mx-6 mb-3 shrink-0"><Alert.Description>
      {validationError || error}
    </Alert.Description></Alert.Root>{/if}
  <footer class="model-edit-footer">
    <Button variant="outline" disabled={busy} onclick={onclose}>取消</Button
    ><Button type="submit" disabled={busy}
      >{busy ? "正在保存…" : mode === "edit" ? "保存修改" : "创建模型"}</Button
    >
  </footer>
</form>

<style>
  /* 新建与编辑模型隐藏浏览器数字调节箭头，仍保留 number 类型及其范围、精度校验。 */

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
    gap: 10px;
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 16px 20px;
  }

  .model-origin {
    display: flex;
    align-items: center;
    gap: 11px;
    padding: 0;
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
  .model-edit-type {
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
    align-items: center;
    font-size: 11px;
    color: var(--muted-foreground);
  }
  .model-edit-type :global(.model-capabilities) {
    max-width: none;
  }
  .model-edit-footer {
    display: flex;
    justify-content: flex-end;
    flex-shrink: 0;
    gap: 8px;
    border-top: 1px solid var(--border);
    padding: 14px 24px;
  }
</style>
