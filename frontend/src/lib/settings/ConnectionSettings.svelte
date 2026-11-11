<script lang="ts">
  import { untrack } from "svelte";
  import {
    ArrowDownToLine,
    ArrowUpFromLine,
    Check,
    Copy,
    Eye,
    EyeOff,
    FileJson,
    Pencil,
  } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import * as Dialog from "$lib/components/ui/dialog";
  import * as InputGroup from "$lib/components/ui/input-group";
  import { VelaService, type GatewaySettings } from "$lib/generated/mygo";
  import { errorMessage, type Notify } from "./sections";

  /** 接入与访问：固定监听地址与端口、可复制的接入地址、单个访问密钥，以及配置搬运入口。 */
  let {
    saved = $bindable(),
    active,
    desktop,
    notify,
  }: {
    /** 已保存的监听设置。 */
    saved: GatewaySettings;
    /** 当前是否显示本分类；首次显示时才读取本地加密凭据。 */
    active: boolean;
    /** 是否运行在 Vela 桌面窗口内。 */
    desktop: boolean;
    /** 保存与复制的成功反馈。 */
    notify: Notify;
  } = $props();

  /** 端口先编辑草稿，保存成功后才更新可复制的接入地址。 */
  let port = $state<number | null>(untrack(() => saved.port));
  let portBusy = $state(false);
  let portError = $state("");
  const portChanged = $derived(port !== saved.port);
  const endpoint = $derived(`http://127.0.0.1:${saved.port}`);

  /** 密钥只在用户主动查看时读入页面；null 表示尚未得知是否已设置。 */
  let keySet = $state<boolean | null>(null);
  let revealed = $state("");
  let keyBusy = $state(false);
  let keyError = $state("");
  /** 最近一次复制成功的对象，用于按钮上的轻量反馈。 */
  let copied = $state("");

  /** 密钥编辑弹窗：是否打开、输入草稿及其可见性、是否正在保存、最近一次保存失败的原因。 */
  let editing = $state(false);
  let draft = $state("");
  let draftVisible = $state(false);
  let saving = $state(false);
  let draftError = $state("");

  /** 校验并保存端口；取消、校验或保存失败都保留原端口与地址。 */
  async function savePort(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    portError = "";
    if (typeof port !== "number" || !Number.isInteger(port) || port < 1024 || port > 65535) {
      portError = "请输入 1024–65535 之间的整数端口。";
      return;
    }
    portBusy = true;
    try {
      await VelaService.saveGatewaySettings({ port });
      saved = { port };
      notify("已保存监听端口，接入地址已更新");
    } catch (error) {
      portError = errorMessage(error);
    } finally {
      portBusy = false;
    }
  }

  /** 丢弃端口草稿。 */
  function cancelPort(): void {
    port = saved.port;
    portError = "";
  }

  /** 读取密钥是否已设置；读取失败时禁用修改，避免覆盖无法读取的记录。 */
  async function loadKey(): Promise<void> {
    if (!desktop) {
      keySet = false;
      return;
    }
    keyBusy = true;
    keyError = "";
    try {
      keySet = await VelaService.hasAccessKey();
    } catch (error) {
      keySet = null;
      keyError = errorMessage(error);
    } finally {
      keyBusy = false;
    }
  }

  /** 显示时才从本地加密存储读取密钥，隐藏后立即从页面清除。 */
  async function toggleReveal(): Promise<void> {
    if (revealed) {
      revealed = "";
      return;
    }
    keyError = "";
    try {
      revealed = await VelaService.revealAccessKey();
    } catch (error) {
      keyError = errorMessage(error);
    }
  }

  /** 复制失败给出原因，不声称已复制。 */
  async function copy(target: "address" | "key"): Promise<void> {
    copied = "";
    try {
      if (target === "address") await VelaService.copyText(endpoint);
      else await VelaService.copyAccessKey();
      copied = target;
      notify(target === "address" ? "已复制网关地址" : "已复制访问密钥");
    } catch (error) {
      notify(`复制失败：${errorMessage(error)}`);
    }
  }

  /** 清除输入草稿与错误；弹窗以任何方式关闭后都不保留输入过的密钥。 */
  function clearDraft(): void {
    draft = "";
    draftVisible = false;
    draftError = "";
  }

  /** 打开密钥编辑弹窗，草稿从空白开始并默认隐藏。 */
  function openEditor(): void {
    revealed = "";
    clearDraft();
    editing = true;
  }

  /** 关闭密钥编辑弹窗并丢弃草稿，不写入存储。 */
  function closeEditor(): void {
    editing = false;
    clearDraft();
  }

  /** 保存成功后恢复掩码；失败保留草稿与旧密钥供重试。 */
  async function saveKey(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (saving) return;
    saving = true;
    draftError = "";
    try {
      await VelaService.saveAccessKey(draft);
      keySet = true;
      closeEditor();
      notify("访问密钥已在本机加密保存");
    } catch (error) {
      draftError = errorMessage(error);
    } finally {
      saving = false;
    }
  }

  /** 旧凭据首次迁移可能触发系统授权，只在用户进入本分类时读取；离开本分类时收起已显示的密钥。 */
  let keyRequested = false;
  $effect(() => {
    if (!active) {
      revealed = "";
      return;
    }
    if (keyRequested) return;
    keyRequested = true;
    untrack(() => void loadKey());
  });
</script>

<div class="grid gap-5">
  <section class="settings-card" aria-labelledby="access-title">
    <div class="settings-heading">
      <div>
        <h2 id="access-title">网关接入</h2>
        <p>复制地址与密钥到你的工具。</p>
      </div>
      <span class="tag">仅 127.0.0.1</span>
    </div>
    <div class="px-6">
      <form class="border-t py-5" novalidate onsubmit={savePort}>
        <div class="grid items-end gap-6 sm:grid-cols-[1fr_minmax(140px,240px)]">
          <div class="grid gap-1.5 text-xs">
            <span class="text-muted-foreground">监听地址</span>
            <code class="text-[13px]">127.0.0.1</code>
          </div>
          <label class="grid gap-1.5 text-xs"
            >监听端口<Input
              aria-label="监听端口"
              type="number"
              min="1024"
              max="65535"
              step="1"
              required
              disabled={!desktop || portBusy}
              bind:value={port}
            /></label
          >
        </div>
        {#if portError}<p class="mt-3 text-xs text-destructive" role="alert">
            {portError}
          </p>{/if}
        <div class="mt-3 flex justify-end gap-2">
          {#if portChanged}<Button variant="outline" size="sm" onclick={cancelPort}
              >取消</Button
            >{/if}
          <Button type="submit" size="sm" disabled={!desktop || !portChanged || portBusy}
            >保存设置</Button
          >
        </div>
      </form>
      <div class="access-field">
        <span id="gateway-address-label">本机接入地址</span>
        <div class="access-value" aria-labelledby="gateway-address-label">
          <code>{endpoint}</code>
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label="复制网关地址"
            title={copied === "address" ? "已复制" : "复制网关地址"}
            disabled={!desktop}
            onclick={() => void copy("address")}
            >{#if copied === "address"}<Check />{:else}<Copy />{/if}</Button
          >
        </div>
      </div>
      <div class="access-field">
        <span id="gateway-key-label">访问密钥</span>
        <div>
          <div class="access-value" aria-labelledby="gateway-key-label">
            <code
              >{keyError && keySet === null
                ? "读取失败"
                : keySet === null
                  ? "正在读取…"
                  : !keySet
                    ? "尚未设置"
                    : revealed || "••••••••••••••••"}</code
            >
            {#if keySet}
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label={revealed ? "隐藏访问密钥" : "显示访问密钥"}
                aria-pressed={!!revealed}
                onclick={() => void toggleReveal()}
                >{#if revealed}<EyeOff />{:else}<Eye />{/if}</Button
              >
              <Button
                variant="ghost"
                size="icon-xs"
                aria-label="复制访问密钥"
                title={copied === "key" ? "已复制" : "复制访问密钥"}
                onclick={() => void copy("key")}
                >{#if copied === "key"}<Check />{:else}<Copy />{/if}</Button
              >
            {/if}
            <Button
              variant="outline"
              size="sm"
              class="ml-1"
              disabled={!desktop || keySet === null}
              onclick={openEditor}><Pencil />{keySet ? "修改" : "设置密钥"}</Button
            >
          </div>
          {#if keyError}
            <p class="mt-2 text-xs text-destructive" role="alert">{keyError}</p>
            {#if keySet === null}<Button
                variant="link"
                size="xs"
                class="px-0"
                disabled={keyBusy}
                onclick={() => void loadKey()}>重新读取</Button
              >{/if}
          {/if}
        </div>
      </div>
      <p class="border-t py-3.5 text-[11px] leading-relaxed text-muted-foreground">
        密钥仅在本机加密保存，不上传给开发者，不写入普通配置、日志或默认导出文件。网关尚未接入，端口与密钥在网关上线后用于监听和鉴权。
      </p>
    </div>
  </section>

  <section class="settings-card p-6" aria-labelledby="transfer-title">
    <div class="flex items-center gap-3">
      <span
        class="grid size-9.5 shrink-0 place-items-center rounded-[10px] bg-accent text-accent-foreground"
        ><FileJson class="size-5" aria-hidden="true" /></span
      >
      <div>
        <h2 id="transfer-title" class="text-[15px] font-semibold">导入与导出</h2>
        <p class="mt-1 text-xs text-muted-foreground">
          公司和个人电脑，复用同一套网关配置。
        </p>
      </div>
    </div>
    <p class="my-5 text-xs leading-relaxed text-muted-foreground">
      搬运供应商、上游 Key 配置、模型映射与调度方案。
    </p>
    <div class="flex flex-wrap gap-2.5">
      <Button size="sm" disabled><ArrowUpFromLine />导出配置</Button>
      <Button variant="outline" size="sm" disabled><ArrowDownToLine />导入配置</Button>
    </div>
    <p class="mt-5 border-t pt-4 text-[11px] leading-relaxed text-muted-foreground">
      调用历史、统计、本机网络与窗口偏好、网关访问密钥留在各自电脑。供应商与调度接入后开放导入导出。
    </p>
  </section>
</div>

<Dialog.Root
  bind:open={editing}
  onOpenChange={(open) => {
    if (!open) clearDraft();
  }}
>
  <Dialog.Content
    showCloseButton={!saving}
    interactOutsideBehavior={saving ? "ignore" : "close"}
    escapeKeydownBehavior={saving ? "ignore" : "close"}
  >
    <Dialog.Header
      ><Dialog.Title>{keySet ? "修改访问密钥" : "设置访问密钥"}</Dialog.Title
      ><Dialog.Description
        >保存后使用新密钥连接；其他客户端也需要更新。</Dialog.Description
      ></Dialog.Header
    >
    <form class="grid gap-4" novalidate onsubmit={saveKey}>
      <label class="grid gap-2 text-xs" for="new-gateway-key">新访问密钥</label>
      <InputGroup.Root class="-mt-2">
        <InputGroup.Input
          id="new-gateway-key"
          type={draftVisible ? "text" : "password"}
          bind:value={draft}
          required
          maxlength={256}
          autocomplete="new-password"
          autocapitalize="none"
          spellcheck={false}
          disabled={saving}
          aria-describedby="key-help"
        />
        <InputGroup.Addon align="inline-end"
          ><InputGroup.Button
            size="icon-xs"
            aria-label={draftVisible ? "隐藏新密钥" : "显示新密钥"}
            aria-pressed={draftVisible}
            onclick={() => (draftVisible = !draftVisible)}
            >{#if draftVisible}<EyeOff />{:else}<Eye />{/if}</InputGroup.Button
          ></InputGroup.Addon
        >
      </InputGroup.Root>
      <p id="key-help" class="text-xs text-muted-foreground">
        1–256 个英文字符、数字或符号，不能包含空格。
      </p>
      {#if draftError}<p class="error-banner" role="alert">{draftError}</p>{/if}
      <Dialog.Footer
        ><Button variant="outline" disabled={saving} onclick={closeEditor}
          >取消</Button
        ><Button type="submit" disabled={saving || !draft}
          >{saving ? "正在保存…" : "保存密钥"}</Button
        ></Dialog.Footer
      >
    </form>
  </Dialog.Content>
</Dialog.Root>
