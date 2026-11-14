<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { onMount } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import ModelSelection from "./ModelSelection.svelte";
  import * as Dialog from "$lib/components/ui/dialog";
  import { VelaService, type ProviderKey, type ProviderModel, type ProviderModelPermissions } from "$lib/generated/mygo";
  /** 权限模式与集合分开保存，指定零个与全部不混同。 */
  let { item, models, onclose, onsaved }: {item: ProviderKey; models: ProviderModel[]; onclose: () => void; onsaved: () => Promise<void>} = $props();
  let permission = $state<ProviderModelPermissions>({allModels: true, modelIds: []});
  let original = $state(""), error = $state("");
  let loading = $state(true), busy = $state(false), discard = $state(false);
  /** 原本指定的模型全部被移除时，单独解释为何当前仍无权限。 */
  let removedSelection=$state(false);
  /** 原始模式参与变更比较，允许从指定零个主动切换为全部。 */
  const changed = $derived(JSON.stringify(permission) !== original);
  onMount(async () => { try {permission = await VelaService.providerKeyModelPermissions(item.id); original = JSON.stringify(permission);removedSelection=!permission.allModels&&permission.modelIds.length===0;} catch(cause) {error=String(cause);} finally {loading=false;} });
  /** 主动勾选产生指定集合，主动取消最后一项按旧版变为全部。 */
  function toggle(id: string, value: boolean): void {
    const next = value ? [...new Set([...permission.modelIds,id])] : permission.modelIds.filter((item) => item!==id);
    permission={allModels: next.length===0, modelIds:next};
  }
  /** 全选为明确指定当前全部模型；取消全选沿用原规则，允许全部当前及后续模型。 */
  function selectAll(checked: boolean): void {permission=checked?{allModels:false,modelIds:models.map((model)=>model.modelId)}:{allModels:true,modelIds:[]};}
  /** 与后端归属约束一致，只保存当前 Key 的权限。 */
  async function save(): Promise<void> { busy=true;error="";try {await VelaService.saveProviderKeyModelPermissions(item.id,permission);await onsaved();onclose();}catch(cause){error=String(cause);}finally{busy=false;} }
  /** 所有退出入口统一确认未保存改动。 */
  function close(): void {if(busy)return;if(changed)discard=true;else onclose();}
</script>
<Dialog.Root open onOpenChange={(value)=>{if(!value)close();}}><Dialog.Content class="provider-form-dialog" showCloseButton={!busy}><Dialog.Header><Dialog.Title>模型权限 · {item.name}</Dialog.Title><Dialog.Description>未选择模型时允许全部；选择后，仅允许所选模型。</Dialog.Description></Dialog.Header>
  {#if loading}<p class="text-sm text-muted-foreground">正在读取权限…</p>{:else}
    <div class="provider-editor-body">
      {#if removedSelection&&!changed}<p class="mb-3 text-xs text-muted-foreground">之前选择的模型已被移除，这把 Key 暂时不能使用任何模型。请重新选择允许使用的模型。</p>{/if}
      <ModelSelection models={models.map((model) => ({id: model.modelId, name: model.name, modelName: model.modelName}))} selected={permission.modelIds} disabled={busy || !original} emptyText="暂无关联模型" onselect={toggle} onselectall={selectAll} />
    </div>
  {/if}
  {#if error}<Alert.Root variant="inline-error"><Alert.Description>{error}</Alert.Description></Alert.Root>{/if}<Dialog.Footer><Button variant="outline" disabled={busy} onclick={close}>取消</Button><Button disabled={!changed||busy||loading||!original} onclick={save}>{busy ? "保存中…" : "保存更改"}</Button></Dialog.Footer>
</Dialog.Content></Dialog.Root>
<Dialog.Root open={discard} onOpenChange={(value)=>discard=value}><Dialog.Content><Dialog.Header><Dialog.Title>放弃权限修改？</Dialog.Title><Dialog.Description>未保存的权限不会生效。</Dialog.Description></Dialog.Header><Dialog.Footer><Button variant="outline" onclick={()=>discard=false}>继续编辑</Button><Button variant="destructive" onclick={onclose}>放弃修改</Button></Dialog.Footer></Dialog.Content></Dialog.Root>
