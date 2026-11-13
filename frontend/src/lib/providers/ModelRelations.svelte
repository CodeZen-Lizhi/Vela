<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import * as Empty from "$lib/components/ui/empty";
  import { mergeProps } from "bits-ui";
  import { Hint } from "$lib/components/ui/tooltip";
  import { Plus, Copy, Pencil, Trash2, ChevronRight, X, Activity } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import ModelSelection from "./ModelSelection.svelte";
  import MappingPriorityList from "./MappingPriorityList.svelte";
  import ProviderModelTest from "./ProviderModelTest.svelte";
  import { Switch } from "$lib/components/ui/switch";
  import * as Dialog from "$lib/components/ui/dialog";
  import Choice from "./Choice.svelte";
  import { VelaService, type Model, type ProviderModel, type ProviderModelMappingsDraft, type ProviderModelsDraft } from "$lib/generated/mygo";
  /** 关联与映射由同一供应商拥有；页面只保存真实后端配置。 */
  let { providerID, models, mappingTab, testResults = [], testResultsError = "", reload, notify }: {
    providerID: string; models: ProviderModel[]; mappingTab: boolean;
    /** 后端最近结果及独立读取错误；读取失败不影响模型配置。 */
    testResults?: Awaited<ReturnType<typeof VelaService.providerModelTestResults>>; testResultsError?: string;
    reload: () => Promise<void>; notify: (message: string) => void;
  } = $props();
  /** 弹窗与删除确认各持有自己的草稿，父抽屉保持挂载。 */
  let dialog = $state<"associate" | "mapping" | "">("");
  let deleting = $state<ProviderModel | null>(null);
  /** 行内测试固定模型，关闭后保留父层最近结果。 */
  let testing = $state<ProviderModel | null>(null);
  /** 行内解除关联的确认目标，独立于映射删除及编辑草稿。 */
  let unlinking = $state<ProviderModel | null>(null);
  let discard = $state(false), busy = $state(false), error = $state("");
  let allModels = $state<Model[]>([]), selected = $state<string[]>([]);
  let query = $state("");
  let mappingScope = $state("all"), hidden = $state<string[]>([]);
  let drafts = $state<ProviderModelMappingsDraft[]>([]), original = $state("");
  /** 展开项只影响展示，不会写入持久化配置。 */
  let expanded = $state<string[]>([]);
  /** 关联模式以集合比较，映射模式以完整有序草稿比较。 */
  const changed = $derived(dialog === "associate"
    ? JSON.stringify([...selected].sort()) !== original
    : JSON.stringify(drafts) !== original);
  const modifiedCount = $derived(drafts.filter((draft) => JSON.stringify(draft.mappings) !== JSON.stringify(models.find((model) => model.modelId === draft.modelId)?.mappings ?? [])).length);
  const mappingRows = $derived(drafts.filter((draft) => {
    const model = models.find((item) => item.modelId === draft.modelId);
    const modified = JSON.stringify(draft.mappings) !== JSON.stringify(model?.mappings ?? []);
    return !hidden.includes(draft.modelId) && `${model?.name} ${model?.modelName} ${draft.mappings.map((item) => item.requestName).join(" ")}`.toLowerCase().includes(query.toLowerCase()) && (mappingScope === "all" || mappingScope === "mapped" && draft.mappings.length > 0 || mappingScope === "unmapped" && !draft.mappings.length || mappingScope === "modified" && modified);
  }));
  /** 最近结果使用服务端完成时间，悬停显示完整时间；缺少结果不推断失败。 */
  function testStatus(modelID: string): string {
    const result = testResults.find((item) => item.modelId === modelID);
    if (!result) return testResultsError ? "测试记录读取失败" : "尚未测试";
    const state = result.state === "success" ? "成功" : result.state === "failed" ? "失败" : "已取消";
    return `${state} · ${new Date(result.completedAt).toLocaleTimeString("zh-CN", {hour:"2-digit", minute:"2-digit", hour12:false})}`;
  }
  /** 返回可读错误，同时保留弹窗中的未保存草稿。 */
  function message(cause: unknown): string { return cause instanceof Error ? cause.message : String(cause); }
  /** 打开关联管理只读取本地统一模型，不发现上游模型。 */
  async function associate(): Promise<void> {
    error = "";
    try {
      allModels = await VelaService.listModels(); selected = models.map((model) => model.modelId);
      original = JSON.stringify([...selected].sort()); query = ""; dialog = "associate";
    } catch (cause) { notify(message(cause)); }
  }
  /** 工作台读取当前已保存映射；定位只改变搜索，不排除其他行的草稿。 */
  function editMappings(model?: ProviderModel): void {
    drafts = models.map((item) => ({modelId: item.modelId, mappings: item.mappings.map((mapping) => ({...mapping}))}));
    original = JSON.stringify(drafts); query = model?.modelName ?? ""; mappingScope = "all"; hidden = []; error = ""; dialog = "mapping";
  }
  /** 全选或取消全部统一模型的关联草稿，保存前不改变已有关联。 */
  function selectAll(checked: boolean): void {
    selected = checked ? allModels.map((model) => model.id) : [];
  }
  /** 逐项更新选中集合，保持稳定身份而不是使用展示名称。 */
  function selectModel(id: string, checked: boolean): void { selected = checked ? [...selected, id] : selected.filter((item) => item !== id); }
  /** 保存前校验父供应商及所有目标，实际原子性由 Go 事务保证。 */
  async function save(): Promise<void> {
    busy = true; error = "";
    try {
      if (dialog === "associate") {
        const draft: ProviderModelsDraft = {modelIds: selected};
        await VelaService.saveProviderModels(providerID, draft);
      } else await VelaService.saveProviderModelMappings(providerID, drafts);
      dialog = ""; await reload(); notify("已保存模型配置");
    } catch (cause) { error = message(cause); } finally { busy = false; }
  }
  /** 未保存时，关闭、取消、遮罩与 Esc 都进入同一放弃确认。 */
  function close(): void { if (busy) return; if (changed) discard = true; else dialog = ""; }
  /** 模型启停即时保存，不移除关联、权限或映射。 */
  async function setEnabled(model: ProviderModel, value: boolean): Promise<void> {
    busy = true;
    try { await VelaService.setProviderModelEnabled(providerID, model.modelId, value); await reload(); }
    catch (cause) { notify(message(cause)); } finally { busy = false; }
  }
  /** 快捷解除关联复用完整关联保存入口，与关联弹窗取消勾选的清理语义一致。 */
  async function unlinkModel(): Promise<void> {
    if (!unlinking || busy) return;
    busy = true; error = "";
    try {
      await VelaService.saveProviderModels(providerID, {modelIds: models.filter((model) => model.modelId !== unlinking?.modelId).map((model) => model.modelId)});
      unlinking = null;
      await reload();
      notify("已解除模型关联");
    } catch (cause) { error = message(cause); } finally { busy = false; }
  }
  /** 展开列表直接保存当前模型映射；重读成功后同步主行数量，错误交给列表回退并提示。 */
  async function saveInlineMappings(model: ProviderModel, mappings: ProviderModelMappingsDraft["mappings"]): Promise<void> {
    busy = true;
    try {
      await VelaService.saveProviderModelMappings(providerID, [{modelId:model.modelId,mappings}]);
      await reload();
    } finally { busy = false; }
  }
  /** 新增一个空请求名，保存时由后端拒绝未填完整的映射。 */
  function addMapping(draft: ProviderModelMappingsDraft): void { draft.mappings.push({requestName: "", priority: draft.mappings.length + 1}); }
  /** 删除模型全部映射后恢复默认请求名，关联保持。 */
  async function deleteMappings(): Promise<void> {
    if (!deleting) return; busy = true; error = "";
    try { await VelaService.saveProviderModelMappings(providerID, [{modelId: deleting.modelId, mappings: []}]); deleting = null; await reload(); notify("已删除映射，恢复默认请求名"); }
    catch (cause) { error = message(cause); } finally { busy = false; }
  }
  /** 模型 ID 复制使用桌面剪贴板，不生成业务数据。 */
  async function copy(value: string): Promise<void> { try { await VelaService.copyText(value); notify("已复制模型 ID"); } catch (cause) { notify(message(cause)); } }
</script>
<div class="provider-section-heading"><h3>{mappingTab ? "模型映射" : "模型列表"}</h3><Button variant="outline" size="sm" onclick={() => mappingTab ? editMappings() : associate()}><Plus data-icon="inline-start" />{mappingTab ? "管理映射" : "关联模型"}</Button></div>
<div class="provider-surface provider-model-list">
  {#each models as model (model.modelId)}
    <div class="provider-model-row">
      {#if mappingTab}<Button size="icon-xs" variant="ghost" aria-label={`${expanded.includes(model.modelId) ? "收起" : "展开"} ${model.name} 的映射`} aria-expanded={expanded.includes(model.modelId)} onclick={() => expanded = expanded.includes(model.modelId) ? expanded.filter((id) => id !== model.modelId) : [...expanded, model.modelId]}><ChevronRight class={expanded.includes(model.modelId) ? "rotate-90" : ""} /></Button>{/if}
      <div class="min-w-0 flex-1"><p class="text-xs font-medium">{model.name}</p><div class="flex items-center gap-1"><code>{model.modelName}</code>{#if !mappingTab}<Button size="icon-xs" variant="ghost" aria-label={`复制 ${model.modelName}`} onclick={() => copy(model.modelName)}><Copy /></Button>{/if}</div></div>
      {#if mappingTab}<span class="text-xs text-muted-foreground">{model.mappings.length} 个映射</span><Button size="icon-xs" variant="ghost" aria-label={`编辑 ${model.name} 映射`} onclick={() => editMappings(model)}><Pencil /></Button><Button size="icon-xs" variant="ghost" disabled={!model.mappings.length} aria-label={`删除 ${model.name} 映射`} onclick={() => { deleting = model; error = ""; }}><Trash2 /></Button>
      {:else}{@const testResult = testResults.find((item) => item.modelId === model.modelId)}<Hint text={testResult ? new Date(testResult.completedAt).toLocaleString("zh-CN", {hour12:false}) : testResultsError || undefined}>{#snippet children(hintProps0)}<span {...hintProps0} class={`shrink-0 text-[11px] ${testResult?.state === "success" ? "text-primary" : testResult?.state === "failed" ? "text-destructive" : "text-muted-foreground"}`} >{testStatus(model.modelId)}</span>{/snippet}</Hint><Hint text="模型连通测试">{#snippet children(hintProps1)}<Button {...mergeProps(hintProps1, { onclick: () => testing = model })} variant="ghost" size="icon-xs" disabled={busy}  aria-label={`测试 ${model.name} 连通性`} ><Activity /></Button>{/snippet}</Hint><Switch checked={model.enabled} disabled={busy} aria-label={`${model.enabled ? "停用" : "启用"}模型 ${model.name}`} onCheckedChange={(value) => setEnabled(model, value)} /><Hint text="解除模型关联">{#snippet children(hintProps2)}<Button {...mergeProps(hintProps2, { onclick: () => { unlinking = model; error = ""; } })} variant="ghost" size="icon-xs" disabled={busy}  aria-label={`解除 ${model.name} 的关联`} ><Trash2 /></Button>{/snippet}</Hint>{/if}
    </div>
    {#if mappingTab && expanded.includes(model.modelId)}
      <div class="border-t bg-muted/30 px-4 py-3">
        <MappingPriorityList mappings={model.mappings} disabled={busy} oncommit={(mappings) => saveInlineMappings(model,mappings)} />
        {#if !model.mappings.length}<p class="py-2 text-xs text-muted-foreground">默认请求名：{model.modelName}</p>{/if}
      </div>
    {/if}
  {:else}<Empty.Root>尚未关联模型。请先在模型页创建统一模型，再在此关联。</Empty.Root>{/each}
</div>
<Dialog.Root open={dialog !== ""} onOpenChange={(value) => { if (!value) close(); }}>
  <Dialog.Content class="provider-form-dialog" showCloseButton={!busy}>
    <Dialog.Header><Dialog.Title>{dialog === "associate" ? "关联模型" : "管理模型映射"}</Dialog.Title><Dialog.Description>{dialog === "associate" ? "勾选表示关联，取消勾选将清理对应映射和指定 Key 权限。" : "维护上游请求名，拖动调整优先级，统一保存后生效。"}</Dialog.Description></Dialog.Header>
    <div class={dialog === "associate" ? "provider-editor-body" : "min-h-0 overflow-auto"}>
      {#if dialog === "mapping"}<div class="provider-tools provider-mapping-filters mb-4"><Input aria-label="搜索模型映射" bind:value={query} placeholder="搜索模型名称、ID或请求名" />
        <Choice bind:value={mappingScope} label="映射筛选" options={[{value:"all",label:"全部模型"},{value:"mapped",label:"已映射"},{value:"unmapped",label:"未映射"},{value:"modified",label:"已修改"}]} />
      </div>{/if}
      {#if dialog === "associate"}
        <ModelSelection models={allModels.map((model) => ({id: model.id, name: model.name, modelName: model.modelId, status: selected.includes(model.id) && !models.some((item) => item.modelId === model.id) ? "将添加" : !selected.includes(model.id) && models.some((item) => item.modelId === model.id) ? "将移除" : models.some((item) => item.modelId === model.id) ? "已关联" : ""}))} {selected} disabled={busy} emptyText="尚无统一模型，请先在模型页创建。" onselect={selectModel} onselectall={selectAll} />
      {:else}
        <div class="mb-3 flex flex-wrap items-center gap-2 text-xs text-muted-foreground"><span>{modifiedCount} 个模型有改动</span><Button variant="ghost" size="sm" onclick={() => drafts = JSON.parse(original)}>重置草稿</Button>{#if hidden.length}<Button variant="ghost" size="sm" onclick={() => hidden = []}>恢复隐藏的 {hidden.length} 个模型</Button>{/if}</div>
        {#each mappingRows as draft (draft.modelId)}{@const model = models.find((item) => item.modelId === draft.modelId)!}
          <section class="mb-4 rounded-lg border p-4"><header class="mb-3 flex items-center gap-2"><div class="min-w-0 flex-1"><h4 class="text-xs font-semibold">{model.name}</h4><code class="text-[11px] text-muted-foreground">{model.modelName}</code></div><Button variant="ghost" size="icon-xs" aria-label={`暂时隐藏 ${model.name}`} onclick={() => hidden = [...hidden, draft.modelId]}><X /></Button></header>
            <MappingPriorityList mappings={draft.mappings} disabled={busy} editable oncommit={async (mappings) => {draft.mappings = mappings;}} />
            <Button variant="outline" size="sm" onclick={() => addMapping(draft)}><Plus data-icon="inline-start" />添加请求名</Button>
          </section>
        {:else}<p class="py-6 text-center text-xs text-muted-foreground">没有符合条件的模型。</p>{/each}
      {/if}
      {#if error}<Alert.Root variant="inline-error"><Alert.Description>{error}</Alert.Description></Alert.Root>{/if}
    </div>
    <Dialog.Footer><Button variant="outline" disabled={busy} onclick={close}>取消</Button><Button disabled={!changed || busy} onclick={save}>{busy ? "保存中…" : "保存更改"}</Button></Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
<Dialog.Root open={discard} onOpenChange={(value) => discard = value}><Dialog.Content><Dialog.Header><Dialog.Title>放弃未保存的修改？</Dialog.Title><Dialog.Description>当前草稿尚未保存，放弃后原有配置保持不变。</Dialog.Description></Dialog.Header><Dialog.Footer><Button variant="outline" onclick={() => discard = false}>继续编辑</Button><Button variant="destructive" onclick={() => {discard=false;dialog="";}}>放弃修改</Button></Dialog.Footer></Dialog.Content></Dialog.Root>
<Dialog.Root open={deleting !== null} onOpenChange={(value) => {if (!busy && !value) deleting=null;}}><Dialog.Content><Dialog.Header><Dialog.Title>删除模型映射？</Dialog.Title><Dialog.Description>将删除 {deleting?.mappings.map((mapping) => mapping.requestName).join("、")}，并恢复默认请求名，保留模型关联。</Dialog.Description></Dialog.Header>{#if error}<Alert.Root variant="inline-error"><Alert.Description>{error}</Alert.Description></Alert.Root>{/if}<Dialog.Footer><Button variant="outline" disabled={busy} onclick={() => deleting=null}>取消</Button><Button variant="destructive" disabled={busy} onclick={deleteMappings}>确认删除</Button></Dialog.Footer></Dialog.Content></Dialog.Root>

<Dialog.Root open={unlinking !== null} onOpenChange={(value) => {if (!busy && !value) unlinking = null;}}>
  <Dialog.Content showCloseButton={!busy} interactOutsideBehavior={busy ? "ignore" : "close"} escapeKeydownBehavior={busy ? "ignore" : "close"}>
    <Dialog.Header><Dialog.Title>解除模型关联？</Dialog.Title><Dialog.Description>将解除“{unlinking?.name}”与当前供应商的关联，并清理对应映射和指定 Key 权限。统一模型本身保留。</Dialog.Description></Dialog.Header>
    {#if error}<Alert.Root variant="inline-error"><Alert.Description>{error}</Alert.Description></Alert.Root>{/if}
    <Dialog.Footer><Button variant="outline" disabled={busy} onclick={() => unlinking = null}>取消</Button><Button variant="destructive" disabled={busy} onclick={unlinkModel}>{busy ? "解除中…" : "确认解除"}</Button></Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

{#if testing}<ProviderModelTest providerID={providerID} model={testing} previous={testResults.find((item) => item.modelId === testing?.modelId) ?? null} onclose={() => testing = null} oncompleted={reload}/>{/if}
