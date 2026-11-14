<script lang="ts">
  import * as Sheet from "$lib/components/ui/sheet";
  import * as Alert from "$lib/components/ui/alert";
  import * as Empty from "$lib/components/ui/empty";
  import * as Table from "$lib/components/ui/table";
  import { Badge } from "$lib/components/ui/badge";
  import { mergeProps } from "bits-ui";
  import { Hint } from "$lib/components/ui/tooltip";
  import { onMount } from "svelte";
  import { isMyGo } from "mygo-runtime";
  import { Plus, RefreshCw, Search, SlidersHorizontal, Pencil, Wallet, Trash2, ExternalLink, ShieldAlert } from "@lucide/svelte";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import * as ToggleGroup from "$lib/components/ui/toggle-group";
  import { Button } from "$lib/components/ui/button";
  import { Switch } from "$lib/components/ui/switch";
  import * as InputGroup from "$lib/components/ui/input-group";
  import * as Dialog from "$lib/components/ui/dialog";
  import { VelaService, type ProviderOverview, type Provider, type ProviderDefaults, type ProviderDraft, type ProviderModel, type ProviderAccount, type VerificationSnapshot } from "$lib/generated/mygo";
  import Choice from "./Choice.svelte";
  import ProviderForm from "./ProviderForm.svelte";
  import ProviderDetails from "./ProviderDetails.svelte";
  import AccountForm from "./AccountForm.svelte";
  import "./providers.css";
  /** 主页面维护列表快照；详情、表单草稿与账户查询各自保有生命周期。 */
  let {notify}: {notify:(message:string)=>void}=$props();
  let rows=$state<ProviderOverview[]>([]), defaults=$state<ProviderDefaults|null>(null);
  let accounts=$state<Record<string,ProviderAccount>>({}), relations=$state<Record<string,ProviderModel[]>>({}), rowErrors=$state<Record<string,string>>({});
  /** 验证摘要只读本地最新结果，不启动检测。 */
  let verification=$state<VerificationSnapshot|null>(null);
  let accountInterval=$state<number|null>(null);
  /** 列表周期只重读本地快照，上游查询由全局账户任务负责。 */
  let listSeconds=$state(60);
  /** 账户保存及首次读取期间锁定父弹窗关闭入口。 */
  let accountBusy=$state(false);
  let query=$state(""), status=$state("all"), modelFilter=$state("all");
  let loading=$state(true), busy=$state(false), refreshing=$state(false), error=$state("");
  let editorOpen=$state(false), editing=$state<Provider|null>(null), detailID=$state(""), accountProvider=$state<Provider|null>(null), deleting=$state<Provider|null>(null), formError=$state("");
  /** 余额在列表中的可见符号、数值和完整可访问说明。 */
  type BalanceDisplay = {
    /** 货币符号；未查询到余额时为空。 */
    symbol: string;
    /** 两位小数的金额；未知余额显示破折号。 */
    amount: string;
    /** 供辅助技术和悬停提示使用的完整币种及金额。 */
    label: string;
    /** 是否存在可展示的余额数值。 */
    available: boolean;
  };
  /** 当前详情取最新快照；删除后不会留下旧抽屉。 */
  const selected=$derived(rows.find((row)=>row.provider.id===detailID)?.provider??null);
  const modelOptions=$derived([{value:"all",label:"全部关联模型"},...Array.from(new Map(Object.values(relations).flat().map((model)=>[model.modelId,{value:model.modelId,label:model.name}])).values()).sort((left,right)=>left.label.localeCompare(right.label))]);
  const filtered=$derived(rows.filter((row)=>{
    const provider=row.provider, models=relations[provider.id]??[];
    return (status==="all"||status==="enabled"&&provider.enabled||status==="disabled"&&!provider.enabled)&&(modelFilter==="all"||models.some((model)=>model.modelId===modelFilter))&&`${provider.name} ${provider.website} ${models.map((model)=>`${model.name} ${model.modelName}`).join(" ")}`.toLowerCase().includes(query.toLowerCase());
  }));
  /** 不把 RPC 错误转换为空集合，失败时保留上一份有效快照。 */
  function message(cause:unknown):string{return cause instanceof Error?cause.message:String(cause);}
  /** 进入页面和恢复窗口只读本地，不在读取动作中访问上游。 */
  async function load():Promise<void>{
    if(!isMyGo()){loading=false;return;}
    error="";
    try{
      const snapshot=await VelaService.providers();rows=snapshot.providers;defaults=snapshot.defaults;
      const nextAccounts:Record<string,ProviderAccount>={},nextRelations:Record<string,ProviderModel[]>={},nextErrors:Record<string,string>={};
      await Promise.all(rows.map(async(row)=>{
        const id=row.provider.id;
        const results=await Promise.allSettled([VelaService.providerAccount(id),VelaService.providerModels(id)]);
        if(results[0].status==="fulfilled")nextAccounts[id]=results[0].value;else nextErrors[id]=message(results[0].reason);
        if(results[1].status==="fulfilled")nextRelations[id]=results[1].value;else nextErrors[id]=message(results[1].reason);
      }));
      accounts={...accounts,...nextAccounts};relations={...relations,...nextRelations};rowErrors=nextErrors;
      verification=await VelaService.verification();
      const settings=await VelaService.settings();accountInterval=settings.account.enabled?settings.account.minutes:null;listSeconds=settings.general.providerSeconds;
    }catch(cause){error=message(cause);}finally{loading=false;}
  }
  onMount(()=>{
    let stopped=false;
    let timer:ReturnType<typeof setTimeout>;
    const poll=async()=>{await load();if(!stopped)timer=setTimeout(()=>{void poll();},listSeconds*1000);};
    void poll();
    const restored=()=>{if(document.visibilityState==="visible")void load();};
    document.addEventListener("visibilitychange",restored);
    return()=>{stopped=true;clearTimeout(timer);document.removeEventListener("visibilitychange",restored);};
  });
  /** 明确的主动刷新才查询上游；单项失败由行内查询状态表达。 */
  async function refresh():Promise<void>{refreshing=true;error="";try{await VelaService.refreshProviderAccounts("");await load();notify("账户刷新已完成，失败项可在对应账户查看原因");}catch(cause){error=message(cause);}finally{refreshing=false;}}
  /** 创建后打开新供应商详情，编辑后留在列表。 */
  async function saveProvider(draft:ProviderDraft):Promise<void>{busy=true;formError="";try{const created=!editing;const provider=editing?await VelaService.updateProvider(editing.id,draft):await VelaService.createProvider(draft);editorOpen=false;await load();if(created)detailID=provider.id;notify(created?"供应商已创建，请继续添加 Key":"供应商已更新");}catch(cause){formError=message(cause);}finally{busy=false;}}
  /** 启停独立于整行详情事件，保存失败后显示原状态。 */
  async function setEnabled(provider:Provider,value:boolean):Promise<void>{busy=true;try{await VelaService.setProviderEnabled(provider.id,value);await load();notify(value?"供应商已启用":"供应商已停用");}catch(cause){notify(message(cause));}finally{busy=false;}}
  /** 删除只在二次确认后调用，真实关联清理由数据库事务与外键完成。 */
  async function remove():Promise<void>{if(!deleting)return;busy=true;formError="";try{await VelaService.deleteProvider(deleting.id);if(detailID===deleting.id)detailID="";deleting=null;await load();notify("供应商及其关联配置已删除");}catch(cause){formError=message(cause);}finally{busy=false;}}
  /** 按计划有效组合统计可用结论，重复复测不累加覆盖。 */
  function verificationSummary(id:string):string{
    const plan=verification?.plans.find((item)=>item.providerId===id);
    if(!plan)return "尚未检测";
    const lines=plan.lines.filter((line)=>verification?.targets.some((target)=>target.providerId===id&&target.modelId===line.modelId&&target.keyId===line.keyId&&!target.reason));
    const reports=lines.flatMap((line)=>verification?.reports.filter((report)=>report.target.providerId===id&&report.target.modelId===line.modelId&&report.target.keyId===line.keyId&&report.target.protocol===line.protocol&&report.target.effort===line.effort&&report.state==="complete"&&report.conclusion!=="insufficient")??[]);
    if(!reports.length)return "尚未检测";
    const differences=reports.filter((report)=>report.conclusion==="difference").length;
    return `已测 ${reports.length}/${lines.length} · ${differences} 项差异`;
  }
  /** 最新检测时间来自当前供应商真实报告，不使用原型常量。 */
  function verificationTime(id:string):string{
    const times=verification?.reports.filter((report)=>report.target.providerId===id).map((report)=>report.time)??[];
    return times.length?new Date(Math.max(...times)).toLocaleString():"";
  }
  /** 常见币种使用符号展示；未知币种保留代码，避免丢失原币种语义。 */
  const currencyDisplay:Record<string,{symbol:string;name:string}>={USD:{symbol:"$",name:"美元"},CNY:{symbol:"¥",name:"人民币"},JPY:{symbol:"¥",name:"日元"},EUR:{symbol:"€",name:"欧元"},GBP:{symbol:"£",name:"英镑"}};
  /** 零余额原样展示，未查询成功与查询失败不能冒充零。 */
  function balance(account:ProviderAccount|undefined):BalanceDisplay{
    const value=account?.balance.amount;
    if(value===null||value===undefined)return {symbol:"",amount:"—",label:"余额未知",available:false};
    const currency=account?.balance.currency?.toUpperCase()||"";
    const meta=currencyDisplay[currency];
    const amount=value.toFixed(2);
    return {symbol:meta?.symbol??(currency||"¤"),amount,label:`${meta?.name??(currency||"未知币种")} ${amount}${currency?`（${currency}）`:""}`,available:true};
  }
  /** 倍率未知与有效零倍率必须分开展示，避免把未知结果伪装成 1×。 */
  function multiplier(value:number|null|undefined):string{return value===null||value===undefined?"倍率未知":`${value}×`;}
  /** 完整点击才打开详情，避免遮罩在按下时关闭后，松开事件穿透到行；行内控件各自处理。 */
  function openRow(event:MouseEvent,id:string):void{if(event.target instanceof Element&&event.target.closest("button,a,input,[role=switch]"))return;detailID=id;}
</script>
<section class="provider-page list-page">
  <PageHeader title="供应商" description="管理上游站点、密钥、模型关联与账户信息。">{#snippet actions()}<Button onclick={refresh} variant="ghost" size="icon-sm" aria-label={refreshing?"正在刷新上游信息":"刷新上游信息"}  aria-busy={refreshing} disabled={!isMyGo()||refreshing||loading||!rows.length} ><RefreshCw class={refreshing ? "animate-spin" : ""}/></Button><Button disabled={!isMyGo()||!defaults} onclick={()=>{editing=null;formError="";editorOpen=true;}}><Plus data-icon="inline-start"/>添加供应商</Button>{/snippet}</PageHeader>
  {#if !isMyGo()}<Alert.Root variant="info"><Alert.Description>浏览器预览：供应商业务需在桌面应用中使用；此处不生成演示数据或模拟保存。</Alert.Description></Alert.Root>{/if}
  {#if error}<Alert.Root variant="inline-error"><Alert.Description>{error}<Button variant="ghost" size="sm" onclick={load}>重新读取</Button></Alert.Description></Alert.Root>{/if}
  <section class="provider-surface list-surface">
    <div class="provider-toolbar"><InputGroup.Root class="provider-search"><InputGroup.Input aria-label="搜索供应商" bind:value={query} placeholder="搜索名称、主站或关联模型"/><InputGroup.Addon><Search/></InputGroup.Addon></InputGroup.Root><div class="provider-filter"><Choice bind:value={modelFilter} label="按关联模型筛选" options={modelOptions}/></div><ToggleGroup.Root type="single" value={status} onValueChange={(value)=>{if(value)status=value;}} variant="segmented" size="xs" spacing={1} aria-label="供应商状态筛选">{#each [{value:"all",label:"全部",count:rows.length},{value:"enabled",label:"已启用",count:rows.filter((row)=>row.provider.enabled).length},{value:"disabled",label:"已停用",count:rows.filter((row)=>!row.provider.enabled).length}] as item}<ToggleGroup.Item value={item.value}>{item.label} <span>{item.count}</span></ToggleGroup.Item>{/each}</ToggleGroup.Root></div>
    <!-- 独立滚动区需要键盘焦点，支持方向键与翻页键浏览列表。 -->
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <div class="list-scroll" role="region" aria-label="供应商列表内容" tabindex="0">
    <Table.Root class="provider-table"><Table.Header><Table.Row><Table.Head>供应商</Table.Head><Table.Head data-align="number">余额</Table.Head><Table.Head>首选 Key / 倍率</Table.Head><Table.Head>健康</Table.Head><Table.Head>模型验证</Table.Head><Table.Head data-align="control">状态</Table.Head><Table.Head data-align="control">操作</Table.Head></Table.Row></Table.Header><Table.Body>{#each filtered as row(row.provider.id)}{@const provider=row.provider}{@const account=accounts[provider.id]}{@const display=balance(account)}
      <!-- 名称按钮提供键盘入口，整行点击仅扩展鼠标命中区域。 -->
      <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_noninteractive_element_interactions -->
      <Table.Row data-disabled={!provider.enabled} onclick={(event)=>openRow(event,provider.id)}>
        <Table.Cell class="provider-main" data-label="供应商"><div class="provider-identity"><span class="provider-avatar">{provider.name.slice(0,1).toUpperCase()}</span><Hint overflowOnly text={provider.name}>{#snippet children(hintProps1)}<Button variant="text" size="table-inline" {...mergeProps(hintProps1, { onclick: ()=>detailID=provider.id })} class="provider-name" >{provider.name}</Button>{/snippet}</Hint>{#if provider.website}<a  href={provider.website} target="_blank" rel="noreferrer" aria-label={`打开 ${provider.name} 主站`} ><ExternalLink class="size-3 text-muted-foreground"/></a>{/if}</div>{#if rowErrors[provider.id]}<small class="text-destructive">{rowErrors[provider.id]}</small>{/if}</Table.Cell>
        <Table.Cell data-label="余额" data-align="number"><div class="provider-balance" class:is-empty={!display.available}  aria-label={display.label}>{#if display.symbol}<span class="provider-currency-symbol" aria-hidden="true">{display.symbol}</span>{/if}<span>{display.amount}</span></div>{#if account?.balance.status.state==="failed"}<small  class="block max-w-36 truncate text-[10px] text-destructive" >查询失败{account.balance.amount!==null?" · 保留上次结果":""}</small>{:else if accountInterval&&account?.config.mode}<small class="text-[10px] text-muted-foreground">每 {accountInterval} 分钟刷新</small>{/if}</Table.Cell>
        <Table.Cell data-label="首选 Key / 倍率">{#if row.preferredKey}<div class="provider-key-summary" aria-label={`首选 Key ${row.preferredKey.name}，倍率 ${multiplier(row.preferredKey.multiplier)}`}><Hint overflowOnly text={row.preferredKey.name}>{#snippet children(hintProps5)}{#if row.preferredKey}<span {...hintProps5} class="provider-key-name">{row.preferredKey.name}</span>{/if}{/snippet}</Hint><Badge variant={row.preferredKey.multiplier===null?"neutral":"info"}>{multiplier(row.preferredKey.multiplier)}</Badge></div>{/if}</Table.Cell>
        <Table.Cell data-label="健康"><Badge variant={row.health.state === "normal" ? "positive" : row.health.state === "abnormal" || row.health.state === "circuit" ? "negative" : row.health.state === "degraded" ? "warning" : "neutral"}>{#if row.health.state === "circuit"}<ShieldAlert aria-hidden="true" />{/if}{({normal:"正常",degraded:"降级",abnormal:"异常",circuit:"熔断",unavailable:"不可用"})[row.health.state]}</Badge></Table.Cell>
        <Table.Cell data-label="模型验证"><span class="text-[11px] text-muted-foreground">{verificationSummary(provider.id)}</span>{#if verificationTime(provider.id)}<Hint overflowOnly text={verificationTime(provider.id)}>{#snippet children(hintProps7)}<small {...hintProps7} class="block truncate text-[10px] text-muted-foreground" >{verificationTime(provider.id)}</small>{/snippet}</Hint>{/if}</Table.Cell>
        <Table.Cell data-label="状态" data-align="control"><Switch checked={provider.enabled} disabled={busy} aria-label={`${provider.enabled?"停用":"启用"}供应商 ${provider.name}`} onCheckedChange={(value)=>setEnabled(provider,value)}/></Table.Cell>
        <Table.Cell data-label="操作" data-align="control"><div class="provider-actions provider-row-actions"><Button onclick={()=>detailID=provider.id} variant="ghost" size="row-action" aria-label={`管理 ${provider.name}`}  ><SlidersHorizontal/></Button><Button onclick={()=>{editing=provider;formError="";editorOpen=true;}} variant="ghost" size="row-action" aria-label={`编辑 ${provider.name}`}  ><Pencil/></Button><Button onclick={()=>accountProvider=provider} variant="ghost" size="row-action" aria-label={`${provider.name} 账户信息`}  ><Wallet/></Button><Button onclick={()=>{deleting=provider;formError="";}} variant="ghost" size="row-action" aria-label={`删除 ${provider.name}`}  ><Trash2/></Button></div></Table.Cell>
      </Table.Row>
    {/each}</Table.Body></Table.Root>
    {#if loading}<Empty.Root>正在读取供应商…</Empty.Root>{:else if !filtered.length}<Empty.Root>{rows.length?"没有符合条件的供应商":"尚未添加供应商"}{#if rows.length}<Button variant="outline" onclick={()=>{query="";status="all";modelFilter="all";}}>清空筛选</Button>{:else}<Button variant="outline" disabled={!isMyGo()||!defaults} onclick={()=>{editing=null;formError="";editorOpen=true;}}>添加供应商</Button>{/if}</Empty.Root>{/if}
    </div>
  </section>
</section>
<Dialog.Root open={editorOpen} onOpenChange={(value)=>{if(!busy)editorOpen=value;}}><Dialog.Content class="provider-form-dialog provider-editor-dialog" showCloseButton={!busy} onInteractOutside={(event)=>{if(busy)event.preventDefault();}} onEscapeKeydown={(event)=>{if(busy)event.preventDefault();}}><Dialog.Header><Dialog.Title>{editing?"编辑供应商":"添加供应商"}</Dialog.Title><Dialog.Description>密钥和模型在创建后的供应商详情中维护。</Dialog.Description></Dialog.Header>{#if editorOpen&&defaults}<ProviderForm provider={editing} {defaults} {busy} error={formError} onsave={saveProvider} onclose={()=>editorOpen=false}/>{/if}</Dialog.Content></Dialog.Root>
<Sheet.Root open={selected!==null} onOpenChange={(value)=>{if(!value)detailID="";}}><Sheet.Content variant="wide" class="provider-drawer">{#if selected&&defaults}{#key selected.id}<ProviderDetails provider={selected} {defaults} refreshSeconds={listSeconds} onupdated={load} {notify}/>{/key}{/if}</Sheet.Content></Sheet.Root>
<Dialog.Root open={accountProvider!==null} onOpenChange={(value)=>{if(!value&&!accountBusy)accountProvider=null;}}><Dialog.Content class="provider-form-dialog" showCloseButton={!accountBusy} onInteractOutside={(event)=>{if(accountBusy)event.preventDefault();}} onEscapeKeydown={(event)=>{if(accountBusy)event.preventDefault();}}><Dialog.Header><Dialog.Title>账户信息</Dialog.Title><Dialog.Description>{accountProvider?.name} · 供应商余额查询，Key 信息来源在各 Key 内配置。</Dialog.Description></Dialog.Header>{#if accountProvider}<AccountForm providerID={accountProvider.id} baseURL={accountProvider.url} onbusy={(value)=>accountBusy=value} {notify} onclose={()=>accountProvider=null} onupdated={load}/>{/if}</Dialog.Content></Dialog.Root>
<Dialog.Root open={deleting!==null} onOpenChange={(value)=>{if(!busy&&!value)deleting=null;}}><Dialog.Content><Dialog.Header><Dialog.Title>删除供应商？</Dialog.Title><Dialog.Description>确认删除“{deleting?.name}”？它的全部 Key、凭据、模型关联、模型权限、映射和账户配置将一并删除，统一模型保持不变。</Dialog.Description></Dialog.Header>{#if formError}<Alert.Root variant="inline-error"><Alert.Description>{formError}</Alert.Description></Alert.Root>{/if}<Dialog.Footer><Button variant="outline" disabled={busy} onclick={()=>deleting=null}>取消</Button><Button variant="destructive" disabled={busy} onclick={remove}>确认删除</Button></Dialog.Footer></Dialog.Content></Dialog.Root>
