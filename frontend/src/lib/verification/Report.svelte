<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import * as Empty from "$lib/components/ui/empty";
  import * as Dialog from "$lib/components/ui/dialog";
  import { Button } from "$lib/components/ui/button";
  import * as Accordion from "$lib/components/ui/accordion";
  import * as Table from "$lib/components/ui/table";
  import { Separator } from "$lib/components/ui/separator";
  import { Info, CircleCheck, CircleAlert } from "@lucide/svelte";
  import { Badge } from "$lib/components/ui/badge";
  import type { VerificationReport } from "$lib/generated/mygo";
  import { conclusions, effort, probability, protocols, time } from "./display";
  /** 报告只呈现后端最近一轮的真实结果，不计算替代评分。 */
  let { report }: { /** 当前组合最近一轮报告。 */ report: VerificationReport } =
    $props();
  /** 候选默认仅显示前八名，展开后仍保留完整排名。 */
  let expanded = $state(false);
  const visibleCandidates = $derived(
    expanded ? report.candidates : report.candidates.slice(0, 8),
  );
  /** 按已核实的上游诊断提供中文解释，未知诊断原样保留。 */
  function diagnosis(reason: string): string {
    if (
      reason ===
      "Confidence is a closed-set probability within the reference bank."
    )
      return "置信度表示当前参考库候选集合内的相对可能性，不能视为模型身份真实性概率。";
    if (
      reason ===
      "The confidence calibration does not match this detector. Confidence is unavailable."
    )
      return "置信度校准数据与当前检测器不匹配，无法提供有效置信度。";
    if (
      reason ===
      "Consecutive numbers change differently from the language-model replies in the reference bank. Program-generated, sorted or shuffled numbers often do this. No ranking was produced."
    )
      return "回答中的相邻数字变化模式与参考库中的模型回答不同，可能呈现程序生成、排序或打乱数字的特征，本轮未生成排名。";
    const partial = reason.match(
      /^Ranked (\d+) valid samples?\. Confidence requires three valid samples\.$/,
    );
    if (partial)
      return `已根据 ${partial[1]} 条有效样本生成候选排名；计算置信度需要完整的 3 条有效样本。`;
    const insufficient = reason.match(
      /^Received (\d+)\/3 valid samples\. Three valid samples are required\.$/,
    );
    if (insufficient)
      return `本轮仅收到 ${insufficient[1]} / 3 条有效样本，未满足评分所需的 3 条有效样本。`;
    return reason || "未提供额外诊断说明";
  }
  /** 排名分保留四位有效数字，完整原值保留在悬停提示中。 */
  function rankScore(value: number): string {
    return Number(value.toPrecision(4)).toString();
  }
  /** 样本与执行状态的中文展示，未知状态保留原值。 */
  const states: Record<string, string> = {
    complete: "完成",
    failed: "失败",
    cancelled: "已取消",
    truncated: "回答截断",
  };
</script>

<Dialog.Header class="shrink-0 border-b px-6 py-5 pr-16">
  <Dialog.Title>验证结果详情</Dialog.Title>
  <Dialog.Description class="sr-only">当前固定组合最近一轮的验证结论、候选排名与原始样本。</Dialog.Description>
</Dialog.Header>
<div class="report-body scroll-quiet">
  <header class="report-identity">
    <span class="v-avatar" aria-hidden="true">{report.target.providerName.slice(0, 1)}</span>
    <div class="report-model">
      <h2>{report.target.modelName}</h2>
      <p>{report.target.providerName} · {report.target.keyName} · 思考{effort(report.target.effort)}</p>
    </div>
    <Badge variant={report.conclusion === "difference" ? "destructive" : "secondary"}>
      {#if report.conclusion === "match"}<CircleCheck />{:else}<CircleAlert />{/if}
      {conclusions[report.conclusion] ?? conclusions.insufficient}
    </Badge>
  </header>

  <section class="report-overview" aria-label="验证结论">
    <div class="report-confidence">
      <span>参考库内置信度</span>
      <strong>{probability(report.candidates[0]?.probability)}</strong>
      <small>仅限当前候选集合</small>
    </div>
    <div class="report-verdict">
      <span>最接近的候选模型</span>
      <h3>{report.candidates[0]?.name || report.candidates[0]?.model || "无法判断"}</h3>
      <p>{diagnosis(report.reason)}</p>
    </div>
  </section>

  <section aria-labelledby="report-target-title">
    <div class="report-section-heading">
      <h3 id="report-target-title">执行目标</h3>
      <Badge variant={report.state === "failed" ? "destructive" : "secondary"}>执行{states[report.state] ?? report.state}</Badge>
    </div>
    <dl class="report-facts">
      {#each [{ name: "预期模型", value: report.target.modelName }, { name: "预期参考模型", value: report.target.referenceModel || report.target.originalModel, code: true }, { name: "实际请求模型", value: report.target.requestModel, code: true }, { name: "请求协议", value: protocols.find((item) => item.value === report.target.protocol)?.label ?? report.target.protocol }, { name: "检测时间", value: time(report.time) }, { name: "算法版本", value: report.algorithmVersion, code: true }, { name: "参考数据版本", value: report.dataVersion, code: true }, { name: "固定端点", value: report.target.url, code: true }] as item}
        <div class:report-fact-wide={item.name === "固定端点" || item.name === "参考数据版本"}>
          <dt>{item.name}</dt><dd class:report-fact-code={item.code}>{item.value || "未知"}</dd>
        </div>
      {/each}
    </dl>
  </section>

  <Separator />

  <section aria-labelledby="report-ranking-title">
    <div class="report-section-heading">
      <h3 id="report-ranking-title">候选排名</h3>
      <Badge variant="secondary">{report.candidates.length} 个候选</Badge>
    </div>
    <p class="report-caption">置信度表示参考库内的相对可能性，不代表模型身份真实性。</p>
    {#if report.candidates.length}
      <div class="report-ranking">
        <Table.Root class="table-fixed">
          <Table.Header><Table.Row>
            <Table.Head class="w-12">排名</Table.Head><Table.Head>候选模型</Table.Head>
            <Table.Head class="w-24 text-right">置信度</Table.Head><Table.Head class="w-24 text-right">排名分</Table.Head>
          </Table.Row></Table.Header>
          <Table.Body>
            {#each visibleCandidates as candidate, index}
              <Table.Row>
                <Table.Cell><span class="report-rank">{String(index + 1).padStart(2, "0")}</span></Table.Cell>
                <Table.Cell class="whitespace-normal break-words">
                  <strong class="report-candidate-name">{candidate.name || candidate.model}</strong>
                  <small class="report-candidate-id">{candidate.model} · {candidate.family || "家族未知"}</small>
                </Table.Cell>
                <Table.Cell class="text-right tabular-nums">{probability(candidate.probability)}</Table.Cell>
                <Table.Cell  class="text-right tabular-nums" >{rankScore(candidate.score)}</Table.Cell>
              </Table.Row>
            {/each}
          </Table.Body>
        </Table.Root>
      </div>
      {#if report.candidates.length > 8}
        <div class="report-expand"><Button variant="ghost" size="sm" aria-expanded={expanded} onclick={() => (expanded = !expanded)}>{expanded ? "收起为前 8 名" : `展开全部 ${report.candidates.length} 个候选`}</Button></div>
      {/if}
    {:else}<Empty.Root size="compact">本轮没有可用候选，请查看下方诊断与样本。</Empty.Root>{/if}
  </section>

  <Separator />

  <section aria-labelledby="report-samples-title">
    <div class="report-section-heading">
      <h3 id="report-samples-title">样本与诊断</h3>
      <Badge variant="secondary">{report.samples.length} 条样本</Badge>
    </div>
    <p class="report-caption">保留本轮原始回答，展开查看解析结果及失败原因。</p>
    <Accordion.Root type="multiple">
      {#each report.samples as sample, index}
        <Accordion.Item value={`sample-${index}`}>
          <Accordion.Trigger>
            <span class="report-sample-title"><span class="report-rank">{String(index + 1).padStart(2, "0")}</span><span>样本 {index + 1}</span><span class="report-sample-count">有效数字 {sample.count} / {sample.expectedCount}</span><Badge variant={sample.state === "failed" || sample.state === "truncated" ? "destructive" : "secondary"}>{states[sample.state] ?? sample.state}</Badge></span>
          </Accordion.Trigger>
          <Accordion.Content>
            {#if sample.error}<Alert.Root variant="inline-error"><Alert.Description>{sample.error}</Alert.Description></Alert.Root>{/if}
            <pre class="report-raw scroll-quiet">{sample.text || "未收到回答"}</pre>
          </Accordion.Content>
        </Accordion.Item>
      {/each}
      {#if report.reason}
        <Accordion.Item value="diagnosis"><Accordion.Trigger>原始诊断</Accordion.Trigger><Accordion.Content><pre class="report-raw scroll-quiet">{report.reason}</pre></Accordion.Content></Accordion.Item>
      {/if}
    </Accordion.Root>
    {#if !report.samples.length}<Empty.Root size="compact">本轮没有返回样本。</Empty.Root>{/if}
  </section>

  <Alert.Root variant="info"><Alert.Description><Info class="size-4 shrink-0" aria-hidden="true" /><p>结论仅描述该固定组合在检测时刻的统计相似性。差异只提示进一步检查，不能单凭结果认定渠道造假或模型能力下降。</p></Alert.Description></Alert.Root>
</div>

<style>
  /* 技术身份使用等宽字形，字段标签与值提高字重和对比度，避免桌面 WebView 中小号浅色文字发虚。 */
  .report-body { display:flex; flex-direction:column; gap:24px; min-height:0; overflow-y:auto; padding:24px; }
  .report-identity { display:flex; align-items:center; gap:12px; flex-wrap:wrap; }
  .report-model { flex:1; min-width:160px; }
  .report-model h2 { font-size:20px; font-weight:600; letter-spacing:-.025em; overflow-wrap:anywhere; }
  .report-model p { margin-top:5px; color:var(--muted-foreground); font-size:12.5px; font-weight:500; overflow-wrap:anywhere; }
  .report-overview { display:grid; grid-template-columns:180px minmax(0,1fr); border:1px solid var(--border); border-radius:10px; background:var(--muted); }
  .report-confidence { display:flex; flex-direction:column; justify-content:center; gap:8px; padding:20px; border-right:1px solid var(--border); }
  .report-confidence > span, .report-verdict > span { font-size:13px; font-weight:550; color:var(--muted-foreground); }
  .report-confidence strong { font-size:34px; line-height:1.15; font-weight:650; letter-spacing:-.04em; font-variant-numeric:tabular-nums; color:var(--primary); }
  .report-confidence small { font-size:12px; font-weight:500; color:var(--muted-foreground); }
  .report-verdict { min-width:0; padding:20px; }
  .report-verdict h3 { margin-top:6px; font-size:17px; font-weight:650; overflow-wrap:anywhere; }
  .report-verdict p { margin-top:10px; font-size:13px; font-weight:500; line-height:1.7; color:color-mix(in srgb,var(--muted-foreground) 78%,var(--foreground)); overflow-wrap:anywhere; }
  .report-section-heading { display:flex; align-items:center; justify-content:space-between; gap:12px; }
  .report-section-heading h3 { font-size:16px; font-weight:650; }
  .report-facts { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:1px; margin-top:16px; overflow:hidden; border:1px solid var(--border); border-radius:10px; background:var(--border); }
  .report-facts > div { min-width:0; padding:14px 16px; background:var(--card); }
  .report-fact-wide { grid-column:1/-1; }
  .report-facts dt { font-size:12.5px; font-weight:550; color:var(--muted-foreground); }
  .report-facts dd { margin-top:5px; font-size:15px; font-weight:550; line-height:1.55; overflow-wrap:anywhere; }
  .report-facts dd.report-fact-code { font-family:ui-monospace,"SFMono-Regular",Menlo,Consolas,monospace; font-size:13.5px; font-weight:500; letter-spacing:-.01em; }
  .report-caption { margin-top:6px; color:color-mix(in srgb,var(--muted-foreground) 78%,var(--foreground)); font-size:13px; font-weight:500; line-height:1.7; }
  .report-ranking { margin-top:14px; border:1px solid var(--border); border-radius:10px; overflow:hidden; }
  .report-candidate-name { display:block; font-weight:600; font-size:14.5px; overflow-wrap:anywhere; }
  .report-candidate-id { display:block; margin-top:4px; font-family:ui-monospace,"SFMono-Regular",Menlo,Consolas,monospace; font-size:12.5px; font-weight:500; color:var(--muted-foreground); overflow-wrap:anywhere; }
  .report-rank { color:var(--muted-foreground); font-size:13px; font-weight:500; font-variant-numeric:tabular-nums; }
  .report-expand { display:flex; justify-content:center; margin-top:8px; }

  .report-sample-title { display:flex; flex:1; align-items:center; flex-wrap:wrap; gap:12px; min-width:0; }
  .report-sample-count { margin-left:auto; color:var(--muted-foreground); font-size:13px; font-weight:400; }
  .report-raw { max-height:240px; margin-top:8px; padding:14px; overflow:auto; white-space:pre-wrap; overflow-wrap:anywhere; border-radius:8px; background:var(--muted); font-size:13px; line-height:1.8; }

  @media (max-width:600px) {
    .report-body { padding:20px; gap:20px; }
    .report-overview { grid-template-columns:1fr; }
    .report-confidence { border-right:0; border-bottom:1px solid var(--border); }
    .report-facts { grid-template-columns:1fr; }
    .report-sample-count { margin-left:0; }
  }
</style>
