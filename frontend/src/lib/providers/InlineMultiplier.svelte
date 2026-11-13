<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { tick } from "svelte";
  import { Pencil } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";

  /** 手动倍率的行内草稿，Enter/失焦保存，Esc 放弃；失败保留输入供修正。 */
  let { value, editable, disabled = false, label, onsave }: {
    /** 已保存倍率；null 表示尚未取得有效值。 */
    value: number | null;
    /** 只有已确认手动来源时允许进入编辑。 */
    editable: boolean;
    /** 同一详情正在保存时锁定入口。 */
    disabled?: boolean;
    /** 当前 Key 名称用于区分多个行内编辑入口。 */
    label: string;
    /** 成功才退出编辑，失败由当前输入显示。 */
    onsave: (value: number) => Promise<void>;
  } = $props();
  /** 输入草稿、编辑状态和错误仅属于本行，不修改父级快照。 */
  let editing=$state(false), draft=$state<number|undefined>(), saving=$state(false), error=$state("");
  /** 进入编辑后聚焦并选中数值，便于直接替换。 */
  let input=$state<HTMLInputElement|null>(null);
  /** 每次打开从已保存值创建草稿，未知值保持空白。 */
  async function start():Promise<void>{draft=value??undefined;error="";editing=true;await tick();input?.focus();input?.select();}
  /** 客户端校验与后端约束一致；拒绝重复提交和无变化写入。 */
  async function save():Promise<void>{
    if(!editing||saving)return;
    if(draft===undefined||!Number.isFinite(draft)||draft<0||draft>100){error="请输入 0～100 的倍率";return;}
    if(draft===value){editing=false;return;}
    saving=true;error="";
    try{await onsave(draft);editing=false;}catch(cause){error=cause instanceof Error?cause.message:String(cause);}finally{saving=false;}
  }
  /** Esc 只取消本行输入，不继续关闭详情侧栏。 */
  function keydown(event:KeyboardEvent):void{
    if(event.key==="Escape"){event.preventDefault();event.stopPropagation();if(!saving){editing=false;error="";}}
    if(event.key==="Enter"){event.preventDefault();event.stopPropagation();void save();}
  }
</script>

<div class="inline-multiplier">
  {#if editing}
    <Input density="compact" class="w-16" bind:ref={input} type="number" min={0} max={100} step="any" bind:value={draft} aria-label={`${label} 手动倍率`} aria-invalid={!!error} disabled={saving} onblur={save} onkeydown={keydown}/>
  {:else if editable}
    <Button onclick={start} variant="ghost" size="table-inline" class="w-full justify-start" {disabled}  aria-label={`修改 ${label} 倍率`} >{value===null?"倍率未知":`${value}×`}<Pencil class="size-3" aria-hidden="true"/></Button>
  {:else}<span class="provider-multiplier-value">{value===null?"倍率未知":`${value}×`}</span>{/if}
  {#if error}<Alert.Root variant="inline-error"><Alert.Description>{error}</Alert.Description></Alert.Root>{/if}
</div>

<style>
  /* 手动按钮、自动文本与输入框共用尺寸，不让来源切换改变表格的内容宽度。 */
  .inline-multiplier { position:relative; display:flex; align-items:center; width:64px; min-height:32px; flex:0 0 64px; }

</style>
