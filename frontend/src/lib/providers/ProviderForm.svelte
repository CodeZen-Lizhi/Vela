<script lang="ts">
  import { untrack } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Textarea } from "$lib/components/ui/textarea";
  import { Switch } from "$lib/components/ui/switch";
  import * as Field from "$lib/components/ui/field";
  import type { Provider, ProviderDefaults, ProviderDraft } from "$lib/generated/mygo";
  import ProxyField from "./ProxyField.svelte";
  /** 新增与编辑使用独立草稿，取消不会把字段写回列表。 */
  let { provider, defaults, busy, error, onsave, onclose }: {
    provider: Provider | null; defaults: ProviderDefaults; busy: boolean; error: string;
    onsave: (draft: ProviderDraft) => Promise<void>; onclose: () => void;
  } = $props();
  /** 身份、地址、备注与请求配置构成保存边界，不包含账户或启停。 */
  let draft = $state<ProviderDraft>(untrack(() => provider ? {name: provider.name, website: provider.website, url: provider.url, note: provider.note, request: {...provider.request, proxy:{...provider.request.proxy}}} : {
    name: "", website: "", url: "", note: "", request: { attempts: 0, streamTotalMillis: 0, firstByteSeconds: 0, requestSeconds: 0, maxTransfers: 0, maxTransferSeconds: 0, proxy: {mode: "global", url: ""}, responsesWebSocket: false, transportFailover: true },
  }));
  /** 可空数值对应后端的“使用默认值”，秒输入只在提交时转换为毫秒。 */
  let attempts = $state<number | undefined>(untrack(() => draft.request.attempts || undefined));
  let totalSeconds = $state<number | undefined>(untrack(() => draft.request.streamTotalMillis ? draft.request.streamTotalMillis / 1000 : undefined));
  let firstSeconds = $state<number | undefined>(untrack(() => draft.request.firstByteSeconds || undefined));
  let requestSeconds = $state<number | undefined>(untrack(() => draft.request.requestSeconds || undefined));
  let transfers = $state<number | undefined>(untrack(() => draft.request.maxTransfers || undefined));
  let transferSeconds = $state<number | undefined>(untrack(() => draft.request.maxTransferSeconds || undefined));
  /** 清空覆盖以 0 提交，默认值只作占位和生效说明，不伪装为显式配置。 */
  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    await onsave({...draft, request: {...draft.request, attempts: attempts ?? 0, streamTotalMillis: Math.round((totalSeconds ?? 0) * 1000), firstByteSeconds: firstSeconds ?? 0, requestSeconds: requestSeconds ?? 0, maxTransfers: transfers ?? 0, maxTransferSeconds: transferSeconds ?? 0}});
  }
</script>
<form class="provider-form" onsubmit={submit}>
  <fieldset disabled={busy} class="contents">
    <Field.Group>
      <Field.Set><Field.Legend>基本信息</Field.Legend><Field.Group>
        <Field.Field><Field.Label for="provider-name">名称 *</Field.Label><Input id="provider-name" bind:value={draft.name} maxlength={80} required placeholder="例如：主力中转站" /></Field.Field>
        <Field.Field><Field.Label for="provider-website">主站链接</Field.Label><Input id="provider-website" bind:value={draft.website} placeholder="https://example.com（可选）" /></Field.Field>
        <Field.Field><Field.Label for="provider-url">服务地址 *</Field.Label><Input id="provider-url" bind:value={draft.url} required placeholder="https://api.example.com" /><Field.Description>填写站点根地址，常见的 /v1、/v1beta 后缀会自动去除。</Field.Description></Field.Field>
        <Field.Field><Field.Label for="provider-note">备注</Field.Label><Textarea id="provider-note" bind:value={draft.note} rows={2} /></Field.Field>
      </Field.Group></Field.Set>
      <Field.Set><Field.Legend>请求配置</Field.Legend><Field.Group class="provider-fields-grid">
        <Field.Field><Field.Label for="provider-attempts">总尝试次数（含首次）</Field.Label><Input id="provider-attempts" type="number" min={1} max={99} step={1} bind:value={attempts} placeholder={String(defaults.attempts)} /><Field.Description>当前生效：{attempts ?? defaults.attempts} 次 · {attempts === undefined ? "默认" : "本供应商配置"}</Field.Description></Field.Field>
        <Field.Field><Field.Label for="provider-total">流式请求总超时（秒）</Field.Label><Input id="provider-total" type="number" min={1} max={1200} step={0.001} bind:value={totalSeconds} placeholder={String(defaults.streamTotalMillis / 1000)} /><Field.Description>当前生效：{totalSeconds ?? defaults.streamTotalMillis / 1000} 秒 · {totalSeconds === undefined ? "默认" : "本供应商配置"}</Field.Description></Field.Field>
        <Field.Field><Field.Label for="provider-first">首次响应超时（秒）</Field.Label><Input id="provider-first" type="number" min={1} max={300} step={1} bind:value={firstSeconds} placeholder={String(defaults.firstByteSeconds)} /><Field.Description>当前生效：{firstSeconds ?? defaults.firstByteSeconds} 秒 · {firstSeconds === undefined ? "默认" : "本供应商配置"}</Field.Description></Field.Field>
        <Field.Field><Field.Label for="provider-request">非流式请求超时（秒）</Field.Label><Input id="provider-request" type="number" min={1} max={1200} step={1} bind:value={requestSeconds} placeholder={String(defaults.requestSeconds)} /><Field.Description>当前生效：{requestSeconds ?? defaults.requestSeconds} 秒 · {requestSeconds === undefined ? "默认" : "本供应商配置"}</Field.Description></Field.Field>
        <Field.Field><Field.Label for="provider-transfers">最大转移次数</Field.Label><Input id="provider-transfers" type="number" min={0} step={1} bind:value={transfers} placeholder="0（不限制）" /></Field.Field>
        <Field.Field><Field.Label for="provider-transfer-seconds">最大转移超时（秒）</Field.Label><Input id="provider-transfer-seconds" type="number" min={0} step={1} bind:value={transferSeconds} placeholder="0（不限制）" /></Field.Field>
      </Field.Group><ProxyField bind:value={draft.request.proxy} /></Field.Set>
      <Field.Set><Field.Legend>功能开关</Field.Legend><Field.Group>
        <Field.Field orientation="horizontal"><Field.Label for="provider-ws">Responses WebSocket 模式</Field.Label><Switch id="provider-ws" bind:checked={draft.request.responsesWebSocket} /></Field.Field>
        <Field.Field orientation="horizontal"><Field.Label for="provider-failover">传输失败后继续转移</Field.Label><Switch id="provider-failover" bind:checked={draft.request.transportFailover} /></Field.Field>
      </Field.Group></Field.Set>
    </Field.Group>
  </fieldset>
  {#if error}<p class="provider-error" role="alert">{error}</p>{/if}
  <footer class="provider-form-footer"><Button variant="outline" disabled={busy} onclick={onclose}>取消</Button><Button type="submit" disabled={busy}>{busy ? "保存中…" : provider ? "保存更改" : "创建供应商"}</Button></footer>
</form>
