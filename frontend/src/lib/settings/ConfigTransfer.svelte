<script lang="ts">
  import { ArrowDownToLine, ArrowUpFromLine, FileJson } from "@lucide/svelte";
  import { isMyGo } from "mygo-runtime";
  import * as Alert from "$lib/components/ui/alert";
  import { Button } from "$lib/components/ui/button";
  import * as Dialog from "$lib/components/ui/dialog";
  import * as Field from "$lib/components/ui/field";
  import { Input } from "$lib/components/ui/input";
  import { Separator } from "$lib/components/ui/separator";
  import * as ToggleGroup from "$lib/components/ui/toggle-group";
  import { VelaService, type ConfigurationPreview } from "$lib/generated/mygo";
  import { errorMessage } from "./sections";

  /** 原生文件对话框与配置服务仅在桌面运行时可用。 */
  const desktop = isMyGo();
  /** 当前迁移流程；关闭时清空密码、文件与预览，不持久化表单草稿。 */
  let action = $state<"export" | "import" | null>(null);
  /** 文件读取、校验、导出或导入期间阻止重复操作与弹窗关闭。 */
  let busy = $state(false);
  /** 密码按原样提交，空格也是密码的一部分；确认密码仅用于导出。 */
  let password = $state("");
  let confirmation = $state("");
  /** 导出提交后显示字段校验，避免用户尚未输入时提前报错。 */
  let attempted = $state(false);
  /** 选中文件名用于展示，包内容仅留在内存并交给后端解密校验。 */
  let filename = $state("");
  let content = $state("");
  /** 只有当前文件和密码通过校验后才允许导入；修改任一项即清除。 */
  let preview = $state<ConfigurationPreview | null>(null);
  /** 默认保留已有配置；覆盖模式也不删除本机独有项。 */
  let mode = $state<"preserve" | "overwrite">("preserve");
  /** 操作失败留在弹窗供重试，完成反馈不携带密码或配置包。 */
  let error = $state("");
  let feedback = $state("");
  /** 按字符计数且不 trim，支持包含空格的导出密码。 */
  const passwordError = $derived(attempted && (Array.from(password).length < 8 || !password.trim()) ? "导出密码至少需要 8 个字符，不能全为空格。" : "");
  const confirmationError = $derived(attempted && password !== confirmation ? "两次密码不一致，请重新确认。" : "");

  /** 清空敏感草稿与校验结果；取消和成功都不保留迁移包。 */
  function clearDraft(): void {
    password = "";
    confirmation = "";
    attempted = false;
    filename = "";
    content = "";
    preview = null;
    mode = "preserve";
    error = "";
  }

  /** 仅在无进行中操作时关闭；预览和取消均不调用写入接口。 */
  function close(): void {
    if (busy) return;
    action = null;
    clearDraft();
  }

  /** 打开全新的导出密码表单，不复用上次输入。 */
  function openExport(): void {
    if (!desktop || busy) return;
    clearDraft();
    feedback = "";
    action = "export";
  }

  /** 重选即废弃旧预览与包，文件对话框取消也不能误导入旧文件。 */
  async function chooseFile(): Promise<void> {
    if (!desktop || busy) return;
    clearDraft();
    busy = true;
    try {
      const file = await VelaService.readConfigurationFile();
      if (!file.content) return;
      filename = file.name;
      content = file.content;
    } catch (cause) {
      error = errorMessage(cause);
    } finally {
      busy = false;
    }
  }

  /** 先选择原生配置文件，再填写原导出密码。 */
  async function openImport(): Promise<void> {
    if (!desktop || busy) return;
    feedback = "";
    action = "import";
    await chooseFile();
  }

  /** 密码发生变化时立即废弃校验结果，不能沿用旧密码的预览。 */
  function changePassword(value: string): void {
    password = value;
    preview = null;
    error = "";
  }

  /** 两次密码校验后调用桌面加密导出；保存对话框取消不报告成功。 */
  async function exportFile(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (!desktop || busy) return;
    attempted = true;
    if (Array.from(password).length < 8 || !password.trim() || password !== confirmation) return;
    busy = true;
    error = "";
    try {
      const saved = await VelaService.exportConfigurationFile(password);
      if (!saved) return;
      feedback = "已导出加密配置。导入时需要使用本次设置的密码。";
      action = null;
      clearDraft();
    } catch (cause) {
      error = errorMessage(cause);
    } finally {
      busy = false;
    }
  }

  /** 仅解密校验并读取数量，不写入配置；失败不保留旧预览。 */
  async function checkFile(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (!desktop || busy || !content || !password) return;
    preview = null;
    busy = true;
    error = "";
    try {
      preview = await VelaService.previewConfiguration(content, password);
    } catch (cause) {
      error = errorMessage(cause);
    } finally {
      busy = false;
    }
  }

  /** 只导入已校验的包，成功后释放密码与包内容；失败保留输入供重试。 */
  async function confirmImport(): Promise<void> {
    if (!desktop || busy || !preview) return;
    busy = true;
    error = "";
    try {
      const result = await VelaService.importConfiguration(content, password, mode);
      feedback = `配置导入完成：${result.models} 个模型、${result.providers} 个供应商、${result.keys} 个 Key、${result.mappings} 条映射。`;
      action = null;
      clearDraft();
    } catch (cause) {
      error = errorMessage(cause);
    } finally {
      busy = false;
    }
  }
</script>

<!-- 配置迁移卡片延续设置页原型的标题、操作与底部范围说明。 -->
<section class="settings-card p-6" aria-labelledby="transfer-title" aria-busy={busy}>
  <div class="flex items-center gap-3">
    <span class="grid size-9.5 shrink-0 place-items-center rounded-[10px] bg-accent text-accent-foreground">
      <FileJson class="size-5" aria-hidden="true" />
    </span>
    <div>
      <h2 id="transfer-title" class="text-[15px] font-semibold">导入与导出</h2>
      <p class="mt-1 text-xs text-muted-foreground">公司和个人电脑，复用同一套网关配置。</p>
    </div>
  </div>
  <p class="my-5 text-xs leading-relaxed text-muted-foreground">
    完整搬运模型、供应商及其配置、上游 Key、余额查询与 Key 查询认证、模型映射、权限和调度。
  </p>
  <div class="flex flex-wrap gap-2.5">
    <Button variant="outline" size="sm" disabled={!desktop || busy || action !== null} onclick={openExport}>
      <ArrowUpFromLine aria-hidden="true" />导出配置
    </Button>
    <Button variant="outline" size="sm" disabled={!desktop || busy || action !== null} onclick={openImport}>
      <ArrowDownToLine aria-hidden="true" />导入配置
    </Button>
  </div>
  {#if !desktop}<p class="mt-3 text-xs text-muted-foreground">请在 Vela 桌面应用中导入或导出配置。</p>{/if}
  {#if feedback}<p class="mt-3 text-xs" role="status">{feedback}</p>{/if}
  <Separator class="mt-5" />
  <p class="pt-4 text-[11px] leading-relaxed text-muted-foreground">
    配置文件包含认证信息，以密码加密的 JSON 保存，请妥善保管文件与密码。导入不删除本机独有项，不影响使用记录、总览统计、报告、本机偏好及网关访问密钥。
  </p>
</section>

<Dialog.Root open={action !== null} onOpenChange={(open) => { if (!open) close(); }}>
  <Dialog.Content
    showCloseButton={!busy}
    interactOutsideBehavior={busy ? "ignore" : "close"}
    escapeKeydownBehavior={busy ? "ignore" : "close"}
    aria-busy={busy}
  >
    <Dialog.Header>
      <Dialog.Title>{action === "export" ? "导出配置" : "导入配置"}</Dialog.Title>
      <Dialog.Description>
        {action === "export" ? "文件包含供应商和 Key 认证信息，将使用密码加密保存为 JSON。" : "选择加密配置文件，输入原导出密码，校验并确认后才会写入。"}
      </Dialog.Description>
    </Dialog.Header>
    {#if action === "export"}
      <form class="flex flex-col gap-5" novalidate onsubmit={exportFile}>
        <Field.Group>
          <Field.Field data-invalid={!!passwordError}>
            <Field.Label for="configuration-export-password">导出密码</Field.Label>
            <Input id="configuration-export-password" type="password" bind:value={password} autocomplete="new-password" autocapitalize="none" spellcheck={false} disabled={busy} aria-invalid={!!passwordError} aria-describedby="configuration-export-help configuration-password-error" />
            <Field.Description id="configuration-export-help">至少 8 个字符，可包含空格；导入时需要使用相同密码。</Field.Description>
            <Field.Error id="configuration-password-error">{passwordError}</Field.Error>
          </Field.Field>
          <Field.Field data-invalid={!!confirmationError}>
            <Field.Label for="configuration-confirm-password">确认密码</Field.Label>
            <Input id="configuration-confirm-password" type="password" bind:value={confirmation} autocomplete="new-password" autocapitalize="none" spellcheck={false} disabled={busy} aria-invalid={!!confirmationError} aria-describedby="configuration-confirm-error" />
            <Field.Error id="configuration-confirm-error">{confirmationError}</Field.Error>
          </Field.Field>
        </Field.Group>
        {#if error}<Alert.Root variant="destructive"><Alert.Description>{error}</Alert.Description></Alert.Root>{/if}
        <Dialog.Footer>
          <Button type="button" variant="outline" disabled={busy} onclick={close}>取消</Button>
          <Button type="submit" disabled={busy}>{busy ? "正在导出…" : "加密并导出"}</Button>
        </Dialog.Footer>
      </form>
    {:else if action === "import"}
      <form class="flex min-w-0 flex-col gap-5" onsubmit={checkFile}>
        <Field.Group>
          <Field.Field>
            <Field.Label id="configuration-file-label">配置文件</Field.Label>
            <div class="flex flex-wrap items-center gap-3">
              <Button type="button" variant="outline" size="sm" disabled={busy} onclick={chooseFile}>{filename ? "重新选择文件" : "选择配置文件"}</Button>
              <span class="min-w-0 break-all text-xs text-muted-foreground" aria-labelledby="configuration-file-label">{filename || "尚未选择加密 JSON 文件"}</span>
            </div>
          </Field.Field>
          {#if content}
            <Field.Field>
              <Field.Label for="configuration-import-password">原导出密码</Field.Label>
              <Input id="configuration-import-password" type="password" bind:value={() => password, changePassword} autocomplete="current-password" autocapitalize="none" spellcheck={false} disabled={busy} required aria-describedby="configuration-import-help" />
              <Field.Description id="configuration-import-help">空格也是密码的一部分。修改密码或重选文件后需要重新校验。</Field.Description>
            </Field.Field>
          {/if}
        </Field.Group>
        {#if preview}
          <div role="status" class="grid grid-cols-2 gap-3 text-sm">
            <p><strong>{preview.models}</strong> 个模型</p>
            <p><strong>{preview.providers}</strong> 个供应商</p>
            <p><strong>{preview.keys}</strong> 个 Key</p>
            <p><strong>{preview.mappings}</strong> 条映射</p>
          </div>
          <Field.Group>
            <Field.Field>
              <Field.Label id="configuration-mode-label">相同配置的处理方式</Field.Label>
              <ToggleGroup.Root type="single" value={mode} variant="segmented" size="sm" spacing={1} disabled={busy} aria-labelledby="configuration-mode-label" onValueChange={(value) => { if (value === "preserve" || value === "overwrite") mode = value; }}>
                <ToggleGroup.Item value="preserve">保留原有，仅新增</ToggleGroup.Item>
                <ToggleGroup.Item value="overwrite">覆盖相同配置</ToggleGroup.Item>
              </ToggleGroup.Root>
              <Field.Description>{mode === "preserve" ? "相同配置保留本机值，只新增本机没有的配置。" : "相同配置使用文件中的值覆盖，同时新增本机没有的配置。"}</Field.Description>
            </Field.Field>
          </Field.Group>
          <p class="text-xs leading-relaxed text-muted-foreground">模型按模型 ID、供应商按稳定 ID 或名称、Key 按身份或密钥匹配。两种模式均不删除本机独有项，不影响使用记录、总览统计、报告、本机偏好及网关访问密钥。</p>
        {/if}
        {#if error}<Alert.Root variant="destructive"><Alert.Description>{error}</Alert.Description></Alert.Root>{/if}
        <Dialog.Footer>
          <Button type="button" variant="outline" disabled={busy} onclick={close}>取消</Button>
          {#if preview}
            <Button type="button" disabled={busy} onclick={confirmImport}>{busy ? "正在导入…" : "确认导入"}</Button>
          {:else}
            <Button type="submit" disabled={busy || !content || !password}>{busy ? "正在读取或校验…" : "校验并预览"}</Button>
          {/if}
        </Dialog.Footer>
      </form>
    {/if}
  </Dialog.Content>
</Dialog.Root>
