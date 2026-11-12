<script lang="ts">
  import * as Field from "$lib/components/ui/field";
  import { Input } from "$lib/components/ui/input";
  import type { ProviderKeyQuery, ProviderKeyQueryDraft } from "$lib/generated/mygo";
  import Choice from "./Choice.svelte";
  import QueryConnection from "./QueryConnection.svelte";
  import MultiplierSource from "./MultiplierSource.svelte";
  import { defaultKeyQuery } from "./query-defaults";

  /** 可嵌入新增或编辑 Key 的纯草稿字段，不负责读取、保存或创建 Key。 */
  let { draft = $bindable(defaultKeyQuery()), disabled = false, queryState = null }: {
    /** 名称来源、倍率来源及当前 Key 的查询配置草稿。 */
    draft?: ProviderKeyQueryDraft;
    /** 父表单加载或保存时统一禁用字段。 */
    disabled?: boolean;
    /** 已保存配置的最近查询状态；新增 Key 未查询时为空，不参与保存。 */
    queryState?: ProviderKeyQuery | null;
  } = $props();
  /** 为多个 Key 编辑器生成独立的标签身份。 */
  const id = $props.id();
  /** 已保存名称与倍率分别展示查询结果，不把未保存的来源选择当作已成功同步。 */
  const statuses = $derived(queryState ? [
    { label: "名称", source: queryState.nameSource, status: queryState.nameStatus },
    { label: "倍率", source: queryState.multiplierSource, status: queryState.multiplierStatus },
  ] : []);
  /** Choice 使用字符串值，派生绑定仍将复用选择保存在布尔契约中。 */
  const reuseOptions = [{ value: "provider", label: "复用供应商余额连接" }, { value: "separate", label: "单独配置" }];
  /** 只把当前选择转换为下拉值，不复制供应商的凭据。 */
  function getConnectionMode(): string { return draft.reuseProvider ? "provider" : "separate"; }
  /** 更新草稿中的连接归属，切换时保留尚未保存的独立连接草稿。 */
  function setConnectionMode(value: string): void { draft.reuseProvider = value === "provider"; }
</script>

<fieldset {disabled} class="query-fields">
  <Field.Set><Field.Legend>信息查询</Field.Legend><Field.Group>
    <Field.Field><Field.Label>名称来源</Field.Label><Choice bind:value={draft.nameSource} label="Key 名称来源" {disabled} options={[{ value: "manual", label: "手动填写" }, { value: "upstream_key", label: "上游 Key 名称" }, { value: "upstream_group", label: "上游分组名称" }]} /><Field.Description>与倍率来源独立。上游查询失败时保留当前名称。</Field.Description></Field.Field>
    <Field.Field><Field.Label>倍率来源</Field.Label><MultiplierSource value={draft.multiplierSource} label="Key 倍率来源" {disabled} onchange={(value) => draft.multiplierSource = value}/><Field.Description>手动项不会被同步覆盖；自动跟随上游倍率，0 表示免费，未知不视为 1。</Field.Description></Field.Field>
    {#if queryState}
      <section class="query-status" aria-label="已保存配置的最近查询状态">
        <h3>最近查询状态</h3>
        <p class="status-help">以下结果来自已保存配置；来源和连接的草稿修改将在保存后生效。</p>
        {#if queryState.groupName}<p class="status-help">上游分组：{queryState.groupName}</p>{/if}
        {#each statuses as entry}
          {@const lastSuccess = entry.status.lastSuccess && !entry.status.lastSuccess.startsWith("0001-")}
          <div class="status-entry" class:failed={entry.source !== "manual" && entry.status.state === "failed"}>
            <strong>{entry.label}：{entry.source === "manual" ? "手动维护" : entry.status.state === "success" ? "同步成功" : entry.status.state === "failed" ? "同步失败" : "尚未同步"}</strong>
            {#if entry.source !== "manual"}
              {#if entry.status.error}<p>失败原因：{entry.status.error}</p>{/if}
              {#if entry.status.state === "failed"}<p>{lastSuccess ? "保留上次成功值，当前值不是本次查询结果。" : "尚无成功同步记录，当前值不代表上游同步结果。"}</p>{/if}
              <small>最后成功：{lastSuccess ? new Date(entry.status.lastSuccess).toLocaleString() : "从未成功"}</small>
            {/if}
          </div>
        {/each}
      </section>
    {/if}
    <Field.Field><Field.Label>查询连接</Field.Label><Choice bind:value={getConnectionMode, setConnectionMode} options={reuseOptions} label="Key 查询连接" {disabled} /></Field.Field>
    {#if draft.reuseProvider}
      <Field.Description>继续跟随供应商余额连接的地址与认证。余额查询成功不代表支持当前 Key 的名称、倍率或分组查询。</Field.Description>
    {:else}
      <QueryConnection bind:value={draft.config} forKey />
    {/if}
    {#if draft.reuseProvider || draft.config.mode === "custom"}
      <Field.Set><Field.Legend variant="label">自定义响应字段路径</Field.Legend><Field.Description>{draft.reuseProvider ? "仅在供应商使用自定义 URL 时生效。复用响应须通过完整 Key 字段确认归属。" : "仅支持当前 Key 的对象响应，不支持列表或动态模板。"}</Field.Description><Field.Group>
        <Field.Field><Field.Label for={`${id}-name-path`}>上游 Key 名称字段</Field.Label><Input id={`${id}-name-path`} bind:value={draft.namePath} placeholder="data.name" /><Field.Description>名称来源为上游 Key 名称时使用。</Field.Description></Field.Field>
        <Field.Field><Field.Label for={`${id}-group-path`}>分组名称字段</Field.Label><Input id={`${id}-group-path`} bind:value={draft.groupPath} placeholder="data.group" /></Field.Field>
        <Field.Field><Field.Label for={`${id}-multiplier-path`}>倍率字段</Field.Label><Input id={`${id}-multiplier-path`} bind:value={draft.multiplierPath} placeholder="data.multiplier" /></Field.Field>
        {#if draft.reuseProvider}
          <Field.Field><Field.Label for={`${id}-key-path`}>完整 Key 字段路径</Field.Label><Input id={`${id}-key-path`} bind:value={draft.keyPath} placeholder="data.key" /><Field.Description>只填写字段路径，后端使用完整密钥精确匹配；无法确认归属时不更新。</Field.Description></Field.Field>
        {/if}
      </Field.Group></Field.Set>
    {/if}
  </Field.Group></Field.Set>
</fieldset>

<style>
  .query-fields { margin: 0; padding: 0; border: 0; min-width: 0; }
  .query-status { padding: 14px; border: 1px solid var(--border); border-radius: 10px; min-width: 0; }
  .query-status h3 { font-size: 13px; font-weight: 600; }
  .status-help, .status-entry small { color: var(--muted-foreground); font-size: 12px; line-height: 1.6; }
  .status-help { margin-top: 6px; }
  .status-entry { display: flex; flex-direction: column; gap: 4px; margin-top: 12px; font-size: 12px; overflow-wrap: anywhere; }
  .status-entry strong { font-weight: 500; }
  .failed { color: var(--destructive); }
</style>
