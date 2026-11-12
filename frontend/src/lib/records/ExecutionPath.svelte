<script lang="ts">
  import type { Attempt, RecordsRecord } from "$lib/generated/mygo";
  import { Button } from "$lib/components/ui/button";
  import { ArrowRight } from "@lucide/svelte";
  import { date, duration, label, states } from "./display";
  import DetailSection from "./DetailSection.svelte";

  /** 实际执行路径按连续供应商分组，选中一次尝试后在路径下方展示事实。 */
  let { record, attempt = $bindable("0"), onresponse }: {
    /** 当前请求与实际尝试索引，不把排序候选当作已发请求。 */ record: RecordsRecord;
    attempt?: string;
    /** 从一次尝试进入其原始响应。 */ onresponse: () => void;
  } = $props();
  /** 连续供应商轮次保留真实顺序，同一供应商再次出现时另建一组。 */
  const groups = $derived.by(() => {
    const result: { id: string; name: string; attempts: { item: Attempt; index: number }[] }[] = [];
    record.attempts.forEach((item, index) => {
      let group = result.at(-1);
      if (!group || group.id !== item.providerId) {
        group = { id: item.providerId, name: item.providerName || "未知供应商", attempts: [] };
        result.push(group);
      }
      group.attempts.push({ item, index });
    });
    return result;
  });
  /** 旧版默认只展示路径，点击尝试后才展开下方检查器。 */
  let inspected = $state<number | null>(null);
  const current = $derived(inspected == null ? undefined : record.attempts[inspected]);
</script>

<section class="record-execution" aria-label="实际执行路径">
  <h3>执行路径</h3>
  <!-- 横向路径保留键盘焦点，方向键可查看窗口外的后续执行轮次。 -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div class="record-path-scroll" role="region" aria-label="横向执行路径" tabindex="0">
    <div class="record-path-stage">
      <section class="record-path-node" aria-label="请求入口"><small>请求入口</small><strong>{record.model || "未知模型"}</strong><code>{record.attempts[0]?.path || "未记录请求路径"}</code></section>
      <ArrowRight aria-hidden="true" class="record-path-arrow" />
      {#each groups as group, index}
        {#if index}<ArrowRight aria-hidden="true" class="record-path-arrow" />{/if}
        <section class="record-provider-round" aria-label={`供应商 ${index + 1} ${group.name}`}>
          <h4><span>{index + 1}</span>{group.name}</h4>
          <div class="record-attempt-stack">
            {#each group.attempts as entry}
              <Button variant={inspected === entry.index ? "secondary" : "outline"} size="content" class="record-attempt" aria-expanded={inspected === entry.index} aria-label={`查看尝试 ${entry.item.number} ${entry.item.keyName || "未知 Key"}`} onclick={() => { attempt = String(entry.index); inspected = inspected === entry.index ? null : entry.index; }}>
                <span class="record-attempt-head"><strong>{entry.item.status ? `HTTP ${entry.item.status}` : "未收到响应"}</strong><small>#{entry.item.number}</small></span>
                <span>Key · {entry.item.keyName || "未知 Key"}</span>
                <small>{duration(entry.item.durationMillis)}{entry.item.polling ? " · 状态轮询" : ""}</small>
                {#if entry.item.error}<small class="text-destructive">{entry.item.error}</small>{/if}
              </Button>
            {/each}
          </div>
        </section>
      {/each}
      <ArrowRight aria-hidden="true" class="record-path-arrow" />
      <section class="record-path-node" aria-label="请求结果"><small>请求结果</small><strong>{label(states, record.state)}</strong><span>总耗时 {duration(record.durationMillis)}</span></section>
    </div>
  </div>
  {#if !record.attempts.length}<p class="records-notice">请求未发送上游，没有上游 HTTP 响应。</p>{/if}
  {#if current}
    <section class="record-attempt-inspector" aria-label={`尝试 ${current.number} 详情`}>
      <dl class="record-context-grid">
        <div><dt>供应商 / 执行 Key</dt><dd>{current.providerName} / {current.keyName || "—"}</dd></div>
        <div><dt>HTTP / 耗时</dt><dd>{current.status || "未收到响应"} · {duration(current.durationMillis)}</dd></div>
        <div><dt>开始时间</dt><dd>{date(current.startedAt, "day")} {date(current.startedAt)}</dd></div>
        <div><dt>首字 / 倍率</dt><dd>{duration(current.firstByteMillis)} · {current.multiplier == null ? "倍率未知" : `${current.multiplier}×`}</dd></div>
      </dl>
      <code>{current.path}</code>
      {#if current.error}<p class="text-destructive">{current.error}</p>{/if}
      <Button variant="text" size="inline" onclick={onresponse}>查看本次响应</Button>
    </section>
  {/if}
  {#if record.candidates?.length}<DetailSection title="排序候选" description="排序快照，非实际发送次数">
    {#each record.candidates as candidate}<p>{candidate.providerName} / {candidate.keyName} · {candidate.reason || (record.attempts.some((item) => item.providerId === candidate.providerId && item.keyId === candidate.keyId) ? "已尝试" : "未执行")}</p>{/each}
  </DetailSection>{/if}
</section>
