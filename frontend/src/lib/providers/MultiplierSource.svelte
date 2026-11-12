<script lang="ts">
  import { Button } from "$lib/components/ui/button";

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

<div class="multiplier-source" role="group" aria-label={label}>
  <Button variant="ghost" size="xs" {disabled} title="手动维护倍率" aria-pressed={value === "manual"} onclick={() => { if (value !== "manual") onchange("manual"); }}>手动</Button>
  <Button variant="ghost" size="xs" {disabled} title="跟随上游自动更新倍率" aria-pressed={value === "upstream"} onclick={() => { if (value !== "upstream") onchange("upstream"); }}>自动</Button>
</div>

<style>
  /* 固定紧凑尺寸，手动与自动切换时不改变列宽，窄窗口仍可与倍率同排。 */
  .multiplier-source { display:inline-flex; flex:0 0 auto; align-items:center; gap:2px; padding:2px; border:1px solid var(--border); border-radius:8px; background:var(--muted); width:max-content; white-space:nowrap; }
  .multiplier-source :global(button) { width:36px; height:24px; padding:0; border-radius:5px; font-size:11px; color:var(--muted-foreground); }
  .multiplier-source :global(button[aria-pressed=true]) { color:var(--primary); background:var(--background); box-shadow:0 1px 3px #00000012; }
  .multiplier-source :global(button:focus-visible) { z-index:1; }
</style>
