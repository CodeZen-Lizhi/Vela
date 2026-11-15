<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import * as InputGroup from "$lib/components/ui/input-group";
  import * as Select from "$lib/components/ui/select";
  import { Switch } from "$lib/components/ui/switch";
  import * as ToggleGroup from "$lib/components/ui/toggle-group";
  import { VelaService, type GeneralSettings } from "$lib/generated/mygo";
  import { errorMessage, type SettingsSection } from "./sections";

  /** 外观选项的稳定取值与界面名称，共享分段控件只提交有效选项。 */
  const appearances = [{value:"light",label:"浅色"},{value:"dark",label:"深色"},{value:"system",label:"跟随系统"}];
  /** 常规与刷新偏好共享一份保存状态，切换分类不卸载表单；修改成功后才更新已保存值。 */
  let {
    saved = $bindable(),
    openAtLogin = $bindable(),
    desktop,
    section,
  }: {
    /** 当前分类，仅控制面板可见性。 */
    section: SettingsSection;
    /** 已保存的应用及刷新偏好。 */
    saved: GeneralSettings;
    /** 系统登录项的实际状态。 */
    openAtLogin: boolean;
    /** 是否运行在 Vela 桌面窗口内；浏览器预览不可修改。 */
    desktop: boolean;
  } = $props();

  /** busy 表示有一次保存正在进行，期间忽略新的修改；error 是最近一次失败的原因。 */
  let busy = $state(false);
  let error = $state("");

  /** 关闭窗口后的行为及其显示名称，键与 Go 侧取值一致。 */
  const closeActions: Record<string, string> = {
    background: "继续运行网关",
    quit: "退出应用",
  };
  /** 后台运行时保留的系统入口及其显示名称。 */
  const backgroundEntries: Record<string, string> = {
    menu: "仅菜单栏图标",
    dock: "仅 Dock 图标",
    both: "菜单栏与 Dock 图标",
  };
  /** 总览与供应商列表可选的刷新间隔，单位秒。 */
  const dashboardIntervals = [10, 30, 60];
  const providerIntervals = [60, 120];

  /** 保存一项修改；成功后才更新界面上的已保存值。 */
  async function update(change: Partial<GeneralSettings>): Promise<void> {
    const next = { ...saved, ...change };
    if (busy || JSON.stringify(next) === JSON.stringify(saved)) return;
    busy = true;
    error = "";
    try {
      await VelaService.saveGeneralSettings(next);
      saved = next;
    } catch (cause) {
      error = errorMessage(cause);
    } finally {
      busy = false;
    }
  }

  /** 分钟输入完成后立即保存整数；非法值不写入，失败恢复已保存的间隔。 */
  async function updateModelMinutes(event: Event): Promise<void> {
    const input = event.currentTarget as HTMLInputElement;
    const minutes = input.valueAsNumber;
    if (!input.validity.valid || !Number.isSafeInteger(minutes) || minutes < 1) {
      error = "模型更新间隔请输入至少 1 分钟的整数。";
      input.reportValidity();
      return;
    }
    await update({ modelUpdateMinutes: minutes });
    input.value = String(saved.modelUpdateMinutes);
  }

  /** 向系统登记或取消开机启动，界面以系统返回的实际状态为准。 */
  async function toggleLogin(enabled: boolean): Promise<void> {
    if (busy) return;
    busy = true;
    error = "";
    try {
      openAtLogin = await VelaService.setOpenAtLogin(enabled);
    } catch (cause) {
      error = errorMessage(cause);
    } finally {
      busy = false;
    }
  }
</script>

<div hidden={section !== "general"}>
<section class="settings-card" aria-label="常规设置">
  <div class="setting-row">
    <span id="appearance-label">外观</span>
    <ToggleGroup.Root type="single" value={saved.appearance} variant="segmented" size="sm" spacing={1} disabled={!desktop || busy} aria-labelledby="appearance-label"
      onValueChange={(next) => { if(next) void update({ appearance: next }); }}>
      {#each appearances as item}
        <ToggleGroup.Item value={item.value}>{item.label}</ToggleGroup.Item>
      {/each}
    </ToggleGroup.Root>
  </div>
  <div class="setting-row">
    <span>语言</span>
    <span class="text-xs text-muted-foreground">简体中文</span>
  </div>
  <div class="setting-row">
    <span id="login-label">开机启动</span>
    <Switch
      aria-labelledby="login-label"
      bind:checked={() => openAtLogin, (value) => void toggleLogin(value)}
      disabled={!desktop}
    />
  </div>
  <div class="setting-row">
    <span id="close-label">关闭窗口后</span>
    <Select.Root
      type="single"
      bind:value={() => saved.closeAction, (value) => void update({ closeAction: value })}
      disabled={!desktop}
    >
      <Select.Trigger aria-labelledby="close-label" class="min-w-36"
        >{closeActions[saved.closeAction]}</Select.Trigger
      >
      <Select.Content align="end"
        ><Select.Group
          >{#each Object.entries(closeActions) as [value, label]}<Select.Item
              {value}>{label}</Select.Item
            >{/each}</Select.Group
        ></Select.Content
      >
    </Select.Root>
  </div>
  {#if saved.closeAction === "background"}
    <div class="setting-row">
      <span id="entry-label">后台运行时显示</span>
      <Select.Root
        type="single"
        bind:value={() => saved.backgroundEntry, (value) => void update({ backgroundEntry: value })}
        disabled={!desktop}
      >
        <Select.Trigger aria-labelledby="entry-label" class="min-w-44"
          >{backgroundEntries[saved.backgroundEntry]}</Select.Trigger
        >
        <Select.Content align="end"
          ><Select.Group
            >{#each Object.entries(backgroundEntries) as [value, label]}<Select.Item
                {value}>{label}</Select.Item
              >{/each}</Select.Group
          ></Select.Content
        >
      </Select.Root>
    </div>
  {/if}
</section>
</div>

<div hidden={section !== "refresh"} class="grid gap-5">
<section class="settings-card" aria-labelledby="page-refresh-title">
  <div class="settings-heading"><div><h2 id="page-refresh-title">页面刷新</h2><p>更新页面中的本地已有数据，不向上游发起查询。</p></div></div>
  <div class="setting-row">
    <span id="dashboard-label">总览刷新间隔</span>
    <Select.Root
      type="single"
      bind:value={() => String(saved.dashboardSeconds), (value) => void update({ dashboardSeconds: Number(value) })}
      disabled={!desktop}
    >
      <Select.Trigger aria-labelledby="dashboard-label" class="min-w-24"
        >{saved.dashboardSeconds} 秒</Select.Trigger
      >
      <Select.Content align="end"
        ><Select.Group
          >{#each dashboardIntervals as seconds}<Select.Item
              value={String(seconds)}>{seconds} 秒</Select.Item
            >{/each}</Select.Group
        ></Select.Content
      >
    </Select.Root>
  </div>
  <div class="setting-row">
    <span id="provider-label"><strong>供应商列表状态更新间隔</strong><small class="block text-xs text-muted-foreground">更新本地健康度、Key 和已保存余额；不向上游查询。上游查询周期在下方“上游信息刷新”中设置。</small></span>
    <Select.Root
      type="single"
      bind:value={() => String(saved.providerSeconds), (value) => void update({ providerSeconds: Number(value) })}
      disabled={!desktop}
    >
      <Select.Trigger aria-labelledby="provider-label" class="min-w-24"
        >{saved.providerSeconds} 秒</Select.Trigger
      >
      <Select.Content align="end"
        ><Select.Group
          >{#each providerIntervals as seconds}<Select.Item
              value={String(seconds)}>{seconds} 秒</Select.Item
            >{/each}</Select.Group
        ></Select.Content
      >
    </Select.Root>
  </div>
</section>
<section class="settings-card" aria-labelledby="model-sync-title">
  <div class="settings-heading"><div><h2 id="model-sync-title">模型目录同步</h2><p>同步在线模型目录与价格。</p></div></div>
  <div class="setting-row">
    <span id="model-update-label"><strong>模型目录与价格自动更新</strong><small class="block text-xs text-muted-foreground">定期检查 basellm 来源，保留本地修改；关闭后仍可手动同步价格。</small></span>
    <Switch
      aria-labelledby="model-update-label"
      checked={saved.modelAutoUpdate}
      onCheckedChange={(enabled) => void update({ modelAutoUpdate: enabled })}
      disabled={!desktop || busy}
    />
  </div>
  <div class="setting-row">
    <label id="model-minutes-label" for="model-update-minutes">模型更新间隔</label>
    <InputGroup.Root class="w-32">
      <InputGroup.Input
        id="model-update-minutes"
        type="number"
        min="1"
        step="1"
        required
        value={saved.modelUpdateMinutes}
        disabled={!desktop || busy || !saved.modelAutoUpdate}
        aria-labelledby="model-minutes-label"
        onchange={(event) => void updateModelMinutes(event)}
      />
      <InputGroup.Addon align="inline-end"><InputGroup.Text>分钟</InputGroup.Text></InputGroup.Addon>
    </InputGroup.Root>
  </div>
</section>
</div>
{#if error && (section === "general" || section === "refresh")}<Alert.Root variant="destructive" class="mt-4"><Alert.Description>{error}</Alert.Description></Alert.Root>{/if}
