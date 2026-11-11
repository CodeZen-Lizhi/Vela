<script lang="ts">
  import { onMount } from "svelte";
  import { Plus, Pencil, Copy, Trash2, ShieldCheck, ExternalLink } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Switch } from "$lib/components/ui/switch";
  import * as Dialog from "$lib/components/ui/dialog";
  import { VelaService, type Provider, type ProviderDefaults, type ProviderKey, type ProviderKeyDraft, type ProviderModel, type ProviderKeyQueryDraft, type ProviderModelPermissions } from "$lib/generated/mygo";
  import Choice from "./Choice.svelte";
  import KeyForm from "./KeyForm.svelte";
  import ModelRelations from "./ModelRelations.svelte";
  import KeyPermissions from "./KeyPermissions.svelte";
  /** 单个供应商详情始终附着在列表上方，数据刷新不关闭抽屉。 */
  let { provider, defaults, onupdated, notify }: {provider:Provider;defaults:ProviderDefaults;onupdated:()=>Promise<void>;notify:(message:string)=>void}=$props();
  let tab=$state("keys"), query=$state(""), status=$state("all");
  let keys=$state<ProviderKey[]>([]), models=$state<ProviderModel[]>([]);
  let permissions=$state<Record<string,ProviderModelPermissions>>({});
  let loading=$state(true), busy=$state(false), error=$state("");
  let editorOpen=$state(false), selected=$state<ProviderKey|null>(null), deleting=$state<ProviderKey|null>(null), permissionKey=$state<ProviderKey|null>(null);
  /** 冷却状态在网关未接入前为空，不使用原型模拟资源状态。 */
  const filtered=$derived(keys.filter((key)=>(status==="all"||status==="enabled"&&key.enabled||status==="disabled"&&!key.enabled)&&key.name.toLowerCase().includes(query.toLowerCase())));
  const nextPriority=$derived(keys.reduce((value,key)=>Math.max(value,key.priority),0)+1);
  /** 读取三个页签的数据和指定权限计数；失败保留已有数据。 */
  async function load():Promise<void>{
    error="";
    try{const [nextKeys,nextModels]=await Promise.all([VelaService.providerKeys(provider.id),VelaService.providerModels(provider.id)]);keys=nextKeys;models=nextModels;const entries=await Promise.all(keys.filter((key)=>!key.allModels).map(async(key)=>[key.id,await VelaService.providerKeyModelPermissions(key.id)] as const));permissions=Object.fromEntries(entries);}
    catch(cause){error=cause instanceof Error?cause.message:String(cause);}finally{loading=false;}
  }
  onMount(load);
  /** 子功能保存后刷新抽屉与列表摘要，不切换当前页签。 */
  async function updated():Promise<void>{await load();await onupdated();}
  /** Key 基本配置保存后，只触发这一把 Key 的已配置上游来源同步。 */
  async function saveKey(draft:ProviderKeyDraft, queryDraft:ProviderKeyQueryDraft):Promise<void>{
    busy=true;error="";
    try {
      const result=await VelaService.saveProviderKey(provider.id,selected?.id??"",draft,queryDraft);
      editorOpen=false;await updated();
      if(result.nameStatus.state==="failed"||result.multiplierStatus.state==="failed")notify("Key 已保存，部分上游信息查询失败，可刷新重试");else notify("Key 已保存");
    }catch(cause){error=cause instanceof Error?cause.message:String(cause);}
    finally{busy=false;}
  }
  /** 启停即时落库，删除则由独立确认框完成。 */
  async function setEnabled(key:ProviderKey,value:boolean):Promise<void>{busy=true;try{await VelaService.setProviderKeyEnabled(key.id,value);await updated();notify(value?"Key 已启用":"Key 已停用");}catch(cause){notify(String(cause));}finally{busy=false;}}
  /** 删除真实凭据、权限及查询配置，失败保留确认框。 */
  async function deleteKey():Promise<void>{if(!deleting)return;busy=true;error="";try{await VelaService.deleteProviderKey(deleting.id);deleting=null;await updated();notify("Key 已删除");}catch(cause){error=String(cause);}finally{busy=false;}}
  /** 完整密钥从后端直接写剪贴板，页面只接收操作结果。 */
  async function copy(key:ProviderKey):Promise<void>{try{await VelaService.copyProviderKeySecret(key.id);notify("已复制完整密钥");}catch(cause){notify(String(cause));}}
</script>
<header class="provider-drawer-heading"><div class="provider-identity"><span class="provider-avatar">{provider.name.slice(0,1).toUpperCase()}</span><Dialog.Title>{provider.name}</Dialog.Title>{#if provider.website}<a href={provider.website} target="_blank" rel="noreferrer" aria-label={`打开 ${provider.name} 主站`}><ExternalLink class="size-3.5 text-muted-foreground"/></a>{/if}</div><Dialog.Description class="sr-only">管理供应商密钥、关联模型与模型映射。</Dialog.Description></header>
<div class="provider-tabs" role="tablist" aria-label="供应商详情页签">{#each [{id:"keys",label:"密钥管理",count:keys.length},{id:"models",label:"模型列表",count:models.length},{id:"mappings",label:"模型映射",count:models.filter((model)=>model.mappings.length).length}] as item}<button role="tab" aria-selected={tab===item.id} onclick={()=>tab=item.id}>{item.label}<small>{item.count}</small></button>{/each}</div>
<div class="provider-drawer-body">
  {#if error&&!editorOpen&&!deleting}<p class="provider-error" role="alert">{error}<Button size="sm" variant="ghost" onclick={load}>重试</Button></p>{/if}
  {#if loading}<p class="text-xs text-muted-foreground">正在读取供应商配置…</p>{:else if tab==="keys"}
    <div class="provider-section-heading"><div class="provider-tools"><Input class="max-w-56" aria-label="搜索 Key" placeholder="搜索 Key 名称" bind:value={query}/><Choice bind:value={status} label="Key 状态" options={[{value:"all",label:"全部状态"},{value:"enabled",label:"已启用"},{value:"disabled",label:"已停用"},{value:"cooling",label:"冷却中"}]}/></div><Button size="sm" onclick={()=>{selected=null;error="";editorOpen=true;}}><Plus data-icon="inline-start"/>添加 Key</Button></div>
    <div class="provider-surface"><table class="provider-table"><thead><tr><th>Key</th><th>倍率</th><th>模型权限</th><th>健康度</th><th>操作</th></tr></thead><tbody>{#each filtered as key(key.id)}<tr data-disabled={!key.enabled}>
      <td class="provider-main" data-label="Key"><p class="text-xs font-semibold">{key.name}</p><div class="mt-1 flex items-center gap-1 text-[11px] text-muted-foreground"><code>{key.secretMask||"未设置"}</code>{#if key.hasSecret}<Button size="icon-xs" variant="ghost" aria-label={`复制 ${key.name} 完整密钥`} onclick={()=>copy(key)}><Copy/></Button>{/if}</div></td>
      <td data-label="倍率"><span>{key.multiplier===null?"倍率未知":`${key.multiplier}×`}</span></td>
      <td data-label="模型权限">{key.allModels?"全部模型":`已选 ${permissions[key.id]?.modelIds.length??0} 个`}</td>
      <td data-label="健康度"><div class="provider-health-bar"><span style={`width:${key.healthScore*10}%`}></span></div><small class="mt-1 block text-[10px] text-muted-foreground">{key.healthScore.toFixed(1)}/10</small></td>
      <td data-label="操作"><div class="provider-actions"><Button size="icon-xs" variant="ghost" title="模型权限" aria-label={`${key.name} 模型权限`} onclick={()=>permissionKey=key}><ShieldCheck/></Button><Button size="icon-xs" variant="ghost" title="编辑" aria-label={`编辑 Key ${key.name}`} onclick={()=>{selected=key;error="";editorOpen=true;}}><Pencil/></Button><Switch checked={key.enabled} disabled={busy} aria-label={`${key.enabled?"停用":"启用"} Key ${key.name}`} onCheckedChange={(value)=>setEnabled(key,value)}/><Button size="icon-xs" variant="ghost" title="删除" aria-label={`删除 Key ${key.name}`} onclick={()=>{deleting=key;error="";}}><Trash2/></Button></div></td>
    </tr>{/each}</tbody></table>{#if !filtered.length}<div class="provider-empty">{keys.length?"没有符合条件的 Key":"尚无 Key，添加第一把 Key 开始配置。"}{#if keys.length}<Button variant="outline" size="sm" onclick={()=>{query="";status="all";}}>清空筛选</Button>{/if}</div>{/if}</div>
  {:else}<ModelRelations providerID={provider.id} {models} {keys} mappingTab={tab==="mappings"} reload={updated} {notify}/>{/if}
</div>
<Dialog.Root open={editorOpen} onOpenChange={(value)=>{if(!busy)editorOpen=value;}}><Dialog.Content class="provider-form-dialog" showCloseButton={!busy} onInteractOutside={(event)=>{if(busy)event.preventDefault();}} onEscapeKeydown={(event)=>{if(busy)event.preventDefault();}}><Dialog.Header><Dialog.Title>{selected?"编辑 Key":"添加 Key"}</Dialog.Title><Dialog.Description>默认共用四种 API 格式，启停与模型权限在列表中维护。</Dialog.Description></Dialog.Header>{#if editorOpen}<KeyForm item={selected} {defaults} {nextPriority} {busy} {error} onsave={saveKey} onclose={()=>editorOpen=false}/>{/if}</Dialog.Content></Dialog.Root>
{#if permissionKey}<KeyPermissions item={permissionKey} {models} onclose={()=>permissionKey=null} onsaved={updated}/>{/if}

<Dialog.Root open={deleting!==null} onOpenChange={(value)=>{if(!busy&&!value)deleting=null;}}><Dialog.Content><Dialog.Header><Dialog.Title>删除 Key？</Dialog.Title><Dialog.Description>确认删除“{deleting?.name}”？密钥、模型权限和查询配置将一并删除，此操作不能撤销。</Dialog.Description></Dialog.Header>{#if error}<p class="provider-error" role="alert">{error}</p>{/if}<Dialog.Footer><Button variant="outline" disabled={busy} onclick={()=>deleting=null}>取消</Button><Button variant="destructive" disabled={busy} onclick={deleteKey}>确认删除</Button></Dialog.Footer></Dialog.Content></Dialog.Root>
