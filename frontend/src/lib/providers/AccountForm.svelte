<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import * as Collapsible from "$lib/components/ui/collapsible";
  import { onMount } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Switch } from "$lib/components/ui/switch";
  import * as Field from "$lib/components/ui/field";
  import { VelaService, type ProviderAccount, type ProviderAccountDraft } from "$lib/generated/mygo";
  import SubscriptionSummary from "./SubscriptionSummary.svelte";
  import QueryConnection from "./QueryConnection.svelte";
  import { defaultQueryConfig, queryConfigDraft } from "./query-defaults";

  /** 独立维护供应商余额连接，读取和保存均只经桌面绑定访问后端。 */
  let { providerID, baseURL, notify, onclose, onupdated, onbusy }: {
    /** 当前供应商稳定身份。 */
    providerID: string;
    /** 新连接默认使用的供应商服务地址。 */
    baseURL: string;
    /** 父页面轻量操作反馈。 */
    notify: (message: string) => void;
    /** 取消或完成编辑时关闭父对话框。 */
    onclose: () => void;
    /** 保存完成后刷新父页面的真实数据。 */
    onupdated: () => Promise<void>;
    /** 父对话框据此阻止 Escape、遮罩和关闭按钮中断保存。 */
    onbusy?: (busy: boolean) => void;
  } = $props();
  /** 弹窗的独立配置草稿，查询结果不进入保存载荷。 */
  let draft = $state<ProviderAccountDraft>({ config: defaultQueryConfig(), monthlyQuota: 0, checkin: false });
  /** 后端返回的最近账户结果；失败保留最后成功值。 */
  let account = $state<ProviderAccount | null>(null);
  /** 加载成功前不允许以空草稿覆盖已有配置。 */
  let loading = $state(true);
  /** 保存时锁定全部可编辑控件与关闭动作。 */
  let saving = $state(false);
  /** 加载或保存失败的可重试反馈。 */
  let error = $state("");
  /** 配置持久化与后续查询分别反馈，查询失败不显示为保存失败。 */
  let feedback = $state("");
  /** 生命周期标记防止卸载后继续通知父组件。 */
  let active = true;
  /** 多实例表单使用独立控件身份。 */
  const id = $props.id();
  /** 汇总当前账户各查询项的独立状态。 */
  const statuses = $derived(account ? [
    { label: "余额查询", status: account.balance.status },
    ...(account.config.mode === "sub2api" ? [{ label: "订阅查询", status: account.balance.subscriptionStatus }] : []),
    ...(account.checkin && account.config.mode === "newapi" ? [{ label: "签到", status: account.balance.checkin }] : []),
  ] : []);

  $effect(() => { onbusy?.(loading || saving); });
  onMount(() => { void load(); return () => { active = false; onbusy?.(false); }; });

  /** 只读本地账户配置，打开弹窗不触发上游查询。 */
  async function load(): Promise<void> {
    loading = true; error = "";
    try {
      const result = await VelaService.providerAccount(providerID);
      if (!active) return;
      applyAccount(result);
    } catch (cause) {
      if (active) error = String(cause);
    } finally { if (active) loading = false; }
  }

  /** 保存后重新使用脱敏配置，清除内存中已提交的新凭据。 */
  function applyAccount(result: ProviderAccount): void {
    account = result;
    draft = { config: { ...result.config, baseUrl: result.config.baseUrl || baseURL, method: result.config.method || "GET", currency: result.config.currency || "USD", quotaDivisor: result.config.quotaDivisor || 500000 }, monthlyQuota: result.monthlyQuota, checkin: result.checkin };
  }

  /** 保存配置并展示后端查询状态；失败保留草稿，成功后刷新父列表但不自动关闭反馈。 */
  async function save(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (saving || loading || !account) return;
    if (!draft.config.mode) { error = "请选择接入方式"; return; }
    saving = true; error = ""; feedback = "";
    try {
      const result = await VelaService.saveProviderAccount(providerID, { ...draft, config: queryConfigDraft(draft.config), checkin: draft.config.mode === "newapi" && draft.checkin });
      if (!active) return;
      applyAccount(result);
      feedback = result.balance.status.state === "failed" ? "账户配置已保存，余额查询失败；已有有效余额保留。" : "账户配置已保存。";
      notify(feedback);
      try { await onupdated(); } catch (cause) { if (active) error = `配置已保存，但列表刷新失败：${String(cause)}`; }
    } catch (cause) { if (active) error = `保存失败：${String(cause)}`; }
    finally { if (active) saving = false; }
  }

  /** 关闭动作与父对话框忙碌保护使用相同条件。 */
  function close(): void { if (!loading && !saving) onclose(); }
</script>

<form class="account-form" onsubmit={save} aria-busy={loading || saving}>
  {#if loading}<p class="status" role="status">正在读取账户配置…</p>{/if}
  {#if account}
    <Field.Set disabled={loading || saving} class="min-w-0"><Field.Group>
      <QueryConnection bind:value={draft.config} />
      <Collapsible.Root class="account-options"><Collapsible.Trigger>{#snippet child({ props })}<Button {...props} variant="text" size="inline">配额与签到 <span class="text-muted-foreground">月配额：{draft.monthlyQuota || "不限"}</span></Button>{/snippet}</Collapsible.Trigger><Collapsible.Content><Field.Group class="pt-4">
        <Field.Field><Field.Label for={`${id}-quota`}>本地月配额（美元）</Field.Label><Input id={`${id}-quota`} type="number" min={0} step="any" bind:value={draft.monthlyQuota} required /><Field.Description>0 表示不限；与上游余额及订阅额度分别保存。</Field.Description></Field.Field>
        {#if draft.config.mode === "newapi"}<Field.Field orientation="horizontal"><Field.Label for={`${id}-checkin`}>余额查询时顺带签到</Field.Label><Switch id={`${id}-checkin`} bind:checked={draft.checkin} disabled={saving} /></Field.Field><Field.Description>默认关闭，签到失败不阻断余额查询。</Field.Description>{/if}
      </Field.Group></Collapsible.Content></Collapsible.Root>
    </Field.Group></Field.Set>
    <section class="query-result" aria-label="最近账户查询结果">
      <h3>最近查询结果</h3>
      <dl><div><dt>余额</dt><dd>{account.balance.amount === null ? "未知" : `${account.balance.amount} ${account.balance.currency || "USD"}`}</dd></div>
        {#if account.balance.cash !== null}<div><dt>现金</dt><dd>{account.balance.cash} {account.balance.currency}</dd></div>{/if}
        {#if account.balance.points !== null}<div><dt>积分</dt><dd>{account.balance.points}</dd></div>{/if}
        {#if account.balance.used !== null}<div><dt>上游已用</dt><dd>{account.balance.used} {account.balance.currency}</dd></div>{/if}
      </dl>
      {#if account.config.mode === "sub2api" && account.balance.subscription}<SubscriptionSummary value={account.balance.subscription}/>{/if}
      {#each statuses as item}<div class="query-state" class:failed={item.status.state === "failed"}><strong>{item.label}：{item.status.state === "success" ? "成功" : item.status.state === "failed" ? "失败" : "尚未查询"}</strong>{#if item.status.error}<p>{item.status.error}</p>{/if}{#if item.status.lastSuccess && !item.status.lastSuccess.startsWith("0001-")}<small>最后成功：{new Date(item.status.lastSuccess).toLocaleString()}</small>{/if}</div>{/each}
    </section>
    <p class="status">定时刷新统一在“设置 → 账户”中配置。</p>
  {/if}
  {#if feedback}<p class="status" role="status">{feedback}</p>{/if}
  {#if error}<Alert.Root variant="inline-error"><Alert.Description>{error}</Alert.Description></Alert.Root>{/if}
  <footer><Button type="button" variant="outline" disabled={loading || saving} onclick={close}>{feedback ? "完成" : "取消"}</Button>{#if !account && !loading}<Button type="button" variant="outline" onclick={() => void load()}>重新读取</Button>{:else}<Button type="submit" disabled={loading || saving || !account}>{saving ? "保存并查询中…" : "保存更改"}</Button>{/if}</footer>
</form>

<style>
  .account-form { display: flex; flex-direction: column; gap: 18px; min-width: 0; }

  :global(.account-options) { padding-top: 14px; border-top: 1px solid var(--border); }

  .query-result { padding: 14px; border: 1px solid var(--border); border-radius: 10px; min-width: 0; }
  h3 { margin-bottom: 12px; font-size: 13px; font-weight: 600; }
  dl { display: flex; flex-wrap: wrap; gap: 16px; } dt { font-size: 12px; color: var(--muted-foreground); } dd { margin: 3px 0 0; font-size: 14px; }
  .query-state { display: flex; flex-direction: column; gap: 4px; margin-top: 12px; font-size: 12px; overflow-wrap: anywhere; }
  .query-state strong { font-weight: 500; } .query-state small, .status { color: var(--muted-foreground); font-size: 12px; line-height: 1.6; }
  .failed { color: var(--destructive); }
  footer { display: flex; justify-content: flex-end; gap: 8px; padding-top: 14px; border-top: 1px solid var(--border); }
</style>
