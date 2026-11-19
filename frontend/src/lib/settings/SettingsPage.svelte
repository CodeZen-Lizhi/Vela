<script lang="ts">
  import PageHeader from "$lib/components/PageHeader.svelte";
  import * as Alert from "$lib/components/ui/alert";
  import { onMount } from "svelte";
  import { isMyGo } from "mygo-runtime";
  import { Button } from "$lib/components/ui/button";
  import { VelaService, type SettingsSnapshot } from "$lib/generated/mygo";
  import { errorMessage, type Notify, type SettingsSection } from "./sections";
  import ApplicationUpdate from "./ApplicationUpdate.svelte";
  import PreferencesSettings from "./PreferencesSettings.svelte";
  import ConfigTransfer from "./ConfigTransfer.svelte";
  import ConnectionSettings from "./ConnectionSettings.svelte";
  import AccountSettings from "./AccountSettings.svelte";
  import NetworkSettings from "./NetworkSettings.svelte";
  import AttentionSettings from "./AttentionSettings.svelte";
  import RecordsSettings from "./RecordsSettings.svelte";

  /** 设置页一次读取已保存设置；各分类保持挂载，切换分类不丢草稿。 */
  let {
    section,
    notify,
  }: {
    /** 当前显示的分类，由应用侧栏持有。 */
    section: SettingsSection;
    /** 保存、复制等操作的成功反馈。 */
    notify: Notify;
  } = $props();

  /** 浏览器预览没有桌面服务，只展示默认值且不可保存；外观跟随浏览器当前的明暗偏好。 */
  const preview: SettingsSnapshot = {
    general: {
      appearance: "system",
      closeAction: "background",
      backgroundEntry: "menu",
      dashboardSeconds: 10,
      providerSeconds: 60,
      modelAutoUpdate: true,
      modelUpdateMinutes: 10,
    },
    openAtLogin: false,
    gateway: { port: 18080 },
    account: { enabled: false, minutes: 5, keyEnabled:false, keyMinutes:30, onVisible:false },
    attention: {
      balance: true,
      difference: true,
      health: true,
      rate: true,
      balanceThreshold: 10,
    },
  };
  /** 是否运行在 Vela 桌面窗口内。 */
  const desktop = isMyGo();
  /** 已保存的设置；null 表示尚未读到。loadError 是读取失败的原因。 */
  let snapshot = $state<SettingsSnapshot | null>(desktop ? null : preview);
  let loadError = $state("");

  /** 读取失败时不显示默认值冒充已保存设置，提供重试。 */
  async function load(): Promise<void> {
    if (!desktop) return;
    loadError = "";
    try {
      snapshot = await VelaService.settings();
    } catch (error) {
      loadError = errorMessage(error);
    }
  }
  onMount(() => {
    void load();
  });
</script>

<div class="settings-page workspace-page">
  <PageHeader title="设置" description="管理应用偏好、接入配置与本机数据。" />
  <div class="workspace-content">
  {#if !desktop}<Alert.Root variant="info"><Alert.Description>
      浏览器预览 · 设置的读取与保存请在 Vela 桌面窗口中进行。
    </Alert.Description></Alert.Root>{/if}
  {#if loadError}
    <Alert.Root variant="destructive"><Alert.Description>
      设置读取失败：{loadError}
      <Button variant="ghost" size="sm" onclick={() => void load()}>重试</Button>
    </Alert.Description></Alert.Root>
  {:else if !snapshot}
    <p class="p-16 text-center text-sm text-muted-foreground">正在读取设置…</p>
  {:else}
    <PreferencesSettings
      bind:saved={snapshot.general}
      bind:openAtLogin={snapshot.openAtLogin}
      {desktop}
      {section}
    />
    <div hidden={section !== "general"}>
      <ApplicationUpdate />
    </div>
    <div hidden={section !== "connection"} class="grid gap-5">
      <ConnectionSettings
        bind:saved={snapshot.gateway}
        active={section === "connection"}
        {desktop}
        {notify}
      />
      <NetworkSettings {desktop} {notify} />
    </div>
    <div hidden={section !== "refresh"} class="mt-5">
      <AccountSettings bind:saved={snapshot.account} {desktop} {notify} />
    </div>
    <div hidden={section !== "attention"}>
      <AttentionSettings bind:saved={snapshot.attention} {desktop} {notify} />
    </div>
    <div hidden={section !== "records"} class="grid gap-5">
      <ConfigTransfer />
      <RecordsSettings />
    </div>
  {/if}
  </div>
</div>
