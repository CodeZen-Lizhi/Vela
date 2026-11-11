<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Textarea } from "$lib/components/ui/textarea";
  import * as Field from "$lib/components/ui/field";
  import * as Collapsible from "$lib/components/ui/collapsible";
  import type { ProviderKey, ProviderKeyDraft, ProviderDefaults } from "$lib/generated/mygo";
  import ProxyField from "./ProxyField.svelte";
  import KeyQueryFields from "./KeyQueryFields.svelte";
  import {defaultKeyQuery, queryConfigDraft} from "./query-defaults";
  import {VelaService, type ProviderKeyQuery, type ProviderKeyQueryDraft} from "$lib/generated/mygo";
  /** Key 基本配置与查询草稿共同编辑，保存由父级协调；启停与模型权限保持独立。 */
  let { item, defaults, nextPriority, busy, error, onsave, onclose }: {
    item: ProviderKey | null; defaults: ProviderDefaults; nextPriority: number; busy: boolean; error: string;
    onsave: (draft: ProviderKeyDraft, query: ProviderKeyQueryDraft) => Promise<void>; onclose: () => void;
  } = $props();
  /** 空密钥在编辑时不替换旧值；新增时允许暂不设置。 */
  let draft = $state<ProviderKeyDraft>(untrack(() => ({
    name: item?.name ?? "", secret: "", note: item?.note ?? "", multiplier: item ? item.multiplier : defaults.keyMultiplier,
    priority: item?.priority ?? nextPriority, rpmLimit: item?.rpmLimit ?? 0, concurrencyLimit: item?.concurrencyLimit ?? 0,
    cacheMinutes: item?.cacheMinutes ?? defaults.keyCacheMinutes, probeMinutes: item?.probeMinutes ?? defaults.keyProbeMinutes,
    proxy: {...(item?.proxy ?? {mode: "provider"})},
  })));
  /** 名称与倍率来源、查询连接也属于当前新增或编辑草稿。 */
  let queryDraft = $state<ProviderKeyQueryDraft>(defaultKeyQuery());
  /** 最近已保存查询的只读状态，与未保存来源草稿分开保留。 */
  let queryState = $state<ProviderKeyQuery | null>(null);
  /** 查询配置读取完成前禁止提交默认草稿覆盖已有配置。 */
  let queryLoading = $state(!!untrack(() => item));
  /** 读取失败时保留基本草稿并明确阻止保存。 */
  let queryError = $state("");
  onMount(async () => {
    if (!item) return;
    try {
      const stored = await VelaService.providerKeyQuery(item.id);
      queryState = stored;
      queryDraft = {reuseProvider: stored.reuseProvider, config: {...stored.config}, nameSource: stored.nameSource, multiplierSource: stored.multiplierSource, namePath: stored.namePath, groupPath: stored.groupPath, multiplierPath: stored.multiplierPath, keyPath: stored.keyPath};
    } catch (cause) { queryError = String(cause); }
    finally { queryLoading = false; }
  });
  /** 高级项有非默认值时自动展开，避免隐藏已生效的配置。 */
  let advanced = $state(untrack(() => !!item && (item.rpmLimit !== 0 || item.concurrencyLimit !== 0 || item.cacheMinutes !== defaults.keyCacheMinutes || item.probeMinutes !== defaults.keyProbeMinutes || item.proxy.mode !== "provider")));
  /** RPM 与并发输入留空分别代表不限制，TTL/探测清空恢复默认。 */
  let rpm = $state<number | undefined>(untrack(() => draft.rpmLimit || undefined));
  let concurrency = $state<number | undefined>(untrack(() => draft.concurrencyLimit || undefined));
  let cache = $state<number | undefined>(untrack(() => draft.cacheMinutes));
  let probe = $state<number | undefined>(untrack(() => draft.probeMinutes));
  /** 分别提交可写基本配置与查询草稿，不提交只读查询状态；父级以同一事务保存配置。 */
  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    await onsave({...draft, rpmLimit: rpm ?? 0, concurrencyLimit: concurrency ?? 0, cacheMinutes: cache ?? defaults.keyCacheMinutes, probeMinutes: probe ?? defaults.keyProbeMinutes}, {...queryDraft, config: queryConfigDraft(queryDraft.config)});
  }
</script>
<form class="provider-form" onsubmit={submit}>
  <fieldset disabled={busy} class="contents"><Field.Group>
    <Field.Field><Field.Label for="key-name">Key 名称 *</Field.Label><Input id="key-name" bind:value={draft.name} required maxlength={100} /></Field.Field>
    <Field.Field><Field.Label for="key-secret">密钥</Field.Label><Input id="key-secret" type="password" autocomplete="new-password" bind:value={draft.secret} placeholder={item?.secretMask || "可暂不设置"} /><Field.Description>{item ? "留空表示不修改当前密钥。" : "密钥加密保存，列表只展示掩码。"}</Field.Description></Field.Field>
    <Field.Field><Field.Label for="key-note">备注</Field.Label><Textarea id="key-note" bind:value={draft.note} rows={2} /></Field.Field>
    <Field.Group class="provider-fields-grid">
      <Field.Field><Field.Label for="key-multiplier">手动倍率</Field.Label><Input id="key-multiplier" type="number" min={0} max={100} step="any" bind:value={draft.multiplier} disabled={queryDraft.multiplierSource === "upstream"} required={queryDraft.multiplierSource === "manual"} /></Field.Field>
      <Field.Field><Field.Label for="key-priority">优先级</Field.Label><Input id="key-priority" type="number" min={0} step={1} bind:value={draft.priority} required /><Field.Description>数字越小越优先。</Field.Description></Field.Field>
    </Field.Group>
    <Collapsible.Root bind:open={advanced}>
      <Collapsible.Trigger class="provider-advanced">高级设置 <span>RPM {rpm ?? "不限"} · 并发 {concurrency ?? "不限"}</span></Collapsible.Trigger>
      <Collapsible.Content><Field.Group class="pt-4"><Field.Group class="provider-fields-grid">
        <Field.Field><Field.Label for="key-rpm">RPM 限制</Field.Label><Input id="key-rpm" type="number" min={1} max={10000} step={1} bind:value={rpm} placeholder="不限制" /></Field.Field>
        <Field.Field><Field.Label for="key-concurrency">并发上限</Field.Label><Input id="key-concurrency" type="number" min={0} step={1} bind:value={concurrency} placeholder="不限制" /></Field.Field>
        <Field.Field><Field.Label for="key-cache">缓存 TTL（分钟）</Field.Label><Input id="key-cache" type="number" min={0} max={60} step={1} bind:value={cache} /></Field.Field>
        <Field.Field><Field.Label for="key-probe">熔断探测间隔上限（分钟）</Field.Label><Input id="key-probe" type="number" min={1} max={32} step={1} bind:value={probe} /></Field.Field>
      </Field.Group><ProxyField bind:value={draft.proxy} forKey /></Field.Group></Collapsible.Content>
    </Collapsible.Root>
    {#if queryLoading}<p class="text-xs text-muted-foreground">正在读取查询配置…</p>{:else if queryError}<p class="provider-error" role="alert">{queryError}</p>{:else}<KeyQueryFields bind:draft={queryDraft} {queryState} disabled={busy}/>{/if}
  </Field.Group></fieldset>
  {#if error}<p class="provider-error" role="alert">{error}</p>{/if}
  <footer class="provider-form-footer"><Button variant="outline" disabled={busy} onclick={onclose}>取消</Button><Button type="submit" disabled={busy || queryLoading || !!queryError}>{busy ? "保存中…" : item ? "保存更改" : "添加 Key"}</Button></footer>
</form>
