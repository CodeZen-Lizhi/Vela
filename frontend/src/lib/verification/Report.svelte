<script lang="ts">
  import * as Dialog from "$lib/components/ui/dialog";
  import { Button } from "$lib/components/ui/button";
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

<Dialog.Header
  ><Dialog.Title>模型验证报告</Dialog.Title><Dialog.Description
    >仅保留本组合最近一轮结果及原始回答。</Dialog.Description
  ></Dialog.Header
>
<div class="v-report">
  <header class="v-report-heading">
    <span class="v-avatar">{report.target.providerName.slice(0, 1)}</span>
    <div>
      <h2>{report.target.modelName}</h2>
      <p>
        {report.target.providerName} · {report.target.keyName} · 思考{effort(
          report.target.effort,
        )}
      </p>
    </div>
    <Badge
      variant={report.conclusion === "difference" ? "destructive" : "secondary"}
      >{conclusions[report.conclusion] ?? conclusions.insufficient}</Badge
    >
  </header>
  <section class="v-report-score">
    <small>实际最接近的候选模型 · 参考库内</small>
    <h3>
      {report.candidates[0]?.name || report.candidates[0]?.model || "无法判断"}
    </h3>
    <strong>{probability(report.candidates[0]?.probability)}</strong>
    <p>{diagnosis(report.reason)}</p>
    {#if report.reason}<details class="v-sample">
        <summary>原始诊断</summary>
        <pre>{report.reason}</pre>
      </details>{/if}
    <p>执行状态：{states[report.state] ?? report.state}</p>
  </section>
  <section>
    <h3>固定执行目标</h3>
    <dl class="v-facts">
      {#each [{ name: "预期模型", value: report.target.modelName }, { name: "实际请求模型", value: report.target.requestModel }, { name: "请求协议", value: protocols.find((item) => item.value === report.target.protocol)?.label ?? report.target.protocol }, { name: "固定端点", value: report.target.url }, { name: "检测时间", value: time(report.time) }, { name: "算法版本", value: report.algorithmVersion }, { name: "参考数据版本", value: report.dataVersion }] as item}<div
        >
          <dt>{item.name}</dt>
          <dd>{item.value || "未知"}</dd>
        </div>{/each}
    </dl>
  </section>
  <section>
    <h3>真实候选排名</h3>
    <p class="v-note">
      置信度仅表示当前参考库候选集合内的相对可能性，不是身份真实性概率。
    </p>
    {#if report.candidates.length}<div class="v-candidates">
        {#each visibleCandidates as candidate, index}<div>
            <span>{index + 1}</span>
            <div>
              <strong>{candidate.name || candidate.model}</strong><small
                >{candidate.model} · {candidate.family || "家族未知"}</small
              >
            </div>
            <div>
              <strong>{probability(candidate.probability)}</strong><small
                title={`完整排名分：${candidate.score}`}
                >排名分 {rankScore(candidate.score)}</small
              >
            </div>
          </div>{/each}
      </div>
      {#if report.candidates.length > 8}<Button
          variant="ghost"
          size="sm"
          aria-expanded={expanded}
          onclick={() => (expanded = !expanded)}
          >{expanded
            ? "收起为前 8 名"
            : `展开全部 ${report.candidates.length} 个候选`}</Button
        >{/if}
    {:else}<p class="v-note">没有可用候选；请检查诊断原因与样本。</p>{/if}
  </section>
  <section>
    <h3>样本解析与原始回答</h3>
    {#each report.samples as sample, index}<details class="v-sample">
        <summary
          >样本 {index + 1} · {states[sample.state] ?? sample.state} · 有效数字 {sample.count}
          / {sample.expectedCount}</summary
        >{#if sample.error}<p class="v-error">{sample.error}</p>{/if}
        <pre>{sample.text || "未收到回答"}</pre>
      </details>{/each}{#if !report.samples.length}<p class="v-note">
        本轮没有返回样本。
      </p>{/if}
  </section>
  <p class="v-note">
    差异只提示进一步检查，不能单凭结果认定渠道造假或模型能力下降。结论只描述该固定组合在检测时刻的统计相似性。
  </p>
</div>
