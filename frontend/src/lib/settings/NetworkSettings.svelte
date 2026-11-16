<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { onMount } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import { Switch } from "$lib/components/ui/switch";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import * as Field from "$lib/components/ui/field";
  import * as Select from "$lib/components/ui/select";
  import { Network, Eye, EyeOff } from "@lucide/svelte";
  import * as InputGroup from "$lib/components/ui/input-group";
  import { parseProxyAddress, formatProxyAddress, proxyProtocols } from "./proxy-address";
  import { Input } from "$lib/components/ui/input";
  import { VelaService, type Proxy } from "$lib/generated/mygo";
  import { errorMessage, type Notify } from "./sections";

  /** 全局代理通过拆分字段编辑，保存时复用已有代理 URL 和加密存储。 */
  let {
    desktop,
    notify,
  }: {
    /** 是否运行在 Vela 桌面窗口内。 */
    desktop: boolean;
    /** 保存成功反馈。 */
    notify: Notify;
  } = $props();

  /** 开关和地址先编辑草稿，取消或校验失败不改变已保存配置。 */
  let enabled = $state(false);
  /** 连接及认证草稿，取消后从已保存地址恢复；密码默认隐藏。 */
  let address = $state(parseProxyAddress(""));
  /** 按需展开认证；收起保留草稿，但提交时不携带认证信息。 */
  let authenticationEnabled = $state(false);
  let passwordVisible = $state(false);
  let saved = $state<Proxy>({ enabled: false, url: "" });
  /** 已从桌面服务读到当前配置后才允许编辑与保存。 */
  let loaded = $state(false);
  /** busy 表示正在保存；error 是读取、校验或保存失败的原因。 */
  let busy = $state(false);
  let error = $state("");
  /** 认证关闭时只提交连接字段，避免隐藏的凭据继续生效。 */
  const effectiveAddress = $derived(authenticationEnabled ? address : { ...address, username: "", password: "" });
  /** 关闭代理时地址不参与比较：关闭后沿用已保存的地址。 */
  const changed = $derived(
    enabled !== saved.enabled || (enabled && JSON.stringify(effectiveAddress) !== JSON.stringify(parseProxyAddress(saved.url))),
  );

  /** 读取唯一的全局代理；读取失败时不允许保存，避免覆盖未读到的配置。 */
  async function load(): Promise<void> {
    if (!desktop) return;
    error = "";
    try {
      const value = await VelaService.globalProxy();
      address = parseProxyAddress(value.url);
      authenticationEnabled = Boolean(address.username || address.password);
      saved = value;
      enabled = saved.enabled;
      loaded = true;
    } catch (cause) {
      error = errorMessage(cause);
    }
  }

  /** 校验有效字段后交给 Go 保存；失败保留草稿，关闭代理时沿用已保存地址。 */
  async function save(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (busy || !loaded) return;
    busy = true;
    error = "";
    try {
      const next = { enabled, url: enabled ? formatProxyAddress(effectiveAddress, saved.url) : saved.url };
      await VelaService.saveGlobalProxy(next);
      saved = next;
      address = parseProxyAddress(saved.url);
      authenticationEnabled = Boolean(address.username || address.password);
      passwordVisible = false;
      notify("已保存全局代理");
    } catch (cause) {
      error = errorMessage(cause);
    } finally {
      busy = false;
    }
  }

  /** 丢弃草稿并恢复已保存的全局代理。 */
  function cancel(): void {
    enabled = saved.enabled;
    address = parseProxyAddress(saved.url);
    authenticationEnabled = Boolean(address.username || address.password);
    passwordVisible = false;
    error = "";
  }

  onMount(() => {
    void load();
  });
</script>

<section class="settings-card" aria-labelledby="global-proxy-title">
  <form novalidate onsubmit={save}>
    <div class="flex items-center justify-between gap-6 border-b px-6 py-5">
      <div class="flex items-center gap-3">
        <span class="grid size-10 shrink-0 place-items-center rounded-lg bg-accent text-primary"><Network class="size-5" aria-hidden="true" /></span>
        <div>
          <h2 id="global-proxy-title" class="text-[15px] font-semibold">出站代理</h2>
          <p class="mt-1 text-xs text-muted-foreground">配置 Vela 访问模型上游与在线价格目录时使用的全局代理</p>
        </div>
      </div>
      <Switch aria-label="使用全局代理" bind:checked={enabled} disabled={!desktop || !loaded || busy} />
    </div>
    <div class="px-6 py-5">
      {#if enabled}
        <Field.Group class="gap-6">
          <div class="grid gap-4 grid-cols-[minmax(100px,1fr)_minmax(0,2fr)_minmax(80px,1fr)]">
            <Field.Field>
              <Field.Label for="proxy-protocol">代理类型</Field.Label>
              <Select.Root type="single" bind:value={address.protocol} disabled={!desktop || busy}>
                <Select.Trigger id="proxy-protocol" class="w-full">{proxyProtocols[address.protocol]}</Select.Trigger>
                <Select.Content><Select.Group>
                  {#each Object.entries(proxyProtocols) as [value, label]}<Select.Item {value}>{label}</Select.Item>{/each}
                </Select.Group></Select.Content>
              </Select.Root>
            </Field.Field>
            <Field.Field>
              <Field.Label for="proxy-host">主机</Field.Label>
              <Input id="proxy-host" bind:value={address.host} placeholder="127.0.0.1 或 proxy.example.com" autocomplete="off" autocapitalize="none" spellcheck={false} disabled={!desktop || busy} />
            </Field.Field>
            <Field.Field>
              <Field.Label for="proxy-port">端口</Field.Label>
              <Input id="proxy-port" bind:value={address.port} inputmode="numeric" placeholder="7890" autocomplete="off" disabled={!desktop || busy} />
            </Field.Field>
          </div>
          <Field.Group class="gap-4">
            <Field.Field orientation="horizontal">
              <Checkbox id="proxy-authentication" bind:checked={authenticationEnabled} disabled={!desktop || busy} onCheckedChange={() => (passwordVisible = false)} />
              <Field.Label for="proxy-authentication">需要身份认证</Field.Label>
            </Field.Field>
            {#if authenticationEnabled}
              <Field.Group class="grid gap-4 sm:grid-cols-2">
                <Field.Field>
                  <Field.Label for="proxy-username">用户名</Field.Label>
                  <Input id="proxy-username" bind:value={address.username} placeholder="输入代理用户名" autocomplete="off" autocapitalize="none" spellcheck={false} disabled={!desktop || busy} />
                </Field.Field>
                <Field.Field>
                  <Field.Label for="proxy-password">密码</Field.Label>
                  <InputGroup.Root>
                    <InputGroup.Input id="proxy-password" type={passwordVisible ? "text" : "password"} bind:value={address.password} placeholder="输入代理密码" autocomplete="new-password" disabled={!desktop || busy} />
                    <InputGroup.Addon align="inline-end"><InputGroup.Button size="icon-xs" aria-label={passwordVisible ? "隐藏代理密码" : "显示代理密码"} aria-pressed={passwordVisible} disabled={!desktop || busy} onclick={() => (passwordVisible = !passwordVisible)}>{#if passwordVisible}<EyeOff />{:else}<Eye />{/if}</InputGroup.Button></InputGroup.Addon>
                  </InputGroup.Root>
                </Field.Field>
              </Field.Group>
            {/if}
          </Field.Group>
        </Field.Group>
      {:else}
        <p class="text-sm text-muted-foreground">当前使用直连。开启后可配置代理服务器和认证信息。</p>
      {/if}
      {#if error}<Alert.Root variant="inline-error" class="mt-4"><Alert.Description>{error}</Alert.Description></Alert.Root>{/if}
      {#if desktop && !loaded && error}<Button variant="link" size="xs" class="mt-2 px-0" onclick={() => void load()}>重新读取</Button>{/if}
    </div>
    <div class="flex flex-wrap items-center justify-between gap-4 border-t px-6 py-4">
      <p class="text-xs text-muted-foreground">仅影响跟随全局代理的连接</p>
      <div class="flex gap-2">
        <Button variant="outline" size="sm" disabled={!changed || busy} onclick={cancel}>取消</Button>
        <Button type="submit" size="sm" disabled={!desktop || !loaded || !changed || busy}>{busy ? "正在保存…" : "保存设置"}</Button>
      </div>
    </div>
  </form>
</section>
