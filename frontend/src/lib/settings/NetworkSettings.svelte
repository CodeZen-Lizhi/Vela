<script lang="ts">
  import { onMount } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Input } from "$lib/components/ui/input";
  import { VelaService, type Proxy } from "$lib/generated/mygo";
  import { errorMessage, type Notify } from "./sections";

  /** 全局代理：只有开关与地址，在线模型目录与后续模型调用共同跟随。 */
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
  let proxy = $state<Proxy>({ enabled: false, url: "" });
  let saved = $state<Proxy>({ enabled: false, url: "" });
  /** 已从桌面服务读到当前配置后才允许编辑与保存。 */
  let loaded = $state(false);
  /** busy 表示正在保存；error 是读取、校验或保存失败的原因。 */
  let busy = $state(false);
  let error = $state("");
  /** 关闭代理时地址不参与比较：关闭后沿用已保存的地址。 */
  const changed = $derived(
    proxy.enabled !== saved.enabled || (proxy.enabled && proxy.url !== saved.url),
  );

  /** 读取唯一的全局代理；读取失败时不允许保存，避免覆盖未读到的配置。 */
  async function load(): Promise<void> {
    if (!desktop) return;
    error = "";
    try {
      saved = await VelaService.globalProxy();
      proxy = { ...saved };
      loaded = true;
    } catch (cause) {
      error = errorMessage(cause);
    }
  }

  /** 地址由 Go 校验，失败时保留草稿；关闭时沿用已保存的地址。 */
  async function save(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const next = proxy.enabled ? { ...proxy } : { enabled: false, url: saved.url };
    busy = true;
    error = "";
    try {
      await VelaService.saveGlobalProxy(next);
      saved = next;
      proxy = { ...next };
      notify("已保存全局代理");
    } catch (cause) {
      error = errorMessage(cause);
    } finally {
      busy = false;
    }
  }

  /** 丢弃草稿并恢复已保存的全局代理。 */
  function cancel(): void {
    proxy = { ...saved };
    error = "";
  }

  onMount(() => {
    void load();
  });
</script>

<section class="settings-card" aria-labelledby="global-proxy-title">
  <div class="settings-heading">
    <div>
      <h2 id="global-proxy-title">全局代理</h2>
      <p>模型调用与在线价格目录共同跟随。</p>
    </div>
  </div>
  <form class="px-6 pb-6" novalidate onsubmit={save}>
    <label class="flex items-center gap-2 py-1.5 text-xs"
      ><Checkbox
        bind:checked={proxy.enabled}
        disabled={!desktop || !loaded || busy}
      />使用代理</label
    >
    {#if proxy.enabled}
      <label class="mt-3 grid gap-1.5 text-xs"
        >代理地址<Input
          aria-label="全局代理地址"
          bind:value={proxy.url}
          required
          placeholder="例如：http://127.0.0.1:7890"
          autocomplete="off"
          autocapitalize="none"
          spellcheck={false}
          disabled={!desktop || busy}
        /></label
      >
      <p class="settings-helper">
        支持 HTTP、HTTPS 和 SOCKS5。供应商或 Key 的单独配置仍按各自设置执行。
      </p>
    {:else}
      <p class="settings-helper">关闭时，模型调用与在线价格目录直接连接。</p>
    {/if}
    {#if error}<p class="text-xs text-destructive" role="alert">{error}</p>{/if}
    {#if desktop && !loaded && error}<Button
        variant="link"
        size="xs"
        class="px-0"
        onclick={() => void load()}>重新读取</Button
      >{/if}
    <div class="mt-6 flex justify-end gap-2 border-t pt-5">
      {#if changed}<Button variant="outline" size="sm" disabled={busy} onclick={cancel}
          >取消</Button
        >{/if}
      <Button type="submit" size="sm" disabled={!desktop || !loaded || !changed || busy}
        >保存设置</Button
      >
    </div>
  </form>
</section>
