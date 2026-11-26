<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { onMount, tick, untrack } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import * as Field from "$lib/components/ui/field";
  import * as Collapsible from "$lib/components/ui/collapsible";
  import * as InputGroup from "$lib/components/ui/input-group";
  import { Badge } from "$lib/components/ui/badge";
  import { ChevronDown, Eye, EyeOff } from "@lucide/svelte";
  import type { ProviderKey, ProviderKeyDraft, ProviderDefaults } from "$lib/generated/mygo";
  import ProxyField from "./ProxyField.svelte";
  import KeyQueryFields from "./KeyQueryFields.svelte";
  import MultiplierSource from "./MultiplierSource.svelte";
  import { defaultKeyQuery, queryConfigDraft } from "./query-defaults";
  import { VelaService, type ProviderKeyQuery, type ProviderKeyQueryDraft } from "$lib/generated/mygo";

  /** Key 编辑器按凭证、调用规则和可选上游同步分层；保存仍由父级协调。 */
  let { item, defaults, nextPriority, providerQueryMode, busy, error, onsave, onclose }: {
    item: ProviderKey | null;
    defaults: ProviderDefaults;
    nextPriority: number;
    /** 供应商余额查询方式；Key 复用连接时据此决定是否显示响应字段路径。 */
    providerQueryMode: string;
    busy: boolean;
    error: string;
    onsave: (draft: ProviderKeyDraft, query: ProviderKeyQueryDraft) => Promise<void>;
    onclose: () => void;
  } = $props();

  /** 空密钥在编辑时不替换旧值；新增时允许暂不设置。 */
  let draft = $state<ProviderKeyDraft>(untrack(() => ({
    name: item?.name ?? "",
    secret: "",
    // 旧备注与无执行效果的 TTL 仅兼容保留，不再提供编辑入口。
    note: item?.note ?? "",
    multiplier: item ? item.multiplier : defaults.keyMultiplier,
    priority: item?.priority ?? nextPriority,
    rpmLimit: item?.rpmLimit ?? 0,
    concurrencyLimit: item?.concurrencyLimit ?? 0,
    cacheMinutes: item?.cacheMinutes ?? defaults.keyCacheMinutes,
    probeMinutes: item?.probeMinutes ?? defaults.keyProbeMinutes,
    proxy: { mode: item?.proxy.mode === "direct" ? "direct" : "global" },
  })));
  /** 名称来源、倍率来源和查询连接与基本配置一次保存。 */
  let queryDraft = $state<ProviderKeyQueryDraft>(defaultKeyQuery());
  /** 最近已保存查询状态只读展示，不与未保存来源草稿混用。 */
  let queryState = $state<ProviderKeyQuery | null>(null);
  /** 编辑时先读取已保存连接，避免默认草稿覆盖既有查询配置。 */
  let queryLoading = $state(!!untrack(() => item));
  let queryError = $state("");
  let showSecret = $state(false);

  onMount(async () => {
    if (!item) return;
    try {
      const stored = await VelaService.providerKeyQuery(item.id);
      queryState = stored;
      queryDraft = {
        reuseProvider: stored.reuseProvider,
        config: { ...stored.config },
        nameSource: stored.nameSource,
        multiplierSource: stored.multiplierSource,
        namePath: stored.namePath,
        groupPath: stored.groupPath,
        multiplierPath: stored.multiplierPath,
        keyPath: stored.keyPath,
      };
      syncOpen = stored.nameSource !== "manual" || stored.multiplierSource !== "manual" || !stored.reuseProvider;
    } catch (cause) {
      queryError = String(cause);
      syncOpen = true;
    } finally {
      queryLoading = false;
    }
  });

  /** 高级项有非默认值时自动展开，避免隐藏已生效的调度或代理配置。 */
  let advanced = $state(untrack(() => !!item && (
    item.rpmLimit !== 0
    || item.concurrencyLimit !== 0
    || item.probeMinutes !== defaults.keyProbeMinutes
    || item.proxy.mode === "direct"
  )));
  /** 自动同步已有配置时直接展开，新增时保持收起并走手动维护的快速路径。 */
  let syncOpen = $state(false);
  /** RPM 与并发留空分别表示不限制；探测留空恢复默认值。 */
  let rpm = $state<number | undefined>(untrack(() => draft.rpmLimit || undefined));
  let concurrency = $state<number | undefined>(untrack(() => draft.concurrencyLimit || undefined));
  let probe = $state<number | undefined>(untrack(() => draft.probeMinutes));
  /** 高级摘要只陈述当前限制，不重复展示全部低频字段。 */
  const limitSummary = $derived(`RPM ${rpm ?? "不限"} · 并发 ${concurrency ?? "不限"}`);
  /** 同步摘要留在折叠标题中，收起后仍能判断名称和倍率的数据来源。 */
  const syncSummary = $derived(queryLoading
    ? "正在读取查询配置…"
    : queryError
      ? "查询配置读取失败"
      : `${queryDraft.nameSource === "upstream_key"
        ? "名称取上游 Key"
        : queryDraft.nameSource === "upstream_group"
          ? "名称取上游分组"
          : "名称手动"} · ${queryDraft.multiplierSource === "upstream" ? "倍率自动" : "倍率手动"}`);

  /** 倍率切为自动时展开同步区，确保用户能看到连接和字段来源。 */
  function setMultiplierSource(source: "manual" | "upstream"): void {
    queryDraft.multiplierSource = source;
    if (source === "upstream") syncOpen = true;
  }

  /** 校验可见及折叠区字段，必要时展开对应区并聚焦首个无效项。 */
  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    const invalid = form.querySelector<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>("input:invalid, textarea:invalid, select:invalid");
    if (invalid) {
      if (invalid.closest('[data-key-section="advanced"]')) advanced = true;
      if (invalid.closest('[data-key-section="sync"]')) syncOpen = true;
      await tick();
      invalid.focus();
      invalid.reportValidity();
      return;
    }
    await onsave(
      {
        ...draft,
        rpmLimit: rpm ?? 0,
        concurrencyLimit: concurrency ?? 0,
        probeMinutes: probe ?? defaults.keyProbeMinutes,
      },
      { ...queryDraft, config: queryConfigDraft(queryDraft.config) },
    );
  }
</script>

<form class="provider-editor key-editor-form" novalidate onsubmit={submit}>
  <div class="provider-editor-body key-editor-body">
    <Field.Set disabled={busy} class="key-editor-fieldset">
      <section class="key-form-section" aria-labelledby="key-credentials-title">
        <header class="key-form-section-heading">
          <h3 id="key-credentials-title">调用凭证</h3>
        </header>
        <Field.Group class="provider-editor-fields">
          <Field.Field orientation="horizontal" class="provider-setting-row">
            <Field.Label for="key-name">Key 名称 <span class="text-destructive">*</span></Field.Label>
            <Field.Content><Input id="key-name" bind:value={draft.name} required maxlength={100} placeholder="例如：日常主力" /></Field.Content>
          </Field.Field>
          <Field.Field orientation="horizontal" class="provider-setting-row">
            <Field.Label for="key-secret">密钥</Field.Label>
            <Field.Content>
              <InputGroup.Root>
                <InputGroup.Input id="key-secret" type={showSecret ? "text" : "password"} autocomplete="new-password" bind:value={draft.secret} placeholder={item?.secretMask || "可暂不设置"} />
                <InputGroup.Addon align="inline-end">
                  <InputGroup.Button size="icon-xs" aria-label={showSecret ? "隐藏密钥" : "显示密钥"} onclick={() => showSecret = !showSecret}>
                    {#if showSecret}<EyeOff />{:else}<Eye />{/if}
                  </InputGroup.Button>
                </InputGroup.Addon>
              </InputGroup.Root>
              <Field.Description>{item ? "留空表示不修改当前密钥。" : "可稍后补充；密钥加密保存，列表只显示掩码。"}</Field.Description>
            </Field.Content>
          </Field.Field>
        </Field.Group>
      </section>

      <section class="key-form-section" aria-labelledby="key-routing-title">
        <header class="key-form-section-heading">
          <h3 id="key-routing-title">调用规则</h3>
        </header>
        <Field.Group class="provider-fields-grid key-routing-grid">
          <Field.Field>
            <Field.Label for="key-multiplier">倍率</Field.Label>
            {#if queryDraft.multiplierSource === "manual"}
              <div class="provider-number-control key-number-control"><Input id="key-multiplier" type="number" min={0} max={100} step="any" bind:value={draft.multiplier} required/><span>×</span></div>
            {:else}
              <Input id="key-multiplier" class="max-w-28" value={item?.multiplier === null || item?.multiplier === undefined ? "倍率未知" : `${item.multiplier}×`} readonly />
            {/if}
          </Field.Field>
          <Field.Field>
            <Field.Label>倍率来源</Field.Label>
            <div><MultiplierSource value={queryDraft.multiplierSource} label="倍率来源" disabled={busy} onchange={setMultiplierSource}/></div>
          </Field.Field>
          <Field.Field>
            <Field.Label for="key-priority">优先级</Field.Label>
            <Input id="key-priority" type="number" min={0} step={1} bind:value={draft.priority} required class="max-w-28" />
            <Field.Description>数字越小越优先。</Field.Description>
          </Field.Field>
        </Field.Group>

        <Collapsible.Root bind:open={advanced} class="provider-editor-advanced" data-key-section="advanced">
          <Collapsible.Trigger disabled={busy}>{#snippet child({ props })}<Button {...props} variant="ghost" size="disclosure"><span>高级设置</span><span class="provider-editor-expand-hint">{limitSummary}</span><ChevronDown class={advanced ? "size-4 rotate-180" : "size-4"}/></Button>{/snippet}</Collapsible.Trigger>
          <Collapsible.Content forceMount class="provider-editor-advanced-content">
            <p class="provider-editor-hint">RPM 与并发留空表示不限；熔断探测留空使用默认值。</p>
            <Field.Group class="provider-editor-fields">
              <Field.Field orientation="horizontal" class="provider-setting-row"><Field.Label for="key-rpm">RPM 限制</Field.Label><Field.Content><Input id="key-rpm" type="number" min={1} max={10000} step={1} bind:value={rpm} placeholder="不限制" /></Field.Content></Field.Field>
              <Field.Field orientation="horizontal" class="provider-setting-row"><Field.Label for="key-concurrency">并发上限</Field.Label><Field.Content><Input id="key-concurrency" type="number" min={0} step={1} bind:value={concurrency} placeholder="不限制" /></Field.Content></Field.Field>
              <Field.Field orientation="horizontal" class="provider-setting-row"><Field.Label for="key-probe">熔断探测</Field.Label><Field.Content><div class="provider-number-control"><Input id="key-probe" type="number" min={1} max={32} step={1} bind:value={probe} placeholder={String(defaults.keyProbeMinutes)}/><span>分钟</span></div><Field.Description>只限制失败后的最长探测间隔。</Field.Description></Field.Content></Field.Field>
              <ProxyField bind:value={draft.proxy} horizontal />
            </Field.Group>
          </Collapsible.Content>
        </Collapsible.Root>
      </section>

      <Collapsible.Root bind:open={syncOpen} class="key-sync-section" data-key-section="sync">
        <Collapsible.Trigger disabled={busy}>{#snippet child({ props })}
          <Button {...props} variant="ghost" size="disclosure" class="key-sync-trigger">
            <span class="key-sync-title">上游同步 <Badge variant="neutral">可选</Badge></span>
            <span class="key-sync-summary">{syncSummary}</span>
            <ChevronDown class={syncOpen ? "size-4 rotate-180" : "size-4"}/>
          </Button>
        {/snippet}</Collapsible.Trigger>
        <Collapsible.Content forceMount class="key-sync-content">
          <p class="key-sync-intro">默认由你手动维护名称和倍率。只有选择上游来源时，才需要使用供应商连接查询。</p>
          {#if queryLoading}<p class="text-xs text-muted-foreground">正在读取查询配置…</p>{:else if queryError}<Alert.Root variant="inline-error"><Alert.Description>{queryError}</Alert.Description></Alert.Root>{:else}<KeyQueryFields bind:draft={queryDraft} {queryState} {providerQueryMode} disabled={busy}/>{/if}
        </Collapsible.Content>
      </Collapsible.Root>
    </Field.Set>
  </div>
  {#if error}<Alert.Root variant="inline-error"><Alert.Description>{error}</Alert.Description></Alert.Root>{/if}
  <footer class="provider-editor-footer key-editor-footer">
    <div class="key-editor-actions"><Button type="button" variant="outline" disabled={busy} onclick={onclose}>取消</Button><Button type="submit" disabled={busy || queryLoading || !!queryError}>{busy ? "保存中…" : item ? "保存更改" : "添加 Key"}</Button></div>
  </footer>
</form>
