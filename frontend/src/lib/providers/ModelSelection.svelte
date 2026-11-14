<script lang="ts">
  import { Label } from "$lib/components/ui/label";
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";

  /** 模型关联与 Key 权限共用列表，仅展示身份、勾选和可选状态。 */
  interface SelectionModel {
    /** 本地模型稳定身份，用于勾选与键控。 */
    id: string;
    /** 模型显示名称。 */
    name: string;
    /** 实际调用模型 ID，显示在名称下方。 */
    modelName: string;
    /** 关联草稿的变更提示；权限列表无需提供。 */
    status?: string;
  }
  /** 批量操作由业务调用方解释，组件不改变关联或权限语义。 */
  let { models, selected, disabled = false, emptyText, onselect, onselectall }: {
    /** 当前可选择的模型及其展示信息。 */
    models: SelectionModel[];
    /** 当前明确勾选的模型稳定身份。 */
    selected: string[];
    /** 保存或加载期间禁用选择操作。 */
    disabled?: boolean;
    /** 无可选模型时的业务提示。 */
    emptyText: string;
    /** 更新单个模型的草稿勾选状态。 */
    onselect: (id: string, checked: boolean) => void;
    /** 全选或取消全部勾选，由调用方维护草稿。 */
    onselectall: (checked: boolean) => void;
  } = $props();
  /** 仅统计当前列表中的勾选，历史上已移除的模型不影响全选状态。 */
  const selectedCount = $derived(models.filter((model) => selected.includes(model.id)).length);
  /** 全选与部分选择分别对应勾选和横杠状态。 */
  const allSelected = $derived(models.length > 0 && selectedCount === models.length);
  /** 只有当前列表的一部分模型被勾选时展示半选状态。 */
  const partiallySelected = $derived(selectedCount > 0 && !allSelected);
</script>

<div class="provider-selection">
  <div class="provider-selection-toolbar">
    <Label variant="content" class="flex items-center gap-3">
      <Checkbox disabled={disabled || !models.length} checked={allSelected} indeterminate={partiallySelected} onCheckedChange={(checked) => onselectall(checked === true)} />
      <span>全选模型</span>
    </Label>
    <span class="text-xs text-muted-foreground" aria-live="polite">已选 {selectedCount} / {models.length}</span>
    <Button variant="ghost" size="xs" disabled={disabled || !models.length} onclick={() => onselectall(false)}>清空选择</Button>
  </div>
  {#each models as model (model.id)}
    <Label variant="content" class="provider-check-row">
      <Checkbox disabled={disabled} checked={selected.includes(model.id)} onCheckedChange={(value) => onselect(model.id, value === true)} />
      <span class="min-w-0 flex-1"><strong class="block font-medium break-words">{model.name}</strong><code class="text-[11px] text-muted-foreground break-all">{model.modelName}</code></span>
      {#if model.status}<small class="shrink-0 text-muted-foreground">{model.status}</small>{/if}
    </Label>
  {:else}<p class="p-5 text-xs text-muted-foreground">{emptyText}</p>{/each}
</div>

<style>
  .provider-selection-toolbar { display:flex; align-items:center; flex-wrap:wrap; gap:8px 16px; padding:9px 15px; border-bottom:1px solid var(--border); background:var(--muted); }
  .provider-selection-toolbar :global(button[data-slot=button]) { margin-left:auto; }
</style>
