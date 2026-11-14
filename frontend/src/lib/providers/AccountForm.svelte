<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { onMount } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import * as Field from "$lib/components/ui/field";
  import { VelaService, type ProviderAccount, type ProviderAccountDraft, type ProviderAccountValidation } from "$lib/generated/mygo";
  import QueryConnection from "./QueryConnection.svelte";
  import { defaultQueryConfig, queryConfigDraft } from "./query-defaults";

  /** 独立维护供应商余额草稿；保存不联网，验证结果仅在同一草稿保存时应用。 */
  let { providerID, baseURL, notify, onclose, onupdated, onbusy }: {
    /** 当前供应商稳定身份。 */
    providerID: string;
    /** 新连接默认使用的供应商服务地址。 */
    baseURL: string;
    /** 父页面轻量操作反馈。 */
    notify: (message: string) => void;
    /** 取消或保存成功后关闭父对话框。 */
    onclose: () => void;
    /** 重读父页面的本地账户与 Key 状态。 */
    onupdated: () => Promise<void>;
    /** 加载、验证和保存期间保护父弹窗关闭入口。 */
    onbusy?: (busy: boolean) => void;
  } = $props();
  /** 旧月配额与签到字段兼容保留；验证引用仅在提交保存时附加。 */
  let draft = $state<ProviderAccountDraft>({ config: defaultQueryConfig(), monthlyQuota: 0, checkin: false, validationId: "" });
  /** 已保存账户只用于初始化草稿，临时验证不覆盖它。 */
  let account = $state<ProviderAccount | null>(null);
  /** 完成读取后才能编辑，避免空草稿覆盖旧配置。 */
  let loading = $state(true);
  /** 当前操作；Key 后台同步不占用弹窗忙碌态。 */
  let pending = $state<"validate" | "save" | null>(null);
  /** 验证和保存执行期间锁定输入与关闭入口。 */
  const saving = $derived(pending !== null);
  /** 本地读取、保存或无法执行验证时的错误。 */
  let error = $state("");
  /** 最近一份临时余额结果；ID 由后端持有的草稿验证产生。 */
  let validation = $state<ProviderAccountValidation | null>(null);
  /** 将反馈绑定到验证时的完整输入；任何修改都会永久失效该次结果。 */
  let validatedFingerprint = $state("");
  /** 组件卸载后停止前台反馈，并清除临时验证引用。 */
  let active = true;
  /** 临时反馈只对当前输入生效。 */
  const feedbackCurrent = $derived(validation !== null && validatedFingerprint === fingerprint());

  $effect(() => { onbusy?.(loading || saving); });
  $effect(() => {
    if (validation && validatedFingerprint !== fingerprint()) {
      releaseValidation(validation.id);
      validation = null;
    }
  });
  onMount(() => {
    void load();
    return () => { active = false; releaseValidation(validation?.id ?? ""); onbusy?.(false); };
  });

  /** 释放只存在于后端内存的临时引用；清理失败不改变账户，后续验证或保存仍会替换它。 */
  function releaseValidation(id: string): void {
    if (id) void VelaService.discardProviderAccountValidation(providerID, id).catch(() => undefined);
  }
  /** 只读本地配置，打开弹窗不触发上游查询。 */
  async function load(): Promise<void> {
    loading = true; error = "";
    try {
      const result = await VelaService.providerAccount(providerID);
      if (!active) return;
      account = result;
      draft = { config: { ...result.config, baseUrl: result.config.baseUrl || baseURL, method: result.config.method || "GET", currency: result.config.currency || "USD", quotaDivisor: result.config.quotaDivisor || 500000 }, monthlyQuota: result.monthlyQuota, checkin: result.checkin, validationId: "" };
    } catch (cause) { if (active) error = String(cause); }
    finally { if (active) loading = false; }
  }
  /** 指纹仅用于使界面临时结果失效，不向后端发送或记录凭据。 */
  function fingerprint(): string { return JSON.stringify($state.snapshot(draft)); }
  /** 仅验证余额可用性；不保存当前草稿、不查询 Key、不刷新正式列表。 */
  async function validate(): Promise<void> {
    if (saving || loading || !account) return;
    releaseValidation(validation?.id ?? ""); validation = null;
    pending = "validate"; error = "";
    const currentFingerprint = fingerprint();
    try {
      const result = await VelaService.validateProviderAccount(providerID, { ...draft, config: queryConfigDraft(draft.config), validationId: "" });
      if (!active) { releaseValidation(result.id); return; }
      if (fingerprint() !== currentFingerprint) { releaseValidation(result.id); return; }
      validatedFingerprint = currentFingerprint;
      validation = result;
    } catch (cause) { if (active) error = `无法完成验证：${String(cause)}`; }
    finally { if (active) pending = null; }
  }
  /** 独立后台同步复用连接的 Key；失败仅反馈同步问题，不影响已完成的配置保存。 */
  async function syncReusedKeys(): Promise<void> {
    try { await VelaService.refreshReusedProviderKeys(providerID); }
    catch (cause) { notify(`账户配置已保存，Key 信息同步未完成：${String(cause)}`); }
    try { await onupdated(); }
    catch (cause) { notify(`配置已保存，但列表更新失败：${String(cause)}`); }
  }
  /** 只提交配置；匹配成功验证时附加引用，保存成功即关闭，不等待 Key 查询。 */
  async function save(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (saving || loading || !account) return;
    const validationId = feedbackCurrent ? validation?.id ?? "" : "";
    const syncKeys = Boolean(validationId && validation?.balance.status.state === "success");
    pending = "save"; error = "";
    try {
      await VelaService.saveProviderAccount(providerID, { ...draft, config: queryConfigDraft(draft.config), validationId });
      validation = null;
      notify(syncKeys ? "配置和已验证余额已保存，Key 信息正在后台同步" : "账户配置已保存");
      if (syncKeys) void syncReusedKeys();
      else void onupdated().catch((cause) => notify(`配置已保存，但列表更新失败：${String(cause)}`));
      onclose();
    } catch (cause) { if (active) error = `保存失败：${String(cause)}`; }
    finally { if (active) pending = null; }
  }
  /** 取消不保存配置、余额或 Key，仅由卸载清除临时验证。 */
  function close(): void { if (!loading && !saving) onclose(); }
</script>

<form class="account-form" onsubmit={save} aria-busy={loading || saving}>
  {#if loading}<p class="status" role="status">正在读取账户配置…</p>{/if}
  {#if account}
    <Field.Set disabled={loading || saving} class="min-w-0 min-h-0 flex-1 overflow-hidden"><Field.Group class="min-h-0 flex-1">
      <QueryConnection bind:value={draft.config} scrollable />
    </Field.Group></Field.Set>
  {/if}
  <div class="account-action-area">
    {#if feedbackCurrent && validation}
      <p class="status" class:failed={validation.balance.status.state !== "success"} role="status">
        {#if validation.balance.status.state === "success"}
          验证成功：余额 {validation.balance.currency} {validation.balance.amount?.toFixed(2)}。保存后应用余额并同步复用连接的 Key。
        {:else}验证失败：{validation.balance.status.error || "无法查询余额"}。仍可保存配置，等待后续刷新。{/if}
      </p>
    {/if}
    {#if error}<Alert.Root variant="inline-error"><Alert.Description>{error}</Alert.Description></Alert.Root>{/if}
    <footer>
      <Button type="button" variant="outline" disabled={loading || saving} onclick={close}>{feedbackCurrent ? "关闭" : "取消"}</Button>
      {#if !account && !loading}
        <Button type="button" variant="outline" onclick={() => void load()}>重新读取</Button>
      {:else}
        <Button type="button" variant="outline" disabled={loading || saving || !account} onclick={() => void validate()}>{pending === "validate" ? "正在验证…" : "验证"}</Button>
        <Button type="submit" disabled={loading || saving || !account}>{pending === "save" ? "正在保存…" : "保存"}</Button>
      {/if}
    </footer>
  </div>
</form>

<style>
  .account-form { display: flex; flex-direction: column; gap: 18px; min-width: 0; min-height: 0; flex: 1; }

  .status { color: var(--positive); font-size: 12px; line-height: 1.6; }
  .status.failed { color: var(--destructive); }
  .account-action-area { flex-shrink: 0; display: flex; flex-direction: column; gap: 10px; padding-top: 14px; border-top: 1px solid var(--border); background: var(--background); }
  footer { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; }
</style>
