<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { Hint } from "$lib/components/ui/tooltip";
  import { onMount, tick } from "svelte";
  import { isMyGo } from "mygo-runtime";
  import {
    CloudDownload,
    PencilLine,
    RefreshCw,
    Search,
    Eye,
    Pencil,
    Trash2,
    Box,
    Copy,
    Check,
    ArrowLeft,
  } from "@lucide/svelte";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import { Button } from "$lib/components/ui/button";
  import * as Select from "$lib/components/ui/select";
  import * as InputGroup from "$lib/components/ui/input-group";
  import ModelCapabilities from "./ModelCapabilities.svelte";
  import PriceSummary from "./PriceSummary.svelte";
  import ModelDetails from "./ModelDetails.svelte";
  import * as Dialog from "$lib/components/ui/dialog";
  import * as Table from "$lib/components/ui/table";
  import ModelCatalog from "./ModelCatalog.svelte";
  import ModelEditor from "./ModelEditor.svelte";
  import {
    VelaService,
    type Model,
    type CatalogEntry,
    type Pricing,
  } from "$lib/generated/mygo";
  import { kinds, dateLabel, modelInitial } from "./display";
  let models = $state<Model[]>([]),
    catalog = $state<CatalogEntry[]>([]);
  let query = $state(""),
    kind = $state("");
  let loading = $state(true),
    busy = $state(false),
    catalogLoading = $state(false),
    error = $state("");
  let restoredGroups = $state<string[]>([]);
  /** 创建草稿只在最终提交时落库；来源条目用于保持精确来源身份。 */
  let createDraft = $state<Model | null>(null);
  let createEntry = $state<CatalogEntry | null>(null);
  /** 在线目录始终挂载，返回时用于恢复搜索焦点且不改变卡片滚动位置。 */
  let catalogPanel = $state<HTMLDivElement | null>(null);
  /** 最近成功复制的模型记录，用于按钮的轻量成功反馈。 */
  let copiedModelId = $state("");
  let addOpen = $state(false),
    editorOpen = $state(false),
    deleteOpen = $state(false);
  let selected = $state<Model | null>(null),
    draft = $state<Model | null>(null),
    readOnly = $state(false),
    dialogError = $state("");
  const filtered = $derived(
    models.filter(
      (m) =>
        (!kind || kind === "all" || m.kind === kind) &&
        `${m.name} ${m.modelId} ${m.family} ${m.providerName} ${m.capabilities.map((c) => capabilityLabel(c)).join(" ")}`
          .toLowerCase()
          .includes(query.toLowerCase()),
    ),
  );
  const lastSync = $derived(
    models
      .map((m) => m.syncedAt)
      .sort()
      .at(-1) ?? "",
  );

  /** 统一错误显示，保留失败时的列表和表单草稿。 */
  function message(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
  }
  /** 加载真实 Go 数据；浏览器预览保持空白并明确说明能力范围。 */
  async function load(): Promise<void> {
    loading = true;
    error = "";
    try {
      if (!isMyGo()) return;
      models = await VelaService.listModels();
    } catch (e) {
      error = message(e);
    } finally {
      loading = false;
    }
  }
  /** 直接打开手动创建表单，不加载在线目录；取消不写入。 */
  function openManual(): void {
    selectForCreate(null);
    addOpen = true;
  }
  /** 打开在线目录，在真实请求结束后显示候选或错误。 */
  async function openCatalog(): Promise<void> {
    createDraft = null;
    createEntry = null;
    addOpen = true;
    dialogError = "";
    if (catalog.length) return;
    catalogLoading = true;
    try {
      catalog = await VelaService.modelCatalog();
    } catch (e) {
      dialogError = message(e);
    } finally {
      catalogLoading = false;
    }
  }
  /** 仅构造独立创建草稿，取消不写入；entry 为空表示手动创建。 */
  function selectForCreate(entry: CatalogEntry | null): void {
    createEntry = entry;
    dialogError = "";
    const pricing: Pricing = {base: [], tiers: [], modes: [], schedules: [], baseline: "", images: [], videos: []};
    createDraft = {id: "", revision: 0, modelId: entry?.canonicalId.split("/")[1] || entry?.modelId || "", name: entry?.name ?? "", family: entry?.family ?? "", kind: entry?.kind ?? "chat", context: entry?.context ?? 0, capabilities: [...(entry?.capabilities ?? [])], providerId: entry?.providerId ?? "", providerName: entry?.providerName ?? "手动创建", sourceModelId: entry?.modelId ?? "", canonicalId: entry?.canonicalId ?? "", official: entry?.official ?? false, sourceAvailable: !!entry, syncedAt: "", pricing: entry ? structuredClone($state.snapshot(entry.pricing)) : pricing, sourcePricing: entry ? structuredClone($state.snapshot(entry.pricing)) : pricing, overrides: []};
  }
  /** 返回目录丢弃未提交草稿，保留目录筛选和滚动位置；焦点回到搜索框。 */
  async function returnToCatalog(): Promise<void> {
    createDraft = null;
    createEntry = null;
    dialogError = "";
    await tick();
    catalogPanel?.querySelector<HTMLInputElement>("input")?.focus({ preventScroll: true });
  }
  /** 最终创建一次提交名称与本地价格，失败保留草稿。 */
  async function add(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (!createDraft) return;
    busy = true;
    dialogError = "";
    try {
      await VelaService.createModel({providerId: createEntry?.providerId ?? "", modelId: createDraft.modelId, sourceModelId: createEntry?.modelId ?? "", name: createDraft.name, kind: createDraft.kind, pricing: createDraft.pricing});
      await load();
      addOpen = false;
    } catch (e) {
      dialogError = message(e);
    } finally {
      busy = false;
    }
  }
  /** 同步会替换 Go 目录候选，先失效前端快照，下次新增重新读取。 */
  async function sync(): Promise<void> {
    busy = true;
    error = "";
    catalog = [];
    try {
      const result = await VelaService.syncModelPrices();
      await load();
      if (result.failures.length) error = `部分模型未更新：${result.failures.join("；")}`;
    } catch (e) {
      error = message(e);
    } finally {
      busy = false;
    }
  }
  /** 编辑使用独立草稿；等待写入或刷新完成后再打开，避免从旧列表创建草稿。 */
  function openEditor(m: Model, detail: boolean): void {
    if (busy || loading) return;
    restoredGroups = [];
    selected = m;
    draft = structuredClone($state.snapshot(m));
    readOnly = detail;
    dialogError = "";
    editorOpen = true;
  }
  /** 保存后重读；失败保留草稿并刷新列表，取消重开时可取得最新版本。 */
  async function save(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (!draft) return;
    busy = true;
    dialogError = "";
    try {
      await VelaService.updateModel({
        id: draft.id,
        revision: draft.revision,
        name: draft.name,
        pricing: draft.pricing,
        restoreGroups: restoredGroups,
      });
      await load();
      editorOpen = false;
    } catch (e) {
      dialogError = message(e);
      await load();
    } finally {
      busy = false;
    }
  }
  /** 恢复操作与编辑草稿共同保存；无来源时在操作处直接反馈错误。 */
  function restore(group: string): void {
    if (!draft) return;
    dialogError = "";
    const source = draft.sourcePricing;
    const values = source[group as keyof Pricing];
    if (!draft.sourceAvailable || !Array.isArray(values) || !values.length) {
      dialogError = "没有可用的来源值，已保留本地配置";
      return;
    }
    const g = group as
      "base" | "tiers" | "modes" | "schedules" | "images" | "videos";
    Object.assign(draft.pricing, {
      [g]: structuredClone($state.snapshot(source[g])),
    });
    if (group === "schedules") draft.pricing.baseline = source.baseline;
    draft.overrides = draft.overrides.filter((v) => v !== group);
    restoredGroups = [...new Set([...restoredGroups, group])];
  }
  /** 确认后删除独立模型，取消不产生写入。 */
  async function remove(): Promise<void> {
    if (!selected) return;
    busy = true;
    dialogError = "";
    try {
      await VelaService.deleteModel(selected.id);
      await load();
      deleteOpen = false;
    } catch (e) {
      dialogError = message(e);
    } finally {
      busy = false;
    }
  }

  /** 能力名参与搜索，使用已记录能力，不从模型名称推断。 */
  function capabilityLabel(value: string): string {
    return (
      (
        {
          text: "文本",
          image: "视觉",
          tools: "工具",
          reasoning: "推理",
          audio: "音频",
          video: "视频",
          pdf: "文档",
        } as Record<string, string>
      )[value] ?? value
    );
  }
  /** 已知模型生产方用于品牌展示，未知生产方保留真实来源名称。 */
  function brand(model: Model): string {
    const lab = model.canonicalId.split("/")[0];
    return (
      (
        {
          anthropic: "Anthropic",
          openai: "OpenAI",
          google: "Google",
          deepseek: "DeepSeek",
          mistral: "Mistral",
          cohere: "Cohere",
          meta: "Meta",
        } as Record<string, string>
      )[lab ?? ""] ?? model.providerName
    );
  }
  /** 复制真实模型 ID，不复制用户自定义的显示名称。 */
  async function copyModelId(model: Model): Promise<void> {
    copiedModelId = "";
    try {
      await VelaService.copyText(model.modelId);
      copiedModelId = model.id;
    } catch (e) {
      error = `复制失败：${message(e)}`;
    }
  }

  onMount(() => {
    void load();
  });
</script>

<div class="list-page models-page">
  <PageHeader title="模型" description="统一管理模型与标准价格，调用成本按本地规则估算。">
    {#snippet actions()}
        <Button
          variant="outline"
          size="sm"
          disabled={busy || catalogLoading || !isMyGo() || !models.length}
          onclick={() => void sync()}
          ><RefreshCw class={busy ? "animate-spin" : ""} />同步全部价格</Button
        >
        <Button
          size="sm"
          disabled={busy || !isMyGo()}
          onclick={() => void openCatalog()}><CloudDownload />在线添加</Button
        >
        <Button
          variant="outline"
          size="sm"
          disabled={busy || !isMyGo()}
          onclick={openManual}><PencilLine />手动创建</Button
        >
    {/snippet}
  </PageHeader>
  {#if !isMyGo()}<Alert.Root variant="info"><Alert.Description>
      浏览器预览 · 数据操作请在 Vela 桌面窗口中进行。
    </Alert.Description></Alert.Root>{/if}
  {#if error}<Alert.Root variant="destructive"><Alert.Description>
      {error}<Button variant="ghost" size="sm" onclick={() => void load()}
        >重试</Button
      >
    </Alert.Description></Alert.Root>{/if}
  <section class="list-surface" aria-label="模型列表">
    <div class="model-toolbar">
      <div class="model-filters">
        <InputGroup.Root class="model-search">
          <InputGroup.Input
            aria-label="搜索模型"
            placeholder="搜索名称、ID、系列或能力"
            bind:value={query}
          />
          <InputGroup.Addon
            ><Search class="size-4" aria-hidden="true" /></InputGroup.Addon
          >
        </InputGroup.Root>
        <Select.Root type="single" bind:value={kind}>
          <Select.Trigger class="model-kind-filter" aria-label="模型类型"
            >{kinds[kind] ?? "全部类型"}</Select.Trigger
          >
          <Select.Content
            ><Select.Group
              ><Select.Item value="all">全部类型</Select.Item
              >{#each Object.entries(kinds) as [id, label]}<Select.Item
                  value={id}>{label}</Select.Item
                >{/each}</Select.Group
            ></Select.Content
          >
        </Select.Root>
      </div>
    </div>
    <!-- 独立滚动区需要键盘焦点，支持方向键与翻页键浏览列表。 -->
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <div class="list-scroll" role="region" aria-label="模型列表内容" tabindex="0">
    {#if loading}<p class="p-16 text-center text-muted-foreground">
        正在读取模型…
      </p>
    {:else if filtered.length === 0}<div
        class="flex flex-col items-center gap-3 px-6 py-20"
      >
        <Box class="size-10 text-muted-foreground" />
        <h2 class="font-semibold">
          {models.length ? "没有匹配的模型" : "添加你的第一个模型"}
        </h2>
        <p class="text-sm text-muted-foreground">
          {models.length
            ? "试试其他名称或类型"
            : "从在线目录带入模型资料和价格，或手动创建目录外的模型。"}
        </p>
        {#if !models.length}<div class="flex gap-2">
            <Button
              variant="outline"
              disabled={!isMyGo()}
              onclick={() => void openCatalog()}
              ><CloudDownload />在线添加</Button
            >
            <Button variant="outline" disabled={!isMyGo()} onclick={openManual}
              ><PencilLine />手动创建</Button
            >
          </div>{/if}
      </div>
    {:else}<Table.Root class="model-table"
        ><Table.Header
          ><Table.Row
            ><Table.Head>模型</Table.Head><Table.Head>能力</Table.Head
            ><Table.Head data-align="number">目录价格</Table.Head><Table.Head data-align="control"
              >操作</Table.Head
            ></Table.Row
          ></Table.Header
        ><Table.Body
          >{#each filtered as model (model.id)}<Table.Row
              ><Table.Cell class="model-main-cell">
                <div class="model-identity">
                  <span
                    class="model-brand"
                    data-brand={model.canonicalId.split("/")[0]}
                    aria-hidden="true"
                    >{modelInitial(
                      model.name,
                      brand(model),
                      !model.providerId,
                    )}</span
                  >
                  <div class="model-identity-copy">
                    <strong>{model.name}</strong>
                    <div class="model-id-row">
                      <Hint overflowOnly text={model.modelId}>{#snippet children(hintProps0)}<code {...hintProps0} >{model.modelId}</code>{/snippet}</Hint>
                      <Button onclick={() => void copyModelId(model)}
                        variant="ghost"
                        size="icon-xs"
                        aria-label={`复制模型 ID ${model.modelId}`}

                      >
                        {#if copiedModelId === model.id}<Check />{:else}<Copy
                          />{/if}
                      </Button>
                    </div>
                  </div>
                </div>
              </Table.Cell><Table.Cell class="model-capability-cell"
                ><ModelCapabilities values={model.capabilities} /></Table.Cell
              ><Table.Cell class="model-price-cell" data-align="number"
                ><PriceSummary
                  pricing={model.pricing}
                  kind={model.kind}
                  modified={model.overrides.length > 0}
                /></Table.Cell
              ><Table.Cell data-align="control"
                ><div class="flex gap-0.5">
                  <Button
                    variant="ghost"
                    size="icon-xs"
                    aria-label={`查看 ${model.name}`}
                    disabled={busy || loading}
                    onclick={() => openEditor(model, true)}><Eye /></Button
                  ><Button
                    variant="ghost"
                    size="icon-xs"
                    aria-label={`编辑 ${model.name}`}
                    disabled={busy || loading}
                    onclick={() => openEditor(model, false)}><Pencil /></Button
                  ><Button
                    variant="ghost"
                    size="icon-xs"
                    aria-label={`删除 ${model.name}`}
                    onclick={() => {
                      selected = model;
                      dialogError = "";
                      deleteOpen = true;
                    }}><Trash2 /></Button
                  >
                </div></Table.Cell
              ></Table.Row
            >{/each}</Table.Body
        ></Table.Root
      >{/if}
    </div>
    <div class="list-footer border-t px-4 py-3 text-xs text-muted-foreground">
      来源 models.dev · 最近同步 {dateLabel(lastSync)}
    </div>
  </section>
</div>

<!-- 在线目录与确认表单保持相同宽高，切换不跳动；手动创建随内容收缩。 -->
<Dialog.Root bind:open={addOpen}>
  <Dialog.Content
    class={createDraft && !createEntry
      ? "flex max-h-[86dvh] flex-col gap-0 overflow-hidden p-0 sm:max-w-[min(760px,88vw)]"
      : "flex h-[min(86dvh,720px)] flex-col gap-0 overflow-hidden p-0 sm:max-w-[min(920px,88vw)]"}
  >
    <Dialog.Header class="h-16 shrink-0 flex-row items-center gap-3 border-b px-6 pr-16"
      >{#if createDraft && createEntry}<Button variant="ghost" size="sm" disabled={busy} onclick={() => void returnToCatalog()}><ArrowLeft data-icon="inline-start" />返回</Button>{/if}<Dialog.Title
        >{createDraft
          ? createEntry
            ? "确认模型"
            : "手动创建模型"
          : "在线添加模型"}</Dialog.Title
      ><Dialog.Description class="sr-only"
        >{createDraft && !createEntry
          ? "填写目录外模型的名称、模型 ID、类型和价格"
          : "从 models.dev 在线目录选择模型，确认后创建"}</Dialog.Description
      ></Dialog.Header
    >
    {#if createDraft && !createEntry}
      <div class="flex min-h-0 flex-1 flex-col">
        <ModelEditor
          bind:model={createDraft}
          {busy}
          error={dialogError}
          mode={createEntry ? "online" : "manual"}
          onsave={add}
          onrestore={restore}
          onclose={() => (addOpen = false)}
        />
      </div>
    {:else}
      <div bind:this={catalogPanel} class={createDraft ? "hidden" : "flex min-h-0 flex-1 flex-col px-6 pt-5"}>
        <ModelCatalog
          {catalog}
          loading={catalogLoading}
          {busy}
          error={dialogError}
          onretry={() => void openCatalog()}
          onselect={selectForCreate}
        />
      </div>
      {#if createDraft}
        <div class="flex min-h-0 flex-1 flex-col">
          <ModelEditor
            bind:model={createDraft}
            {busy}
            error={dialogError}
            mode="online"
            onsave={add}
            onrestore={restore}
            onclose={() => (addOpen = false)}
          />
        </div>
      {/if}
    {/if}
  </Dialog.Content>
</Dialog.Root>

<Dialog.Root bind:open={editorOpen}
  ><Dialog.Content
    class={readOnly
      ? "flex max-h-[86dvh] flex-col gap-0 overflow-hidden p-0 sm:max-w-[min(920px,88vw)]"
      : "flex max-h-[86dvh] flex-col gap-0 overflow-hidden p-0 sm:max-w-[min(760px,88vw)]"}
    ><Dialog.Header class="shrink-0 border-b px-6 py-5"
      ><Dialog.Title>{readOnly ? "模型详情" : "编辑模型"}</Dialog.Title
      ><Dialog.Description class="sr-only"
        >{draft?.modelId} · {draft?.providerName} · 来源同步 {dateLabel(
          draft?.syncedAt ?? "",
        )}</Dialog.Description
      ></Dialog.Header
    >
    {#if draft && readOnly}
      <div class="scroll-quiet min-h-0 overflow-y-auto p-6">
        <ModelDetails model={draft} />
      </div>
      <Dialog.Footer class="shrink-0 border-t px-6 py-3.5"
        ><Button variant="outline" onclick={() => (editorOpen = false)}
          >关闭</Button
        ></Dialog.Footer
      >
    {:else if draft}<div class="flex min-h-0 flex-col">
        <ModelEditor
          bind:model={draft}
          {busy}
          error={dialogError}
          onsave={save}
          onrestore={restore}
          onclose={() => (editorOpen = false)}
        />
      </div>{/if}
  </Dialog.Content></Dialog.Root
>
<Dialog.Root bind:open={deleteOpen}
  ><Dialog.Content
    ><Dialog.Header
      ><Dialog.Title>删除模型？</Dialog.Title><Dialog.Description
        >将删除 {selected?.name} 的本地模型配置。取消则保持不变。</Dialog.Description
      ></Dialog.Header
    >{#if dialogError}<Alert.Root variant="destructive"><Alert.Description>
        {dialogError}
      </Alert.Description></Alert.Root>{/if}<Dialog.Footer
      ><Button variant="outline" onclick={() => (deleteOpen = false)}
        >取消</Button
      ><Button
        variant="destructive"
        disabled={busy}
        onclick={() => void remove()}>确认删除</Button
      ></Dialog.Footer
    ></Dialog.Content
  ></Dialog.Root
>
