<script lang="ts">
  import { onMount } from "svelte";
  import { Check, RefreshCw } from "@lucide/svelte";
  import { isMyGo } from "mygo-runtime";
  import { Button } from "$lib/components/ui/button";
  import VelaMark from "$lib/components/VelaMark.svelte";
  import { VelaService, type RuntimeInfo } from "$lib/generated/mygo";

  /** 页面状态，区分真实桌面运行时、浏览器预览和读取失败。 */
  type Phase = "loading" | "live" | "preview" | "error";

  const navigation = ["总览", "供应商", "模型", "调度", "使用记录", "设置"];
  const stack = [
    "Go 1.27",
    "MyGo 0.8",
    "Svelte 5",
    "Vite 8",
    "Tailwind CSS 4",
    "shadcn-svelte",
  ];

  let phase = $state<Phase>("loading");
  let runtime = $state<RuntimeInfo | null>(null);
  let errorMessage = $state("");

  let statusLabel = $derived.by(() => {
    if (phase === "live") return "框架运行中";
    if (phase === "preview") return "浏览器预览";
    if (phase === "error") return "读取失败";
    return "正在连接";
  });

  /** 读取 Go 应用服务；普通浏览器中不伪造 MyGo 运行时。 */
  async function loadRuntime(): Promise<void> {
    phase = "loading";
    errorMessage = "";

    if (!isMyGo()) {
      phase = "preview";
      return;
    }

    try {
      runtime = await VelaService.runtimeInfo();
      phase = "live";
    } catch (error) {
      phase = "error";
      errorMessage = error instanceof Error ? error.message : String(error);
    }
  }

  onMount(() => {
    void loadRuntime();
  });
</script>

<svelte:head>
  <title>Vela</title>
</svelte:head>

<div class="min-h-screen bg-[#f2f0e9] text-foreground">
  <div class="grid min-h-screen lg:grid-cols-[220px_1fr]">
    <aside class="hidden border-r border-border/80 bg-[#ebe7dd]/75 p-4 lg:flex lg:flex-col">
      <div class="flex items-center gap-3 px-2 py-2">
        <span
          class="grid size-9 place-items-center rounded-[10px] bg-white text-[#29473e] shadow-sm ring-1 ring-black/5"
        >
          <VelaMark class="size-9" />
        </span>
        <div>
          <p class="font-serif text-2xl leading-none">Vela</p>
          <p class="mt-1 text-[0.52rem] font-bold uppercase tracking-[0.2em] text-muted-foreground">
            AI gateway
          </p>
        </div>
      </div>

      <nav class="mt-8 space-y-1" aria-label="主导航">
        {#each navigation as item, index}
          <button
            type="button"
            class={[
              "w-full rounded-lg px-3 py-2.5 text-left text-[0.76rem] font-semibold",
              index === 0
                ? "bg-[#17363b] text-[#f8f3e9]"
                : "text-muted-foreground",
            ]}
            disabled={index !== 0}
          >
            {item}
          </button>
        {/each}
      </nav>

      <p class="mt-auto px-3 text-[0.58rem] leading-5 text-muted-foreground">
        当前仓库仅完成基础工程初始化，业务功能尚未实现。
      </p>
    </aside>

    <div class="flex min-w-0 flex-col">
      <header class="flex min-h-16 items-center justify-between border-b border-border/80 bg-[#f8f5ee]/85 px-4 sm:px-6">
        <div class="flex items-center gap-3">
          <span
            class="grid size-8 place-items-center rounded-lg bg-white text-[#29473e] shadow-sm ring-1 ring-black/5 lg:hidden"
          >
            <VelaMark class="size-8" />
          </span>
          <div>
            <p class="text-[0.55rem] font-bold uppercase tracking-[0.18em] text-muted-foreground">
              Vela / Workspace
            </p>
            <p class="mt-0.5 text-[0.86rem] font-bold">总览</p>
          </div>
        </div>
        <div class="flex items-center gap-2">
          <span class="hidden rounded-full border border-border/80 bg-background px-3 py-1.5 text-[0.62rem] font-semibold sm:inline">
            {statusLabel}
          </span>
          <Button
            variant="outline"
            size="icon-sm"
            aria-label="刷新运行状态"
            onclick={() => void loadRuntime()}
          >
            <RefreshCw class={phase === "loading" ? "animate-spin" : ""} />
          </Button>
        </div>
      </header>

      <main class="mx-auto w-full max-w-[1080px] flex-1 p-4 sm:p-6 lg:p-10">
        <div>
          <p class="text-[0.58rem] font-bold uppercase tracking-[0.2em] text-primary">
            Application baseline
          </p>
          <h1 class="mt-2 font-serif text-[clamp(2.4rem,5vw,4.4rem)] leading-none tracking-[-0.05em]">
            基础框架已就绪
          </h1>
          <p class="mt-4 max-w-[680px] text-[0.82rem] leading-6 text-muted-foreground">
            Go、MyGo、Svelte、Vite、Tailwind CSS 与 shadcn-svelte 已完成最小接入。
            这里不承载业务首页，后续页面将在同一应用壳内逐步迁移。
          </p>
        </div>

        <section class="mt-8 grid gap-3 md:grid-cols-[1.2fr_0.8fr]">
          <div class="rounded-2xl border border-border/80 bg-card/80 p-5 sm:p-6">
            <div class="flex items-center justify-between gap-3">
              <div>
                <p class="text-[0.58rem] font-bold uppercase tracking-[0.18em] text-muted-foreground">
                  Runtime
                </p>
                <h2 class="mt-2 font-serif text-[1.8rem] leading-none">
                  {phase === "live" ? "桌面桥接正常" : statusLabel}
                </h2>
              </div>
              <span class="grid size-8 place-items-center rounded-full bg-primary/10 text-primary">
                <Check class="size-4" aria-hidden="true" />
              </span>
            </div>

            <div class="mt-6 divide-y divide-border/75 rounded-xl border border-border/75 bg-background/60 px-4">
              <div class="flex items-center justify-between gap-4 py-3 text-[0.7rem]">
                <span class="text-muted-foreground">应用版本</span>
                <span class="font-semibold">v{runtime?.version ?? "0.1.0"}</span>
              </div>
              <div class="flex items-center justify-between gap-4 py-3 text-[0.7rem]">
                <span class="text-muted-foreground">运行环境</span>
                <span class="font-semibold">
                  {runtime?.platform === "darwin" ? "macOS" : runtime?.platform ?? "浏览器"}
                  · {runtime?.architecture ?? "arm64"}
                </span>
              </div>
              <div class="flex items-center justify-between gap-4 py-3 text-[0.7rem]">
                <span class="text-muted-foreground">服务状态</span>
                <span class="font-semibold">{statusLabel}</span>
              </div>
            </div>

            {#if phase === "preview"}
              <p class="mt-3 text-[0.64rem] leading-5 text-muted-foreground">
                浏览器模式无法调用 MyGo 绑定，桌面窗口打开后会显示真实运行信息。
              </p>
            {:else if phase === "error"}
              <p class="mt-3 text-[0.64rem] leading-5 text-destructive">{errorMessage}</p>
            {/if}
          </div>

          <div class="rounded-2xl border border-border/80 bg-[#e9e0d0]/75 p-5 sm:p-6">
            <p class="text-[0.58rem] font-bold uppercase tracking-[0.18em] text-muted-foreground">
              Stack
            </p>
            <h2 class="mt-2 font-serif text-[1.8rem] leading-none">技术栈</h2>
            <div class="mt-5 flex flex-wrap gap-2">
              {#each stack as item}
                <span class="rounded-full border border-foreground/10 bg-background/60 px-3 py-1.5 text-[0.62rem] font-semibold">
                  {item}
                </span>
              {/each}
            </div>
          </div>
        </section>

        <section class="mt-3 rounded-2xl border border-dashed border-border bg-card/40 p-5 text-[0.72rem] leading-6 text-muted-foreground sm:p-6">
          下一步再创建供应商、模型、调度、SQLite 和网关业务模块。当前提交范围内不预置业务接口或模拟数据。
        </section>
      </main>

      <footer class="border-t border-border/75 px-4 py-3 text-[0.56rem] font-bold uppercase tracking-[0.14em] text-muted-foreground sm:px-6">
        Vela 0.1.0 · Application scaffold
      </footer>
    </div>
  </div>
</div>
