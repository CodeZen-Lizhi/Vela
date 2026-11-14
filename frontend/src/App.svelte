<script lang="ts">
  import { onMount } from "svelte";
  import OverviewPage from "$lib/overview/OverviewPage.svelte";
  import { observeAttention } from "$lib/overview/attention.svelte";
  // 关注状态由外壳持有，切页继续观察已知倍率变化。
  onMount(observeAttention);
  import { Button } from "$lib/components/ui/button";
  import { Toaster } from "$lib/components/ui/sonner";
  import { toast } from "svelte-sonner";
  import { mergeProps } from "bits-ui";
  import { Hint } from "$lib/components/ui/tooltip";
  import { Provider as TooltipProvider } from "$lib/components/ui/tooltip";
  import {
    Box,
    Settings2,
    LayoutDashboard,
    Waypoints,
    Network,
    ScrollText,
    FlaskConical,
  } from "@lucide/svelte";
  import * as Resizable from "$lib/components/ui/resizable";
  import VelaMark from "$lib/components/VelaMark.svelte";
  import VerificationPage from "$lib/verification/VerificationPage.svelte";
  import RecordsPage from "$lib/records/RecordsPage.svelte";
  import ModelsPage from "$lib/models/ModelsPage.svelte";
  import ProvidersPage from "$lib/providers/ProvidersPage.svelte";
  import SchedulingPage from "$lib/scheduling/SchedulingPage.svelte";
  import SettingsPage from "$lib/settings/SettingsPage.svelte";
  import { settingsSections, type SettingsSection } from "$lib/settings/sections";
  /** 当前显示的顶级页面，默认打开已接入真实统计的总览。 */
  let page = $state("总览");
  /** 设置子分类是否展开；已在设置页时由侧栏入口切换。 */
  let settingsExpanded = $state(false);
  /** 默认168、最大240；最小72并避让系统按钮组，分栏内部使用百分比。 */
  const sidebarMax = 240, sidebarDefault = 168;
  let sidebarMin = $state(72);
  /** 容器与侧栏实测宽度，文字所需宽度按当前字体及实际菜单测量。 */
  let shellWidth = $state(0), sidebarWidth = $state(sidebarDefault), labelWidth = $state(160);
  /** 已挂载侧栏节点，用于观察实际宽度与文字尺寸，离开时释放观察。 */
  let sidebarElement = $state<HTMLElement>();
  /** 文字无法单行完整容纳时整体切换为图标，恢复时不改变当前页面。 */
  const sidebarCollapsed = $derived(sidebarWidth + 0.5 < labelWidth);

  $effect(() => {
    const element = sidebarElement;
    if (!element) return;
    /** 按基础菜单完整单行所需空间决定模式，不让隐藏的设置子菜单改变阈值。 */
    const measureSidebar = (): void => {
      sidebarWidth = element.getBoundingClientRect().width;
      const style = getComputedStyle(element);
      const controlsWidth = parseFloat(style.getPropertyValue("--mygo-titlebar-inset-left")) || 0;
      sidebarMin = Math.max(72, controlsWidth + 8);
      const inset = parseFloat(style.paddingLeft) + parseFloat(style.paddingRight) + 1;
      const widths = [...element.querySelectorAll<HTMLElement>(".nav-item")].map((item) => {
        const text = item.querySelector<HTMLElement>("span");
        const itemStyle = getComputedStyle(item);
        return (text?.scrollWidth ?? 0) + (item.querySelector("svg")?.getBoundingClientRect().width ?? 16) + parseFloat(itemStyle.columnGap) + 2 * parseFloat(style.getPropertyValue("--sidebar-nav-inset"));
      });
      labelWidth = Math.ceil(inset + Math.max(...widths));
    };
    // 显隐文字会改变被观察节点的尺寸，下一轮事件测量避免同轮ResizeObserver反馈，也支持后台窗口。
    let measurement: ReturnType<typeof setTimeout>;
    const observer = new ResizeObserver(() => {
      clearTimeout(measurement);
      measurement = setTimeout(measureSidebar, 0);
    });
    observer.observe(element);
    for (const label of element.querySelectorAll(".nav-item span")) observer.observe(label);
    measureSidebar();
    return () => {
      observer.disconnect();
      clearTimeout(measurement);
    };
  });
  /** 设置子分类由外壳持有，侧栏、面包屑与内容面板共用。 */
  let settingsSection = $state<SettingsSection>("general");
  /** 已实现入口正常导航，其余模块保持明确的待开发状态。 */
  const navigation = [
    { name: "总览", icon: LayoutDashboard, enabled: true },
    { name: "供应商", icon: Network, enabled: true },
    { name: "模型", icon: Box, enabled: true },
    { name: "调度", icon: Waypoints, enabled: true },
    { name: "模型验证", icon: FlaskConical, enabled: true },
    { name: "使用记录", icon: ScrollText, enabled: true },
  ];
  /** 面包屑末级显示的设置分类名称。 */
  const sectionName = $derived(
    settingsSections.find((section) => section.id === settingsSection)?.name,
  );

  /** 全应用共用官方通知组件，连续操作更新同一条反馈，3.5秒后自动关闭。 */
  function notify(message: string): void {
    toast(message, { id: "vela-feedback", duration: 3500 });
  }

  /** 点击“设置”进入常规并展开子分类；已在设置页时再次点击切换收起或展开。 */
  function toggleSettings(): void {
    if (page !== "设置") {
      settingsSection = "general";
      page = "设置";
      settingsExpanded = true;
      return;
    }
    settingsExpanded = !settingsExpanded;
  }

  /** 打开指定设置分类并展开侧栏；停留在设置页期间各分类保持挂载，切换分类不丢草稿。 */
  function openSettings(section: SettingsSection = "general"): void {
    settingsSection = section;
    page = "设置";
    settingsExpanded = true;
  }
</script>

<svelte:head
  ><title>Vela · {page === "模型" ? "模型管理" : page}</title></svelte:head
>
<TooltipProvider>
<div class="app-shell" class:sidebar-collapsed={sidebarCollapsed} bind:clientWidth={shellWidth}>
  {#if shellWidth > 0}
  <Resizable.PaneGroup direction="horizontal" keyboardResizeBy={800 / shellWidth} class="app-panels">
  <Resizable.Pane defaultSize={sidebarDefault / shellWidth * 100} minSize={sidebarMin / shellWidth * 100} maxSize={sidebarMax / shellWidth * 100}>

  <aside class="sidebar" id="app-sidebar" aria-label="侧栏" bind:this={sidebarElement}>
    <div class="sidebar-header">
      <div class="sidebar-brand">
        <span class="text-primary"><VelaMark class="size-7" /></span>
        <p class="text-xl font-bold tracking-tight">Vela</p>
      </div>

    </div>
    <nav aria-label="主导航" class="flex flex-col gap-1">
      {#each navigation as item}<Hint side="right" overflowOnly={false} text={sidebarCollapsed ? item.name : undefined}>{#snippet children(hintProps0)}<Button {...mergeProps(hintProps0, { onclick: () => {page = item.name;} })}
          variant="navigation" size="navigation"
          class="nav-item"
          aria-label={item.name}
          aria-current={page === item.name ? "page" : undefined}

          disabled={!item.enabled}

          ><item.icon class="size-4" aria-hidden="true" /><span>{item.name}</span></Button
        >{/snippet}</Hint>{/each}
    </nav>
    <div class="settings-nav">
      <Hint side="right" overflowOnly={false} text={sidebarCollapsed ? "设置" : undefined}>{#snippet children(hintProps1)}<Button {...mergeProps(hintProps1, { onclick: toggleSettings })}
        class="nav-item w-full"
        variant="navigation" size="navigation"
        aria-label="设置"

        aria-current={page === "设置" ? "page" : undefined}
        aria-expanded={page === "设置" && settingsExpanded && !sidebarCollapsed}
        aria-controls={page === "设置" && settingsExpanded && !sidebarCollapsed ? "settings-subnav" : undefined}

        ><Settings2 class="size-4" aria-hidden="true" /><span>设置</span></Button
      >{/snippet}</Hint>
      {#if page === "设置" && settingsExpanded && !sidebarCollapsed}
        <nav id="settings-subnav" class="settings-subnav" aria-label="设置分类">
          {#each settingsSections as section}
            <Button
              variant="subnavigation" size="subnavigation"
              aria-current={settingsSection === section.id ? "page" : undefined}
              onclick={() => openSettings(section.id)}>{section.name}</Button
            >
          {/each}
        </nav>
      {/if}
    </div>
    <p class="px-3 pt-4 text-xs text-muted-foreground">Vela {import.meta.env.VITE_VELA_VERSION}</p>
  </aside>
  </Resizable.Pane>
  <Resizable.Handle  aria-label="调整侧栏宽度"  />
  <Resizable.Pane>
  <div class="app-workspace">
    <header class="app-titlebar">
      {#if page === "设置"}<p class="text-xs text-muted-foreground">
        设置 <span class="mx-2 text-border">/</span><span class="text-foreground">{sectionName}</span>
      </p>{/if}
    </header>
    <main class="app-main" class:list-workspace={["供应商", "模型", "使用记录", "模型验证"].includes(page)}>
      {#if page === "总览"}<OverviewPage {notify} navigate={(target) => { if (target === "providers") page = "供应商"; else openSettings("connection"); }} />{:else if page === "供应商"}<ProvidersPage {notify} />{:else if page === "模型"}<ModelsPage {notify} />{:else if page === "调度"}<SchedulingPage
          {notify}
        />{:else if page === "使用记录"}<RecordsPage {notify} />{:else if page === "模型验证"}<VerificationPage {notify} />{:else}<SettingsPage
          section={settingsSection}
          {notify}
        />{/if}
    </main>
  </div>
  </Resizable.Pane>
  </Resizable.PaneGroup>
  {/if}
</div>
<Toaster position="bottom-center" />

</TooltipProvider>
