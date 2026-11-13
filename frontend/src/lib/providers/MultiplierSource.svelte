<script lang="ts">
  import { Hint } from "$lib/components/ui/tooltip";
  import * as ToggleGroup from "$lib/components/ui/toggle-group";

  /** 倍率来源分段选择；当前值由父级草稿或已保存配置持有，保存完成后才更新列表选中态。 */
  let { value, disabled = false, label = "倍率来源", onchange }: {
    /** manual 为手动维护，upstream 为跟随上游。 */
    value: string;
    /** 读取或保存期间禁止重复操作。 */
    disabled?: boolean;
    /** 多把 Key 同屏时标明控件所属对象。 */
    label?: string;
    /** 点击未选中的来源时交由父级更新。 */
    onchange: (value: "manual" | "upstream") => void;
  } = $props();
</script>

<ToggleGroup.Root type="single" {value} {disabled} variant="segmented" size="xs" spacing={1} aria-label={label}
  onValueChange={(next) => { if (next === "manual" || next === "upstream") onchange(next); }}>
  <Hint text="手动维护倍率">{#snippet children(props)}<ToggleGroup.Item {...props} value="manual">手动</ToggleGroup.Item>{/snippet}</Hint>
  <Hint text="跟随上游自动更新倍率">{#snippet children(props)}<ToggleGroup.Item {...props} value="upstream">自动</ToggleGroup.Item>{/snippet}</Hint>
</ToggleGroup.Root>
