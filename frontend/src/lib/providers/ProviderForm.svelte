<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { tick, untrack } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { ChevronDown } from "@lucide/svelte";
  import * as Collapsible from "$lib/components/ui/collapsible";
  import { Switch } from "$lib/components/ui/switch";
  import * as Field from "$lib/components/ui/field";
  import type { Provider, ProviderDefaults, ProviderDraft } from "$lib/generated/mygo";
  import ProxyField from "./ProxyField.svelte";
  /** 新增与编辑使用独立草稿，取消不会把字段写回列表。 */
  let { provider, defaults, busy, error, onsave, onclose }: {
    provider: Provider | null; defaults: ProviderDefaults; busy: boolean; error: string;
    onsave: (draft: ProviderDraft) => Promise<void>; onclose: () => void;
  } = $props();
  /** 供应商草稿仅包含名称、地址与请求配置，不包含备注。 */
  let draft = $state<ProviderDraft>(untrack(() => provider ? {name: provider.name, website: provider.website, url: provider.url, request: {...provider.request, proxy:{...provider.request.proxy}}} : {
    name: "", website: "", url: "", request: { attempts: 0, streamTotalMillis: 0, firstByteSeconds: 0, requestSeconds: 0, proxy: {mode: "global"}, responsesWebSocket: false },
  }));
  /** 高级设置默认折叠，收起只影响显示，草稿和约束始终保留。 */
  let advanced = $state(false);
  /** 可空数值对应后端的“使用默认值”，秒输入只在提交时转换为毫秒。 */
  let attempts = $state<number | undefined>(untrack(() => draft.request.attempts || undefined));
  let totalSeconds = $state<number | undefined>(untrack(() => draft.request.streamTotalMillis ? draft.request.streamTotalMillis / 1000 : undefined));
  let firstSeconds = $state<number | undefined>(untrack(() => draft.request.firstByteSeconds || undefined));
  let requestSeconds = $state<number | undefined>(untrack(() => draft.request.requestSeconds || undefined));
  /** 清空覆盖以 0 提交，默认值只作占位和生效说明，不伪装为显式配置。 */
  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    const invalid = form.querySelector<HTMLInputElement>("input:invalid");
    if (invalid) {
      if (invalid.closest("[data-slot=collapsible-content]")) advanced = true;
      await tick();
      invalid.focus();
      invalid.reportValidity();
      return;
    }
    await onsave({...draft, request: {...draft.request, attempts: attempts ?? 0, streamTotalMillis: Math.round((totalSeconds ?? 0) * 1000), firstByteSeconds: firstSeconds ?? 0, requestSeconds: requestSeconds ?? 0}});
  }
</script>
<form class="provider-editor" novalidate onsubmit={submit}>
  <div class="provider-editor-body">
    <Field.Set disabled={busy} class="contents">
      <Field.Group class="provider-editor-fields">
        <Field.Field orientation="horizontal" class="provider-setting-row"><Field.Label for="provider-name">供应商名称 <span class="text-destructive">*</span></Field.Label><Field.Content><Input id="provider-name" bind:value={draft.name} maxlength={80} required placeholder="例如：主力中转站" /></Field.Content></Field.Field>
        <Field.Field orientation="horizontal" class="provider-setting-row"><Field.Label for="provider-url">服务地址 <span class="text-destructive">*</span></Field.Label><Field.Content><Input id="provider-url" bind:value={draft.url} required placeholder="https://api.example.com" /><Field.Description>填写站点根地址，自动去除 /v1、/v1beta 后缀。</Field.Description></Field.Content></Field.Field>
        <Field.Field orientation="horizontal" class="provider-setting-row"><Field.Label for="provider-website">主站链接</Field.Label><Field.Content><Input id="provider-website" bind:value={draft.website} placeholder="https://example.com（可选）" /></Field.Content></Field.Field>
        <ProxyField bind:value={draft.request.proxy} horizontal />
      </Field.Group>
      <Collapsible.Root bind:open={advanced} class="provider-editor-advanced">
        <Collapsible.Trigger disabled={busy}>{#snippet child({ props })}<Button {...props} variant="ghost" size="disclosure"><span>高级设置</span><span class="provider-editor-expand-hint">超时、尝试与连接</span><ChevronDown class={advanced ? "size-4 rotate-180" : "size-4"}/></Button>{/snippet}</Collapsible.Trigger>
        <Collapsible.Content forceMount class="provider-editor-advanced-content">
          <p class="provider-editor-hint">留空使用默认值；总尝试次数包含首次请求。</p>
          <Field.Group class="provider-editor-fields">
            <Field.Field orientation="horizontal" class="provider-setting-row"><Field.Label for="provider-first">首次响应超时</Field.Label><Field.Content><div class="provider-number-control"><Input id="provider-first" type="number" min={1} max={300} step={1} bind:value={firstSeconds} placeholder={String(defaults.firstByteSeconds)}/><span>秒</span></div></Field.Content></Field.Field>
            <Field.Field orientation="horizontal" class="provider-setting-row"><Field.Label for="provider-total">流式总超时</Field.Label><Field.Content><div class="provider-number-control"><Input id="provider-total" type="number" min={1} max={1200} step={0.001} bind:value={totalSeconds} placeholder={String(defaults.streamTotalMillis / 1000)}/><span>秒</span></div></Field.Content></Field.Field>
            <Field.Field orientation="horizontal" class="provider-setting-row"><Field.Label for="provider-request">非流式超时</Field.Label><Field.Content><div class="provider-number-control"><Input id="provider-request" type="number" min={1} max={1200} step={1} bind:value={requestSeconds} placeholder={String(defaults.requestSeconds)}/><span>秒</span></div></Field.Content></Field.Field>
            <Field.Field orientation="horizontal" class="provider-setting-row"><Field.Label for="provider-attempts">总尝试次数</Field.Label><Field.Content><div class="provider-number-control"><Input id="provider-attempts" type="number" min={1} max={99} step={1} bind:value={attempts} placeholder={String(defaults.attempts)}/><span>次</span></div><Field.Description>包含首次请求。</Field.Description></Field.Content></Field.Field>
            <Field.Field orientation="horizontal" class="provider-connection-setting">
              <Field.Content>
                <div class="provider-connection-toggle">
                  <Field.Label for="provider-ws" variant="content">Responses WebSocket</Field.Label>
                  <Switch id="provider-ws" aria-describedby="provider-ws-description" bind:checked={draft.request.responsesWebSocket} disabled={busy}/>
                </div>
                <Field.Description id="provider-ws-description">仅在上游支持时开启，不影响 HTTP / SSE。</Field.Description>
              </Field.Content>
            </Field.Field>
          </Field.Group>
        </Collapsible.Content>
      </Collapsible.Root>
    </Field.Set>
  </div>
  {#if error}<Alert.Root variant="inline-error"><Alert.Description>{error}</Alert.Description></Alert.Root>{/if}
  <footer class="provider-editor-footer"><Button type="button" variant="outline" disabled={busy} onclick={onclose}>取消</Button><Button type="submit" disabled={busy}>{busy ? "保存中…" : provider ? "保存更改" : "创建供应商"}</Button></footer>
</form>
