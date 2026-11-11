<script lang="ts">
  /** 只读显示后端筛选过的订阅额度，不与余额或本地月配额相加。 */
  let { value }: { value: string } = $props();
  /** 上游字段对应的业务名称；未支持的字段不向用户输出内部标识。 */
  const labels: Record<string, string> = {
    active_count: "有效订阅数", total_used_usd: "累计已用（美元）", group_name: "分组",
    name: "名称", status: "状态", expires_at: "到期时间", start_at: "开始时间", end_at: "结束时间",
    daily_limit_usd: "日额度（美元）", weekly_limit_usd: "周额度（美元）", monthly_limit_usd: "月额度（美元）",
    daily_used_usd: "日已用（美元）", weekly_used_usd: "周已用（美元）", monthly_used_usd: "月已用（美元）",
    daily_usage_usd: "日已用（美元）", weekly_usage_usd: "周已用（美元）", monthly_usage_usd: "月已用（美元）",
    daily_limit: "日额度", weekly_limit: "周额度", monthly_limit: "月额度",
    daily_usage: "日已用", weekly_usage: "周已用", monthly_usage: "月已用",
    daily_reset_at: "日重置时间", weekly_reset_at: "周重置时间", monthly_reset_at: "月重置时间",
  };
  /** 递归读取对象及订阅列表，保留真实零值；无可读字段时显示空状态。 */
  function fields(raw: string): {label: string; value: string}[] {
    const rows: {label: string; value: string}[] = [];
    function visit(node: unknown, prefix = ""): void {
      if (Array.isArray(node)) { node.forEach((item, index) => visit(item, `订阅 ${index + 1} · `)); return; }
      if (!node || typeof node !== "object") return;
      for (const [key, item] of Object.entries(node)) {
        if (item !== null && typeof item === "object") visit(item, prefix);
        else if (labels[key] && (typeof item === "string" || typeof item === "number")) rows.push({label: `${prefix}${labels[key]}`, value: String(item)});
      }
    }
    try { visit(JSON.parse(raw)); } catch { return []; }
    return rows;
  }
  const rows = $derived(fields(value));
</script>
<section class="mt-3 border-t pt-3" aria-label="上游订阅额度">
  <h4 class="mb-2 text-xs font-medium">上游订阅额度</h4>
  {#each rows as row}<div class="flex flex-wrap justify-between gap-3 py-1 text-xs"><span class="text-muted-foreground">{row.label}</span><span class="break-all">{row.value}</span></div>{:else}<p class="text-xs text-muted-foreground">暂无可展示的订阅额度。</p>{/each}
</section>
