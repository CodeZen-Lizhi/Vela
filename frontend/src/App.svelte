<script lang="ts">
  import {
    Box,
    Settings2,
    LayoutDashboard,
    Waypoints,
    Network,
    ScrollText,
    FlaskConical,
    PanelLeftClose,
    PanelLeftOpen,
  } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import VelaMark from "$lib/components/VelaMark.svelte";
  import VerificationPage from "$lib/verification/VerificationPage.svelte";
  import ModelsPage from "$lib/models/ModelsPage.svelte";
  import ProvidersPage from "$lib/providers/ProvidersPage.svelte";
  import SchedulingPage from "$lib/scheduling/SchedulingPage.svelte";
  import SettingsPage from "$lib/settings/SettingsPage.svelte";
  import { settingsSections, type SettingsSection } from "$lib/settings/sections";
  /** 当前显示的顶级页面，取导航项名称或“设置”。 */
  let page = $state("模型");
  /** 侧栏收起时只显示导航图标；切换页面和展开时保留当前页面及设置分类。 */
  let sidebarCollapsed = $state(false);
  /** 设置子分类由外壳持有，侧栏、面包屑与内容面板共用。 */
  let settingsSection = $state<SettingsSection>("general");
  /** 已实现入口正常导航，其余模块保持明确的待开发状态。 */
  const navigation = [
    { name: "总览", icon: LayoutDashboard, enabled: false },
    { name: "供应商", icon: Network, enabled: true },
    { name: "模型", icon: Box, enabled: true },
    { name: "调度", icon: Waypoints, enabled: true },
    { name: "模型验证", icon: FlaskConical, enabled: true },
    { name: "使用记录", icon: ScrollText, enabled: false },
  ];
  /** 面包屑末级显示的设置分类名称。 */
  const sectionName = $derived(
    settingsSections.find((section) => section.id === settingsSection)?.name,
  );

  /** 当前显示的操作反馈文字，空字符串表示不显示；toastTimer 负责到时清除。 */
  let toast = $state("");
  let toastTimer: ReturnType<typeof setTimeout> | undefined;
  /** 显示一条非阻塞的操作反馈，3.5 秒后消失；连续操作替换上一条并重新计时。 */
  function notify(message: string): void {
    toast = message;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => (toast = ""), 3500);
  }

  /** 点击“设置”进入常规，点击子分类切换面板；停留在设置页期间各分类保持挂载，切换分类不丢草稿。 */
  function openSettings(section: SettingsSection = "general"): void {
    settingsSection = section;
    page = "设置";
  }
</script>

<svelte:head
  ><title>Vela · {page === "模型" ? "模型管理" : page}</title></svelte:head
>
<div class="app-shell" class:sidebar-collapsed={sidebarCollapsed}>
  <aside class="sidebar" id="app-sidebar" aria-label="侧栏">
    <div class="sidebar-controls">
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label={sidebarCollapsed ? "展开侧栏" : "收起侧栏"}
        title={sidebarCollapsed ? "展开侧栏" : "收起侧栏"}
        aria-expanded={!sidebarCollapsed}
        aria-controls="app-sidebar"
        onclick={() => (sidebarCollapsed = !sidebarCollapsed)}
      >
        {#if sidebarCollapsed}<PanelLeftOpen />{:else}<PanelLeftClose />{/if}
      </Button>
    </div>
    <div class="sidebar-brand mb-8 flex items-center gap-3 px-2">
      <span class="text-primary"><VelaMark class="size-9" /></span>
      <div>
        <p class="text-xl font-bold tracking-tight">Vela</p>
        <p class="text-[10px] tracking-widest text-muted-foreground">
          AI GATEWAY
        </p>
      </div>
    </div>
    <nav aria-label="主导航" class="flex flex-col gap-1">
      {#each navigation as item}<button
          class:active={page === item.name}
          class="nav-item"
          aria-label={item.name}
          aria-current={page === item.name ? "page" : undefined}
          title={sidebarCollapsed ? item.name : undefined}
          disabled={!item.enabled}
          onclick={() => (page = item.name)}
          ><item.icon class="size-4" aria-hidden="true" /><span>{item.name}</span></button
        >{/each}
    </nav>
    <div class="settings-nav">
      <button
        class="nav-item w-full"
        class:active={page === "设置"}
        aria-label="设置"
        title={sidebarCollapsed ? "设置" : undefined}
        aria-current={page === "设置" ? "page" : undefined}
        aria-expanded={page === "设置" && !sidebarCollapsed}
        onclick={() => openSettings()}
        ><Settings2 class="size-4" aria-hidden="true" /><span>设置</span></button
      >
      {#if page === "设置" && !sidebarCollapsed}
        <nav class="settings-subnav" aria-label="设置分类">
          {#each settingsSections as section}
            <button
              class:active={settingsSection === section.id}
              aria-current={settingsSection === section.id ? "page" : undefined}
              onclick={() => openSettings(section.id)}>{section.name}</button
            >
          {/each}
        </nav>
      {/if}
    </div>
    <p class="px-3 pt-4 text-xs text-muted-foreground">Vela 0.1.0</p>
  </aside>
  <div class="min-w-0">
    <header
      class="app-titlebar flex h-16 items-center justify-between px-7"
    >
      <p class="text-sm text-muted-foreground">
        工作空间 <span class="mx-3 text-border">/</span><span
          class="text-foreground">{page}</span
        >{#if page === "设置"}<span class="mx-3 text-border">/</span><span
            class="text-foreground">{sectionName}</span
          >{/if}
      </p>
      <span class="tag">本地优先</span>
    </header>
    <main class="mx-auto max-w-7xl p-5 lg:p-8">
      {#if page === "供应商"}<ProvidersPage {notify} openAccountSettings={() => openSettings("account")} />{:else if page === "模型"}<ModelsPage />{:else if page === "调度"}<SchedulingPage
          {notify}
        />{:else if page === "模型验证"}<VerificationPage {notify} />{:else}<SettingsPage
          section={settingsSection}
          {notify}
        />{/if}
    </main>
  </div>
</div>
{#if toast}<div class="toast" role="status">{toast}</div>{/if}
