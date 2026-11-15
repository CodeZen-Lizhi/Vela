<script lang="ts">
  import { ArrowDownToLine, ArrowUpFromLine, FileJson } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  // 配置迁移入口沿用现有禁用占位，尚未接入真实导入导出。
</script>

  <section class="settings-card p-6" aria-labelledby="transfer-title">
    <div class="flex items-center gap-3">
      <span
        class="grid size-9.5 shrink-0 place-items-center rounded-[10px] bg-accent text-accent-foreground"
        ><FileJson class="size-5" aria-hidden="true" /></span
      >
      <div>
        <h2 id="transfer-title" class="text-[15px] font-semibold">配置迁移</h2>
        <p class="mt-1 text-xs text-muted-foreground">
          公司和个人电脑，复用同一套网关配置。
        </p>
      </div>
    </div>
    <p class="my-5 text-xs leading-relaxed text-muted-foreground">
      搬运供应商、上游 Key 配置、模型映射与调度方案。
    </p>
    <div class="flex flex-wrap gap-2.5">
      <Button variant="outline" size="sm" disabled><ArrowUpFromLine />导出配置</Button>
      <Button variant="outline" size="sm" disabled><ArrowDownToLine />导入配置</Button>
    </div>
    <p class="mt-5 border-t pt-4 text-[11px] leading-relaxed text-muted-foreground">
      调用历史、统计、本机网络与窗口偏好、网关访问密钥留在各自电脑。供应商与调度接入后开放导入导出。
    </p>
  </section>
