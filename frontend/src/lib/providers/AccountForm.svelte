<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { onMount } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import * as Field from "$lib/components/ui/field";
  import { VelaService, type ProviderAccount, type ProviderAccountDraft } from "$lib/generated/mygo";
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
  /** 弹窗的独立配置草稿；旧月配额与签到字段不展示，随保存兼容保留。 */
  let draft = $state<ProviderAccountDraft>({ config: defaultQueryConfig(), monthlyQuota: 0, checkin: false });
  /** 后端返回的最近账户结果；失败保留最后成功值。 */
  let account = $state<ProviderAccount | null>(null);
  /** 加载成功前不允许以空草稿覆盖已有配置。 */
  let loading = $state(true);
  /** 当前正在执行的验证或保存动作；null 表示可继续编辑。 */
  let pending = $state<"validate" | "save" | null>(null);
  /** 任一验证或保存动作执行期间锁定控件与关闭入口。 */
  const saving = $derived(pending !== null);
  /** 加载、验证或保存失败的可重试反馈。 */
  let error = $state("");
  /** 配置持久化与后续查询分别反馈，查询失败不显示为保存失败。 */
  let feedback = $state("");
  /** 验证结果是否包含余额或 Key 同步失败，用于区分成功与错误状态。 */
  let feedbackFailed = $state(false);
  /** 最近一次验证后的草稿指纹，用于判断反馈是否仍对应当前输入。 */
  let validatedFingerprint = $state("");
  /** 当前草稿是否已完整验证成功，供保存动作避免重复发起上游查询。 */
  let validationSucceeded = $state(false);
  /** 生命周期标记防止卸载后继续通知父组件。 */
  let active = true;

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
    // 已隐藏的兼容字段读回草稿，保存时原样写回，避免旧配置被清空。
    draft = { config: { ...result.config, baseUrl: result.config.baseUrl || baseURL, method: result.config.method || "GET", currency: result.config.currency || "USD", quotaDivisor: result.config.quotaDivisor || 500000 }, monthlyQuota: result.monthlyQuota, checkin: result.checkin };
  }

  /** 草稿指纹只用于识别验证后是否继续修改，不参与保存或查询载荷。 */
  function fingerprint(): string {
    return JSON.stringify($state.snapshot(draft));
  }

  /** 仅在验证反馈仍对应当前草稿时展示，后续输入变化不会保留过期成功提示。 */
  const feedbackCurrent = $derived(Boolean(feedback) && validatedFingerprint === fingerprint());

  /** 汇总复用供应商连接且需要上游字段的 Key，区分无需同步、成功和失败。 */
  async function inspectKeySync(): Promise<{ checked: number; failedKeys: number; reason: string }> {
    const keys = await VelaService.providerKeys(providerID);
    const queries = await Promise.all(keys.map(async (key) => ({ key, query: await VelaService.providerKeyQuery(key.id) })));
    const relevant = queries.filter(({ query }) => query.reuseProvider && (query.nameSource !== "manual" || query.multiplierSource !== "manual"));
    const failed = new Map<string, string>();
    for (const { key, query } of relevant) {
      const nameFailed = query.nameSource !== "manual" && query.nameStatus.state === "failed";
      const multiplierFailed = query.multiplierSource !== "manual" && query.multiplierStatus.state === "failed";
      if (nameFailed || multiplierFailed) {
        const reason = (nameFailed ? query.nameStatus.error : "") || (multiplierFailed ? query.multiplierStatus.error : "") || "所选上游字段同步失败";
        failed.set(key.id, reason);
      }
    }
    return { checked: relevant.length, failedKeys: failed.size, reason: failed.values().next().value ?? "" };
  }

  /** 保存当前草稿并立即验证余额及复用 Key；验证动作保留弹窗，保存动作成功后关闭。 */
  async function run(intent: "validate" | "save"): Promise<void> {
    if (saving || loading || !account) return;
    if (!draft.config.mode) { error = "请选择接入方式"; return; }
    pending = intent; error = ""; feedback = ""; feedbackFailed = false; validationSucceeded = false;
    try {
      const result = await VelaService.saveProviderAccount(providerID, { ...draft, config: queryConfigDraft(draft.config), checkin: draft.config.mode === "newapi" && draft.checkin });
      if (!active) return;
      applyAccount(result);
      const currentFingerprint = fingerprint();
      let keySync: Awaited<ReturnType<typeof inspectKeySync>> | null = null;
      let keySyncError = "";
      try { keySync = await inspectKeySync(); } catch (cause) { keySyncError = String(cause); }
      let refreshError = "";
      try { await onupdated(); } catch (cause) { refreshError = String(cause); if (active) error = `配置已保存，但列表刷新失败：${refreshError}`; }
      if (!active) return;
      const balanceFailed = result.balance.status.state === "failed";
      const balanceReason = result.balance.status.error.trim();
      feedbackFailed = balanceFailed || Boolean(keySyncError) || Boolean(keySync?.failedKeys) || Boolean(refreshError);
      if (refreshError) {
        feedback = `验证结果已返回，但列表刷新失败：${refreshError}。请重试保存。`;
      } else if (balanceFailed) {
        feedback = `验证失败：${balanceReason || "无法查询供应商余额，请检查连接配置"}。配置已保存，可修改后重试。`;
      } else if (keySyncError) {
        feedback = `验证结果不完整：余额查询成功，但无法读取 Key 同步状态（${keySyncError}）。列表已更新，可再次验证。`;
      } else if (keySync?.failedKeys) {
        feedback = `验证部分失败：余额查询成功，但 ${keySync.failedKeys} 把复用 Key 的信息同步失败（${keySync.reason}）。配置已保存，可修改后重试。`;
      } else if (keySync?.checked) {
        feedback = `验证成功：已查询供应商余额并同步 ${keySync.checked} 把复用 Key，列表已更新。`;
      } else {
        feedback = "验证成功：已查询供应商余额；没有需要同步的复用 Key，列表已更新。";
      }
      validatedFingerprint = currentFingerprint;
      validationSucceeded = !feedbackFailed;
      notify(refreshError ? "配置已保存，但列表刷新失败，请重试" : feedbackFailed ? "验证未完全通过，请检查账户配置" : intent === "save" ? "已保存，列表已更新" : "验证成功，列表已更新");
      if (intent === "save" && !feedbackFailed) onclose();
    } catch (cause) {
      if (active) error = `${intent === "validate" ? "验证" : "保存"}失败：${String(cause)}`;
    } finally {
      if (active) pending = null;
    }
  }

  /** 保存按钮仅在当前草稿已验证成功时直接关闭，否则执行完整的保存与验证。 */
  async function save(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (validationSucceeded && validatedFingerprint === fingerprint()) {
      onclose();
      return;
    }
    await run("save");
  }

  /** 关闭动作与父对话框忙碌保护使用相同条件。 */
  function close(): void { if (!loading && !saving) onclose(); }
</script>

<form class="account-form" onsubmit={save} aria-busy={loading || saving}>
  {#if loading}<p class="status" role="status">正在读取账户配置…</p>{/if}
  {#if account}
    <Field.Set disabled={loading || saving} class="min-w-0"><Field.Group>
      <QueryConnection bind:value={draft.config} />
    </Field.Group></Field.Set>
  {/if}
  <div class="account-action-area">
    {#if feedbackCurrent}<p class="status" class:failed={feedbackFailed} role="status">{feedback}</p>{/if}
    {#if error}<Alert.Root variant="inline-error"><Alert.Description>{error}</Alert.Description></Alert.Root>{/if}
    <footer>
      <Button type="button" variant="outline" disabled={loading || saving} onclick={close}>{feedbackCurrent ? "关闭" : "取消"}</Button>
      {#if !account && !loading}
        <Button type="button" variant="outline" onclick={() => void load()}>重新读取</Button>
      {:else}
        <Button type="button" variant="outline" disabled={loading || saving || !account} onclick={() => void run("validate")}>{pending === "validate" ? "正在验证…" : "验证"}</Button>
        <Button type="submit" disabled={loading || saving || !account}>{pending === "save" ? "正在保存…" : "保存"}</Button>
      {/if}
    </footer>
  </div>
</form>

<style>
  .account-form { display: flex; flex-direction: column; gap: 18px; min-width: 0; }

  .status { color: var(--positive); font-size: 12px; line-height: 1.6; }
  .status.failed { color: var(--destructive); }
  .account-action-area { position: sticky; bottom: 0; z-index: 1; display: flex; flex-direction: column; gap: 10px; padding-top: 14px; border-top: 1px solid var(--border); background: var(--background); }
  footer { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; }
</style>
