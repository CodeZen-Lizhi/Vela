<script lang="ts">
  import * as Field from "$lib/components/ui/field";
  import { Input } from "$lib/components/ui/input";
  import { Badge } from "$lib/components/ui/badge";
  import type { ProviderKeyQuery, ProviderKeyQueryDraft } from "$lib/generated/mygo";
  import Choice from "./Choice.svelte";
  import QueryConnection from "./QueryConnection.svelte";
  import { defaultKeyQuery } from "./query-defaults";

  /** 只维护上游同步草稿；读取、保存和 Key 生命周期仍由父级负责。 */
  let { draft = $bindable(defaultKeyQuery()), disabled = false, queryState = null, providerQueryMode = "" }: {
    /** 名称来源、倍率来源及当前 Key 的查询连接草稿。 */
    draft?: ProviderKeyQueryDraft;
    /** 父表单加载或保存时统一禁用字段。 */
    disabled?: boolean;
    /** 已保存配置的最近同步状态；新增 Key 尚未查询时为空。 */
    queryState?: ProviderKeyQuery | null;
    /** 复用供应商连接时实际使用的查询方式；用于隐藏内置查询不适用的字段路径。 */
    providerQueryMode?: string;
  } = $props();

  /** 为同页多个编辑器生成唯一字段身份。 */
  const id = $props.id();
  /** 只有名称或倍率选择上游来源时才需要配置查询连接。 */
  const hasUpstreamSource = $derived(draft.nameSource !== "manual" || draft.multiplierSource !== "manual");
  /** 当前 Key 实际使用的查询方式；复用连接时以供应商账户配置为准。 */
  const queryMode = $derived(draft.reuseProvider ? providerQueryMode : draft.config.mode);
  /** 只有自定义查询返回当前 Key 对象时，才需要声明响应字段路径。 */
  const needsPathFields = $derived(hasUpstreamSource && queryMode === "custom");
  /** 最近同步只显示当前真正由上游维护的字段，手动项不制造同步状态。 */
  const statuses = $derived(queryState ? [
    { label: "名称", source: queryState.nameSource, status: queryState.nameStatus },
    { label: "倍率", source: queryState.multiplierSource, status: queryState.multiplierStatus },
  ].filter((entry) => entry.source !== "manual") : []);

  /** Choice 使用字符串值，绑定函数仍把复用选择保存在布尔契约中。 */
  const reuseOptions = [{ value: "provider", label: "复用供应商余额连接" }, { value: "separate", label: "单独配置" }];
  /** 只转换当前选择，不复制供应商凭据。 */
  function getConnectionMode(): string { return draft.reuseProvider ? "provider" : "separate"; }
  /** 切换连接归属时保留尚未保存的独立连接草稿。 */
  function setConnectionMode(value: string): void { draft.reuseProvider = value === "provider"; }
</script>

<Field.Set {disabled} class="key-sync-fields">
  <Field.Field orientation="horizontal" class="provider-setting-row">
    <Field.Label>名称来源</Field.Label>
    <Field.Content>
      <Choice bind:value={draft.nameSource} label="Key 名称来源" {disabled} options={[{ value: "manual", label: "手动填写" }, { value: "upstream_key", label: "上游 Key 名称" }, { value: "upstream_group", label: "上游分组名称" }]} />
      <Field.Description>名称与倍率来源独立；查询失败时保留当前名称。</Field.Description>
    </Field.Content>
  </Field.Field>

  {#if hasUpstreamSource}
    <div class="key-sync-divider" aria-hidden="true"></div>
    <Field.Field orientation="horizontal" class="provider-setting-row">
      <Field.Label>查询连接</Field.Label>
      <Field.Content>
        <Choice bind:value={getConnectionMode, setConnectionMode} options={reuseOptions} label="Key 查询连接" {disabled} />
        {#if draft.reuseProvider}
          <Field.Description>继续跟随供应商余额连接的地址与认证；余额查询成功不代表当前 Key 信息可查询。</Field.Description>
        {:else}
          <Field.Description>仅影响当前 Key 的名称与倍率查询，不改变供应商余额连接。</Field.Description>
        {/if}
      </Field.Content>
    </Field.Field>
    {#if !draft.reuseProvider}<QueryConnection bind:value={draft.config} forKey />{/if}
    {#if needsPathFields}
      <section class="key-path-section" aria-labelledby={`${id}-paths-title`}>
        <div class="key-path-heading"><h3 id={`${id}-paths-title`}>响应字段路径</h3><p>{draft.reuseProvider ? "仅当供应商使用自定义查询时生效；通过完整 Key 精确匹配当前对象。" : "仅支持当前 Key 的对象响应，不支持列表或动态模板。"}</p></div>
        <Field.Group class="provider-fields-grid">
          {#if draft.nameSource === "upstream_key"}<Field.Field><Field.Label for={`${id}-name-path`}>上游 Key 名称字段</Field.Label><Input id={`${id}-name-path`} bind:value={draft.namePath} placeholder="data.name" /></Field.Field>{:else if draft.nameSource === "upstream_group"}<Field.Field><Field.Label for={`${id}-group-path`}>分组名称字段</Field.Label><Input id={`${id}-group-path`} bind:value={draft.groupPath} placeholder="data.group" /></Field.Field>{/if}
          {#if draft.multiplierSource === "upstream"}<Field.Field><Field.Label for={`${id}-multiplier-path`}>倍率字段</Field.Label><Input id={`${id}-multiplier-path`} bind:value={draft.multiplierPath} placeholder="data.multiplier" /></Field.Field>{/if}
          {#if draft.reuseProvider}<Field.Field><Field.Label for={`${id}-key-path`}>完整 Key 字段路径</Field.Label><Input id={`${id}-key-path`} bind:value={draft.keyPath} placeholder="data.key" /><Field.Description>只填字段路径；后端用完整密钥匹配，无法唯一确认时不更新。</Field.Description></Field.Field>{/if}
        </Field.Group>
      </section>
    {/if}
    {#if statuses.length}
      <section class="query-status" aria-label="已保存配置的最近同步状态">
        <header class="query-status-heading"><div><h3>最近同步</h3><p>以下结果来自已保存配置；来源或连接修改将在保存后生效。</p></div>{#if queryState?.groupName}<Badge variant="neutral">分组 · {queryState.groupName}</Badge>{/if}</header>
        <div class="query-status-list">{#each statuses as entry}{@const lastSuccess=entry.status.lastSuccess && !entry.status.lastSuccess.startsWith("0001-")}<div class="query-status-row"><div class="query-status-copy"><strong>{entry.label}</strong><small>{lastSuccess ? `最后成功：${new Date(entry.status.lastSuccess).toLocaleString()}` : "尚未成功同步"}</small></div><Badge variant={entry.status.state === "success" ? "positive" : entry.status.state === "failed" ? "negative" : "neutral"}>{entry.status.state === "success" ? "同步成功" : entry.status.state === "failed" ? "同步失败" : "尚未同步"}</Badge>{#if entry.status.error}<p>{entry.status.error}</p>{:else if entry.status.state === "failed"}<p>{lastSuccess ? "当前值不是本次查询结果，已保留上次成功值。" : "当前值不代表上游同步结果。"}</p>{/if}</div>{/each}</div>
      </section>
    {/if}
  {:else}
    <div class="key-sync-idle"><strong>当前不查询上游</strong><span>名称和倍率均由你手动维护；将任一来源改为上游后才需要连接配置。</span></div>
  {/if}
</Field.Set>

<style>
  :global(.key-sync-fields) { gap: 18px; }
  .key-sync-divider { height: 1px; background: var(--border); }
  .key-path-section { display: flex; flex-direction: column; gap: 14px; padding: 15px; border: 1px solid var(--border); border-radius: 10px; background: color-mix(in srgb, var(--muted) 28%, var(--background)); }
  .key-path-heading h3, .query-status-heading h3 { font-size: 13px; font-weight: 600; }
  .key-path-heading p, .query-status-heading p { margin-top: 4px; color: var(--muted-foreground); font-size: 11px; line-height: 1.55; }
  .key-sync-idle { display: flex; flex-direction: column; gap: 3px; padding: 13px 14px; border: 1px dashed var(--border); border-radius: 10px; color: var(--muted-foreground); font-size: 11px; line-height: 1.55; }
  .key-sync-idle strong { color: var(--foreground); font-size: 12px; font-weight: 500; }
  .query-status { padding: 15px; border: 1px solid var(--border); border-radius: 10px; min-width: 0; }
  .query-status-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
  .query-status-list { margin-top: 12px; border-top: 1px solid var(--border); }
  .query-status-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 4px 12px; padding-top: 11px; border-top: 1px solid var(--border); }
  .query-status-row:first-child { border-top: 0; }
  .query-status-copy { display: flex; min-width: 0; flex-direction: column; gap: 3px; }
  .query-status-copy strong { font-size: 12px; font-weight: 500; }
  .query-status-copy small, .query-status-row p { color: var(--muted-foreground); font-size: 11px; line-height: 1.55; overflow-wrap: anywhere; }
  .query-status-row p { grid-column: 1 / -1; color: var(--destructive); }
</style>
