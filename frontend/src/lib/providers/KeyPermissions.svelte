<script lang="ts">
  import { onMount } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Input } from "$lib/components/ui/input";
  import * as Dialog from "$lib/components/ui/dialog";
  import { VelaService, type ProviderKey, type ProviderModel, type ProviderModelPermissions } from "$lib/generated/mygo";
  /** 权限模式与集合分开保存，指定零个与全部不混同。 */
  let { item, models, onclose, onsaved }: {item: ProviderKey; models: ProviderModel[]; onclose: () => void; onsaved: () => Promise<void>} = $props();
  let permission = $state<ProviderModelPermissions>({allModels: true, modelIds: []});
  let original = $state(""), query = $state(""), error = $state("");
  let loading = $state(true), busy = $state(false), discard = $state(false);
  /** 原始模式参与变更比较，允许从指定零个主动切换为全部。 */
  const changed = $derived(JSON.stringify(permission) !== original);
  const filtered = $derived(models.filter((model) => `${model.name} ${model.modelName}`.toLowerCase().includes(query.toLowerCase())));
  onMount(async () => { try {permission = await VelaService.providerKeyModelPermissions(item.id); original = JSON.stringify(permission);} catch(cause) {error=String(cause);} finally {loading=false;} });
  /** 主动勾选产生指定集合，主动取消最后一项按旧版变为全部。 */
  function toggle(id: string, value: boolean): void {
    const next = value ? [...new Set([...permission.modelIds,id])] : permission.modelIds.filter((item) => item!==id);
    permission={allModels: next.length===0, modelIds:next};
  }
  /** 与后端归属约束一致，只保存当前 Key 的权限。 */
  async function save(): Promise<void> { busy=true;error="";try {await VelaService.saveProviderKeyModelPermissions(item.id,permission);await onsaved();onclose();}catch(cause){error=String(cause);}finally{busy=false;} }
  /** 所有退出入口统一确认未保存改动。 */
  function close(): void {if(busy)return;if(changed)discard=true;else onclose();}
</script>
<Dialog.Root open onOpenChange={(value)=>{if(!value)close();}}><Dialog.Content class="provider-form-dialog" showCloseButton={!busy}><Dialog.Header><Dialog.Title>模型权限 - {item.name}</Dialog.Title><Dialog.Description>不选任何模型保存表示全部模型；关联被清理后的指定零个模式保持无权限。</Dialog.Description></Dialog.Header>
  {#if loading}<p class="text-sm text-muted-foreground">正在读取权限…</p>{:else}
    <div class="min-h-0 overflow-auto"><div class="mb-3 flex items-center justify-between gap-2"><span class="text-xs text-primary">{permission.allModels?"全部模型":`已选 ${permission.modelIds.length} 个`}</span><Button size="sm" variant="ghost" onclick={()=>permission={allModels:true,modelIds:[]}}>使用全部模型</Button></div><Input aria-label="搜索 Key 模型权限" bind:value={query} placeholder="搜索模型名称或 ID" />
      <div class="my-3 flex gap-2"><Button size="sm" variant="ghost" onclick={()=>permission={allModels:false,modelIds:models.map((model)=>model.modelId)}} disabled={!models.length}>全选</Button><Button size="sm" variant="ghost" onclick={()=>permission={allModels:true,modelIds:[]}}>取消全选</Button></div>
      <div class="provider-selection">{#each filtered as model}<label class="provider-check-row"><Checkbox checked={permission.modelIds.includes(model.modelId)} onCheckedChange={(value)=>toggle(model.modelId,value===true)}/><span><span class="block">{model.name}</span><code class="text-[11px] text-muted-foreground">{model.modelName}</code></span></label>{:else}<p class="p-5 text-xs text-muted-foreground">没有可选择的关联模型。</p>{/each}</div>
    </div>
  {/if}
  {#if error}<p class="provider-error" role="alert">{error}</p>{/if}<Dialog.Footer><Button variant="outline" disabled={busy} onclick={close}>取消</Button><Button disabled={!changed||busy||loading||!original} onclick={save}>保存权限</Button></Dialog.Footer>
</Dialog.Content></Dialog.Root>
<Dialog.Root open={discard} onOpenChange={(value)=>discard=value}><Dialog.Content><Dialog.Header><Dialog.Title>放弃权限修改？</Dialog.Title><Dialog.Description>未保存的权限不会生效。</Dialog.Description></Dialog.Header><Dialog.Footer><Button variant="outline" onclick={()=>discard=false}>继续编辑</Button><Button variant="destructive" onclick={onclose}>放弃修改</Button></Dialog.Footer></Dialog.Content></Dialog.Root>
