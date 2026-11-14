<script lang="ts">
  import { onDestroy } from "svelte";
  import { Check, Terminal } from "@lucide/svelte";
  import * as ToggleGroup from "$lib/components/ui/toggle-group";
  import * as Collapsible from "$lib/components/ui/collapsible";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Textarea } from "$lib/components/ui/textarea";
  import * as Field from "$lib/components/ui/field";
  import { VelaService, type QueryConfig } from "$lib/generated/mygo";
  import Choice from "./Choice.svelte";
  import { defaultQueryConfig } from "./query-defaults";

  /** 查询连接只维护草稿；Key 模式不编辑余额字段或另一把调用密钥。 */
  let { value = $bindable(defaultQueryConfig()), forKey = false }: {
    /** 可双向绑定的连接草稿，秘密掩码仅作占位。 */
    value?: QueryConfig;
    /** 当前连接是否专属于一把上游 Key。 */
    forKey?: boolean;
  } = $props();
  /** 多个编辑器共存时保持标签与控件身份唯一。 */
  const id = $props.id();
  /** 与原 APP 一致，在 Sub2API 站点控制台读取 Refresh Token。 */
  const refreshTokenCommand = "localStorage.getItem('refresh_token')";
  /** 快捷命令复制状态只服务当前连接表单。 */
  let commandCopied = $state(false);
  let commandError = $state("");
  let commandTimer: ReturnType<typeof setTimeout> | undefined;
  /** 已认可的四种连接方式及对应业务说明。 */
  const methods = [
    { value: "sub2api", title: "Sub2API", description: "使用站点 Refresh Token" },
    { value: "newapi", title: "New API", description: "用户 ID 与系统访问令牌" },
    { value: "usage_api", title: "API Key", description: "兼容上游查询接口" },
    { value: "custom", title: "自定义 URL", description: "GET / POST 与 JSON 字段路径" },
  ];
  /** 切换方式清空秘密草稿，避免把其他方式的掩码误当成新认证。 */
  function selectMode(mode: string): void {
    if (value.mode === mode) return;
    clearTimeout(commandTimer);
    commandCopied = false;
    commandError = "";
    value = { ...value, mode, method: value.method || "GET", currency: value.currency || "USD", quotaDivisor: value.quotaDivisor || 500000, refreshToken: "", accessToken: "", apiKey: "", headers: "", body: "" };
  }

  /** 仅把快捷命令写入系统剪贴板，不在界面展开命令或操作教程。 */
  async function copyRefreshTokenCommand(): Promise<void> {
    clearTimeout(commandTimer);
    commandError = "";
    try {
      await VelaService.copyText(refreshTokenCommand);
      commandCopied = true;
      commandTimer = setTimeout(() => { commandCopied = false; }, 1800);
    } catch (cause) {
      commandCopied = false;
      commandError = `复制失败：${String(cause)}`;
    }
  }

  onDestroy(() => clearTimeout(commandTimer));
</script>

<div class="connection-layout">
  <Field.Set class="method-section">
    <Field.Legend>接入方式</Field.Legend>
    <Field.Description>选择站点实际支持的查询方式。</Field.Description>
    <ToggleGroup.Root type="single" value={value.mode} onValueChange={(next) => { if (next) selectMode(next); }} variant="card" size="choice" spacing={2} orientation="vertical" class="methods w-full" aria-label="查询接入方式">
      {#each methods as method}
        <ToggleGroup.Item value={method.value}>
          <span><strong>{method.title}</strong><small>{method.description}</small></span>
        </ToggleGroup.Item>
      {/each}
    </ToggleGroup.Root>
  </Field.Set>
  <div class="connection-fields">
    {#if value.mode}
      <Field.Group>
        {#if value.mode !== "custom"}
          <Field.Field><Field.Label for={`${id}-base`}>站点地址</Field.Label><Input id={`${id}-base`} type="url" bind:value={value.baseUrl} placeholder="https://api.example.com" /><Field.Description>留空使用供应商服务地址。</Field.Description></Field.Field>
          {#if value.mode === "sub2api"}
            <Field.Field>
              <div class="field-label-row">
                <Field.Label for={`${id}-refresh`}>Refresh Token</Field.Label>
                <Button type="button" variant="text" size="inline" onclick={() => void copyRefreshTokenCommand()} aria-label="复制获取 Refresh Token 的命令">{#if commandCopied}<Check />已复制{:else}<Terminal />复制获取命令{/if}</Button>
              </div>
              <Input id={`${id}-refresh`} type="password" autocomplete="new-password" value={value.refreshToken === "***" ? "" : value.refreshToken} oninput={(event) => value.refreshToken = event.currentTarget.value} placeholder={value.refreshToken === "***" ? "***（已保存，留空保留）" : "填写 Refresh Token"} />
              <Field.Description>从 Sub2API 控制台获取；已保存的令牌不会回显，留空不替换。</Field.Description>
              <span class="sr-only" role="status">{commandCopied ? "已复制获取 Refresh Token 的命令" : ""}</span>
              {#if commandError}<p class="command-error" role="alert">{commandError}</p>{/if}
            </Field.Field>
          {:else if value.mode === "newapi"}
            <Field.Field><Field.Label for={`${id}-user`}>用户 ID</Field.Label><Input id={`${id}-user`} bind:value={value.userId} required /></Field.Field>
            <Field.Field><Field.Label for={`${id}-access`}>系统访问令牌</Field.Label><Input id={`${id}-access`} type="password" autocomplete="new-password" value={value.accessToken === "***" ? "" : value.accessToken} oninput={(event) => value.accessToken = event.currentTarget.value} placeholder={value.accessToken === "***" ? "***（已保存，留空保留）" : "填写系统访问令牌"} /><Field.Description>在 New API 个人设置中生成，不是模型调用 Key。</Field.Description></Field.Field>
            <Collapsible.Root><Collapsible.Trigger>{#snippet child({ props })}<Button {...props} variant="text" size="inline">额度换算</Button>{/snippet}</Collapsible.Trigger><Collapsible.Content><Field.Field class="pt-3"><Field.Label for={`${id}-divisor`}>额度换算除数</Field.Label><Input id={`${id}-divisor`} type="number" min={0.000000001} step="any" required bind:value={value.quotaDivisor} placeholder="500000" /><Field.Description>须为正数，默认 500000。</Field.Description></Field.Field></Collapsible.Content></Collapsible.Root>
          {:else if value.mode === "usage_api" && !forKey}
            <Field.Field><Field.Label for={`${id}-api`}>API Key</Field.Label><Input id={`${id}-api`} type="password" autocomplete="new-password" value={value.apiKey === "***" ? "" : value.apiKey} oninput={(event) => value.apiKey = event.currentTarget.value} placeholder={value.apiKey === "***" ? "***（已保存，留空保留）" : "填写上游 API Key"} /><Field.Description>仅适用于兼容 /v1/usage 的余额接口。</Field.Description></Field.Field>
          {:else if value.mode === "usage_api"}
            <Field.Description>查询使用当前 Key 的实际调用密钥，无需重复填写。</Field.Description>
          {/if}
        {:else}
          <Field.Field><Field.Label>请求方法</Field.Label><Choice bind:value={value.method} label="自定义请求方法" options={[{ value: "GET", label: "GET" }, { value: "POST", label: "POST" }]} /></Field.Field>
          <Field.Field><Field.Label for={`${id}-url`}>{forKey ? "Key 信息查询地址" : "余额查询地址"}</Field.Label><Input id={`${id}-url`} type="url" bind:value={value.url} required placeholder="https://api.example.com/api/usage" /></Field.Field>
          <Field.Field><Field.Label for={`${id}-headers`}>请求头 JSON（可选）</Field.Label><Textarea id={`${id}-headers`} rows={3} spellcheck={false} autocomplete="off" value={value.headers === "***" ? "" : value.headers} oninput={(event) => value.headers = event.currentTarget.value} placeholder={value.headers === "***" ? "***（已保存，留空保留）" : '{"Accept":"application/json"}'} /><Field.Description>填写 JSON 字符串对象；已有请求头仅显示占位，留空保留。</Field.Description></Field.Field>
          {#if value.method === "POST"}
            <Field.Field><Field.Label for={`${id}-body`}>POST JSON 请求体（可选）</Field.Label><Textarea id={`${id}-body`} rows={3} spellcheck={false} autocomplete="off" value={value.body === "***" ? "" : value.body} oninput={(event) => value.body = event.currentTarget.value} placeholder={value.body === "***" ? "***（已保存，留空保留）" : "{}"} /><Field.Description>填写合法 JSON；GET 不发送请求体。</Field.Description></Field.Field>
          {/if}
          {#if !forKey}
            <Field.Field><Field.Label for={`${id}-balance`}>余额字段路径</Field.Label><Input id={`${id}-balance`} bind:value={value.balancePath} required placeholder="data.balance" /><Field.Description>仅支持对象成员点路径，不支持表达式或数组遍历。</Field.Description></Field.Field>
            <Field.Field><Field.Label for={`${id}-currency`}>原币种</Field.Label><Input id={`${id}-currency`} bind:value={value.currency} placeholder="USD" /><Field.Description>按上游原币种显示，不自动换汇。</Field.Description></Field.Field>
          {/if}
        {/if}
      </Field.Group>
    {:else}
      <p class="empty-method">选择一种接入方式后填写对应连接信息。</p>
    {/if}
  </div>
</div>

<style>
  .connection-layout { display: grid; grid-template-columns: minmax(170px, .8fr) minmax(0, 1.4fr); gap: 20px; min-width: 0; }
  .connection-layout :global(.method-section), .connection-fields { min-width: 0; }
  :global(.methods) { display: flex; flex-direction: column; gap: 8px; }

  :global(.methods) strong, :global(.methods) small { display: block; }
  :global(.methods) small { margin-top: 4px; color: var(--muted-foreground); font-size: 12px; font-weight: 400; }
  .connection-fields { padding: 16px; border: 1px solid var(--border); border-radius: 10px; }
  .empty-method { color: var(--muted-foreground); font-size: 13px; line-height: 1.7; }
  .field-label-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
  .field-label-row :global(button) { color: var(--primary); }
  .command-error { color: var(--destructive); font-size: 12px; line-height: 1.6; }

  @media (max-width: 640px) { .connection-layout { grid-template-columns: minmax(0, 1fr); } :global(.methods) { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); } }
</style>
