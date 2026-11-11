<script lang="ts">
  import { Plus, Copy, Pencil, Trash2, ChevronDown, ChevronUp, X } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Switch } from "$lib/components/ui/switch";
  import * as Dialog from "$lib/components/ui/dialog";
  import * as Field from "$lib/components/ui/field";
  import Choice from "./Choice.svelte";
  import { VelaService, type Model, type ProviderModel, type ProviderKey, type ProviderModelMappingsDraft, type ProviderModelsDraft } from "$lib/generated/mygo";
  /** 沿用旧版 request_operation：常用范围使用业务名称，未知历史范围保留。 */
  function operationLabel(operations: string[]): string {
    return !operations.length ? "全部操作" : operations.length === 1 && operations[0] === "compact" ? "仅会话压缩" : `仅匹配：${operations.join("、")}`;
  }
  /** 已存的自定义范围作为当前选项保留，常用编辑只选择全部或会话压缩。 */
  function operationOptions(operations: string[]): {value: string; label: string}[] {
    const options = [{value:"[]",label:"全部操作"},{value:'["compact"]',label:"仅会话压缩"}];
    const current = JSON.stringify(operations);
    if (!options.some((option)=>option.value===current)) options.push({value:current,label:operationLabel(operations)});
    return options;
  }
  /** 关联与映射由同一供应商拥有；页面只保存真实后端配置。 */
  let { providerID, models, keys, mappingTab, reload, notify }: {
    providerID: string; models: ProviderModel[]; keys: ProviderKey[]; mappingTab: boolean;
    reload: () => Promise<void>; notify: (message: string) => void;
  } = $props();
  /** 弹窗与删除确认各持有自己的草稿，父抽屉保持挂载。 */
  let dialog = $state<"associate" | "mapping" | "">("");
  let deleting = $state<ProviderModel | null>(null);
  let discard = $state(false), busy = $state(false), error = $state("");
  let allModels = $state<Model[]>([]), selected = $state<string[]>([]);
  let query = $state(""), kind = $state("all"), grant = $state("none"), grantKey = $state("");
  let mappingScope = $state("all"), hidden = $state<string[]>([]);
  let drafts = $state<ProviderModelMappingsDraft[]>([]), original = $state("");
  /** 展开项只影响展示，不会写入持久化配置。 */
  let expanded = $state<string[]>([]);
  /** 关联模式以集合比较，映射模式以完整有序草稿比较。 */
  const changed = $derived(dialog === "associate"
    ? JSON.stringify([...selected].sort()) !== original
    : JSON.stringify(drafts) !== original);
  const modifiedCount = $derived(drafts.filter((draft) => JSON.stringify(draft.mappings) !== JSON.stringify(models.find((model) => model.modelId === draft.modelId)?.mappings ?? [])).length);
  const associationRows = $derived(allModels.filter((model) => (kind === "all" || model.kind === kind) && `${model.name} ${model.modelId}`.toLowerCase().includes(query.toLowerCase())));
  const mappingRows = $derived(drafts.filter((draft) => {
    const model = models.find((item) => item.modelId === draft.modelId);
    const modified = JSON.stringify(draft.mappings) !== JSON.stringify(model?.mappings ?? []);
    return !hidden.includes(draft.modelId) && `${model?.name} ${model?.modelName} ${draft.mappings.map((item) => item.requestName).join(" ")}`.toLowerCase().includes(query.toLowerCase()) && (mappingScope === "all" || mappingScope === "mapped" && draft.mappings.length > 0 || mappingScope === "unmapped" && !draft.mappings.length || mappingScope === "modified" && modified);
  }));
  /** 返回可读错误，同时保留弹窗中的未保存草稿。 */
  function message(cause: unknown): string { return cause instanceof Error ? cause.message : String(cause); }
  /** 打开关联管理只读取本地统一模型，不发现上游模型。 */
  async function associate(): Promise<void> {
    error = "";
    try {
      allModels = await VelaService.listModels(); selected = models.map((model) => model.modelId);
      original = JSON.stringify([...selected].sort()); query = ""; kind = "all"; grant = "none"; grantKey = ""; dialog = "associate";
    } catch (cause) { notify(message(cause)); }
  }
  /** 工作台读取当前已保存映射；定位只改变搜索，不排除其他行的草稿。 */
  function editMappings(model?: ProviderModel): void {
    drafts = models.map((item) => ({modelId: item.modelId, mappings: item.mappings.map((mapping) => ({...mapping, operations:[...mapping.operations]}))}));
    original = JSON.stringify(drafts); query = model?.modelName ?? ""; mappingScope = "all"; hidden = []; error = ""; dialog = "mapping";
  }
  /** 主动选择取消全选关联，不影响当前筛选之外的选择。 */
  function selectVisible(checked: boolean): void {
    const visible = new Set(associationRows.map((model) => model.id));
    selected = checked ? [...new Set([...selected, ...visible])] : selected.filter((id) => !visible.has(id));
  }
  /** 逐项更新选中集合，保持稳定身份而不是使用展示名称。 */
  function selectModel(id: string, checked: boolean): void { selected = checked ? [...selected, id] : selected.filter((item) => item !== id); }
  /** 保存前校验父供应商及所有目标，实际原子性由 Go 事务保证。 */
  async function save(): Promise<void> {
    busy = true; error = "";
    try {
      if (dialog === "associate") {
        const draft: ProviderModelsDraft = {modelIds: selected, grantMode: grant as ProviderModelsDraft["grantMode"], keyIds: grant === "selected" && grantKey ? [grantKey] : []};
        if (grant === "selected" && !grantKey) throw new Error("请选择要授权的 Key");
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
  /** 上移下移调整草稿并重新编号，不在用户保存前写数据库。 */
  function move(draft: ProviderModelMappingsDraft, index: number, delta: number): void {
    const target = index + delta;
    if (target < 0 || target >= draft.mappings.length) return;
    [draft.mappings[index], draft.mappings[target]] = [draft.mappings[target]!, draft.mappings[index]!];
    draft.mappings.forEach((mapping, position) => mapping.priority = position + 1);
  }
  /** 新增一个空请求名，保存时由后端拒绝未填完整的映射。 */
  function addMapping(draft: ProviderModelMappingsDraft): void { draft.mappings.push({requestName: "", priority: draft.mappings.length + 1, operations: []}); }
  /** 删除模型全部映射后恢复默认请求名，关联保持。 */
  async function deleteMappings(): Promise<void> {
    if (!deleting) return; busy = true; error = "";
    try { await VelaService.saveProviderModelMappings(providerID, [{modelId: deleting.modelId, mappings: []}]); deleting = null; await reload(); notify("已删除映射，恢复默认请求名"); }
    catch (cause) { error = message(cause); } finally { busy = false; }
  }
  /** 模型 ID 复制使用桌面剪贴板，不生成业务数据。 */
  async function copy(value: string): Promise<void> { try { await VelaService.copyText(value); notify("已复制模型 ID"); } catch (cause) { notify(message(cause)); } }
</script>
<div class="provider-section-heading"><h3>{mappingTab ? "模型映射" : "模型列表"}</h3><Button size="sm" onclick={() => mappingTab ? editMappings() : associate()}><Plus data-icon="inline-start" />{mappingTab ? "管理映射" : "关联模型"}</Button></div>
<div class="provider-surface provider-model-list">
  {#each models as model (model.modelId)}
    <div class="provider-model-row">
      {#if mappingTab}<Button size="icon-xs" variant="ghost" aria-label={`展开 ${model.name} 的映射`} onclick={() => expanded = expanded.includes(model.modelId) ? expanded.filter((id) => id !== model.modelId) : [...expanded, model.modelId]}><ChevronDown /></Button>{/if}
      <div class="min-w-0 flex-1"><p class="text-xs font-medium">{model.name}</p><div class="flex items-center gap-1"><code>{model.modelName}</code>{#if !mappingTab}<Button size="icon-xs" variant="ghost" aria-label={`复制 ${model.modelName}`} onclick={() => copy(model.modelName)}><Copy /></Button>{/if}</div></div>
      {#if mappingTab}<span class="text-xs text-muted-foreground">{model.mappings.length} 个映射</span><Button size="icon-xs" variant="ghost" aria-label={`编辑 ${model.name} 映射`} onclick={() => editMappings(model)}><Pencil /></Button><Button size="icon-xs" variant="ghost" disabled={!model.mappings.length} aria-label={`删除 ${model.name} 映射`} onclick={() => { deleting = model; error = ""; }}><Trash2 /></Button>
      {:else}<Switch checked={model.enabled} disabled={busy} aria-label={`${model.enabled ? "停用" : "启用"}模型 ${model.name}`} onCheckedChange={(value) => setEnabled(model, value)} /><Button size="sm" variant="ghost" onclick={() => editMappings(model)}>管理映射</Button>{/if}
    </div>
    {#if mappingTab && expanded.includes(model.modelId)}<div class="border-t bg-muted/30 px-8 py-4 text-xs text-muted-foreground">{#each model.mappings as mapping}<p class="py-1">{mapping.priority}. {mapping.requestName} <span class="ml-2">{operationLabel(mapping.operations)}</span></p>{:else}<p>默认请求名：{model.modelName}</p>{/each}</div>{/if}
  {:else}<div class="provider-empty">尚未关联模型。请先在模型页创建统一模型，再在此关联。</div>{/each}
</div>
<Dialog.Root open={dialog !== ""} onOpenChange={(value) => { if (!value) close(); }}>
  <Dialog.Content class="provider-form-dialog" showCloseButton={!busy}>
    <Dialog.Header><Dialog.Title>{dialog === "associate" ? "关联模型" : "管理模型映射"}</Dialog.Title><Dialog.Description>{dialog === "associate" ? "勾选表示关联，取消勾选将清理对应映射和指定 Key 权限。" : "维护上游请求名、顺序和操作范围，统一保存后生效。"}</Dialog.Description></Dialog.Header>
    <div class="min-h-0 overflow-auto">
      <div class="provider-tools mb-4"><Input aria-label={dialog === "associate" ? "搜索关联模型" : "搜索模型映射"} bind:value={query} placeholder="搜索模型名称、ID或请求名" />
        {#if dialog === "associate"}<Choice bind:value={kind} label="模型类型" options={[{value:"all",label:"全部类型"},{value:"chat",label:"对话"},{value:"image",label:"图片"},{value:"video",label:"视频"},{value:"vector",label:"向量"}]} />
        {:else}<Choice bind:value={mappingScope} label="映射筛选" options={[{value:"all",label:"全部模型"},{value:"mapped",label:"已映射"},{value:"unmapped",label:"未映射"},{value:"modified",label:"已修改"}]} />{/if}
      </div>
      {#if dialog === "associate"}
        <div class="mb-3 flex gap-2"><Button variant="ghost" size="sm" onclick={() => selectVisible(true)}>全选当前可见项</Button><Button variant="ghost" size="sm" onclick={() => selectVisible(false)}>取消当前可见项</Button></div>
        <div class="provider-selection">{#each associationRows as model (model.id)}<label class="provider-check-row"><Checkbox checked={selected.includes(model.id)} onCheckedChange={(value) => selectModel(model.id, value === true)} /><span class="min-w-0 flex-1"><strong class="block font-medium">{model.name}</strong><code class="text-[11px] text-muted-foreground">{model.modelId}</code></span><small class="text-muted-foreground">{selected.includes(model.id) && !models.some((item) => item.modelId === model.id) ? "将添加" : !selected.includes(model.id) && models.some((item) => item.modelId === model.id) ? "将移除" : models.some((item) => item.modelId === model.id) ? "已关联" : ""}</small></label>{:else}<p class="p-5 text-xs text-muted-foreground">没有符合条件的统一模型。</p>{/each}</div>
        <Field.Group class="mt-5"><Field.Field><Field.Label>新增模型的 Key 权限</Field.Label><Choice bind:value={grant} label="新增模型授权" options={[{value:"none",label:"不修改 Key 权限"},{value:"selected",label:"指定 Key"},{value:"enabled",label:"全部启用 Key"}]} /></Field.Field>{#if grant === "selected"}<Choice bind:value={grantKey} label="授权 Key" options={keys.map((key) => ({value:key.id,label:key.name}))} />{/if}</Field.Group>
      {:else}
        <div class="mb-3 flex flex-wrap items-center gap-2 text-xs text-muted-foreground"><span>{modifiedCount} 个模型有改动</span><Button variant="ghost" size="sm" onclick={() => drafts = JSON.parse(original)}>重置草稿</Button>{#if hidden.length}<Button variant="ghost" size="sm" onclick={() => hidden = []}>恢复隐藏的 {hidden.length} 个模型</Button>{/if}</div>
        {#each mappingRows as draft (draft.modelId)}{@const model = models.find((item) => item.modelId === draft.modelId)!}
          <section class="mb-4 rounded-lg border p-4"><header class="mb-3 flex items-center gap-2"><div class="min-w-0 flex-1"><h4 class="text-xs font-semibold">{model.name}</h4><code class="text-[11px] text-muted-foreground">{model.modelName}</code></div><Button variant="ghost" size="icon-xs" aria-label={`暂时隐藏 ${model.name}`} onclick={() => hidden = [...hidden, draft.modelId]}><X /></Button></header>
            {#each draft.mappings as mapping, index}<div class="mb-3 flex items-start gap-1"><div class="min-w-0 flex-1"><Input aria-label={`${model.name} 请求名 ${index + 1}`} bind:value={mapping.requestName} placeholder="上游请求名" /><details class="mt-2 text-xs text-muted-foreground"><summary>操作范围 · {operationLabel(mapping.operations)}</summary><div class="pt-2"><Choice label={`${model.name} 操作范围 ${index + 1}`} bind:value={() => JSON.stringify(mapping.operations), (value) => mapping.operations = JSON.parse(value)} options={operationOptions(mapping.operations)}/></div></details></div><Button variant="ghost" size="icon-xs" disabled={index === 0} aria-label="上移映射" onclick={() => move(draft,index,-1)}><ChevronUp /></Button><Button variant="ghost" size="icon-xs" disabled={index === draft.mappings.length - 1} aria-label="下移映射" onclick={() => move(draft,index,1)}><ChevronDown /></Button><Button variant="ghost" size="icon-xs" aria-label="移除映射" onclick={() => {draft.mappings.splice(index,1);draft.mappings.forEach((item,i) => item.priority=i+1);}}><Trash2 /></Button></div>{/each}
            <Button variant="outline" size="sm" onclick={() => addMapping(draft)}><Plus data-icon="inline-start" />添加请求名</Button>
          </section>
        {:else}<p class="py-6 text-center text-xs text-muted-foreground">没有符合条件的模型。</p>{/each}
      {/if}
      {#if error}<p class="provider-error" role="alert">{error}</p>{/if}
    </div>
    <Dialog.Footer><Button variant="outline" disabled={busy} onclick={close}>取消</Button><Button disabled={!changed || busy} onclick={save}>{busy ? "保存中…" : "保存更改"}</Button></Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
<Dialog.Root open={discard} onOpenChange={(value) => discard = value}><Dialog.Content><Dialog.Header><Dialog.Title>放弃未保存的修改？</Dialog.Title><Dialog.Description>当前草稿尚未保存，放弃后原有配置保持不变。</Dialog.Description></Dialog.Header><Dialog.Footer><Button variant="outline" onclick={() => discard = false}>继续编辑</Button><Button variant="destructive" onclick={() => {discard=false;dialog="";}}>放弃修改</Button></Dialog.Footer></Dialog.Content></Dialog.Root>
<Dialog.Root open={deleting !== null} onOpenChange={(value) => {if (!busy && !value) deleting=null;}}><Dialog.Content><Dialog.Header><Dialog.Title>删除模型映射？</Dialog.Title><Dialog.Description>将删除 {deleting?.mappings.map((mapping) => mapping.requestName).join("、")}，并恢复默认请求名，保留模型关联。</Dialog.Description></Dialog.Header>{#if error}<p class="provider-error" role="alert">{error}</p>{/if}<Dialog.Footer><Button variant="outline" disabled={busy} onclick={() => deleting=null}>取消</Button><Button variant="destructive" disabled={busy} onclick={deleteMappings}>确认删除</Button></Dialog.Footer></Dialog.Content></Dialog.Root>
