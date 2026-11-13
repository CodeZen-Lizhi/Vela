<script lang="ts">
  import { Hint } from "$lib/components/ui/tooltip";
  import { LoaderCircle, Square } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import type { VerificationProgress } from "$lib/generated/mygo";

  /** 以单条紧凑状态栏展示全局当前检测，目标切换时原位更新。 */
  let {
    progress,
    busy,
    onstop,
  }: {
    /** 后端推送的真实目标和累计样本进度。 */
    progress: VerificationProgress;
    /** 停止请求处理中时禁用重复操作。 */
    busy: boolean;
    /** 停止当前全局检测，保留已完成结果。 */
    onstop: () => void;
  } = $props();
</script>

<div class="verification-activity">
  <div class="activity-icon" aria-hidden="true"><LoaderCircle class="size-4 motion-safe:animate-spin" /></div>
  <div class="activity-copy" role="status" aria-atomic="true">
    <div class="activity-heading">
      <strong>{progress.mode === "single" ? "正在复测" : "正在执行检测计划"}</strong>
      <Hint overflowOnly text={`${progress.target.providerName} · ${progress.target.modelName} · ${progress.target.keyName}`}>{#snippet children(hintProps0)}<span {...hintProps0} class="activity-target" >{progress.target.providerName} · {progress.target.modelName} · {progress.target.keyName}</span>{/snippet}</Hint>
    </div>
    <p>全局一次运行一轮检测 · 停止后已发出的请求仍可能产生费用</p>
  </div>
  <span class="activity-count" aria-label={`已完成 ${progress.done} 条，共 ${progress.total} 条样本`}><strong>{progress.done}</strong><span>/ {progress.total} 样本</span></span>
  <Button variant="ghost" size="sm" disabled={busy} onclick={onstop}><Square data-icon="inline-start" />停止</Button>
</div>

<style>
  .verification-activity { display:flex; align-items:center; gap:14px; padding:14px 20px; border-bottom:1px solid var(--border); background:var(--background); }
  .activity-icon { display:grid; place-items:center; flex-shrink:0; color:var(--primary); }
  .activity-copy { flex:1; min-width:0; }
  .activity-heading { display:flex; align-items:baseline; gap:12px; min-width:0; font-size:12px; }
  .activity-heading > strong { flex-shrink:0; font-weight:600; }
  .activity-target { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; color:var(--muted-foreground); }
  .activity-copy p { margin-top:4px; font-size:11px; color:var(--muted-foreground); line-height:1.6; }
  .activity-count { display:flex; align-items:baseline; flex-shrink:0; gap:5px; font-size:12px; font-variant-numeric:tabular-nums; }
  .activity-count > strong { font-weight:600; color:var(--primary); }
  .activity-count > span { color:var(--muted-foreground); }
  @media (max-width:600px) {
    .verification-activity { gap:10px; padding:12px 16px; flex-wrap:wrap; }
    .activity-heading { flex-direction:column; gap:3px; align-items:stretch; }
    .activity-copy { flex-basis:calc(100% - 32px); }
    .activity-count { margin-left:26px; margin-right:auto; }
  }
</style>
