import type {
  VerificationLine,
  VerificationReport,
  VerificationTarget,
} from "$lib/generated/mygo";

/** 三类结论的展示文案；未知值保留不可判断语义。 */
export const conclusions: Record<string, string> = {
  match: "与预期接近",
  difference: "存在差异",
  insufficient: "证据不足 / 无法判断",
};
/** 请求协议为显式选择，空值不作为默认协议。 */
export const protocols = [
  { value: "chat_completions", label: "OpenAI Chat Completions" },
  { value: "responses", label: "OpenAI Responses" },
  { value: "messages", label: "Anthropic Messages" },
  { value: "gemini", label: "Gemini 原生协议" },
];
/** 思考强度业务值及中文名称。 */
export const efforts = [
  { value: "auto", label: "自动" },
  { value: "low", label: "低" },
  { value: "medium", label: "中" },
  { value: "high", label: "高" },
];
/** 显示真实毫秒时间；未执行使用独立占位。 */
export function time(value: number): string {
  return value
    ? new Date(value).toLocaleString("zh-CN", { hour12: false })
    : "未执行";
}
/** 置信度仅呈现上游实际返回值，不将缺失转换为零。 */
export function probability(value: number | null | undefined): string {
  return value == null ? "未知" : `${(value * 100).toFixed(1)}%`;
}
/** 将思考强度转换为中文，保留未知值便于诊断。 */
export function effort(value: string): string {
  return efforts.find((item) => item.value === value)?.label ?? value;
}
/** 精确比较执行线路与参考选择，避免新选择沿用旧报告覆盖；旧报告缺失选择视为空。 */
export function sameLine(
  left: VerificationLine,
  right: VerificationLine,
): boolean {
  return (
    left.modelId === right.modelId &&
    left.keyId === right.keyId &&
    left.protocol === right.protocol &&
    left.effort === right.effort &&
    (left.referenceModel ?? "") === (right.referenceModel ?? "")
  );
}
/** 复测必须仍能找到相同出站模型和地址，不把旧报告静默替换为新目标。 */
export function unavailable(
  report: VerificationReport,
  targets: VerificationTarget[],
): string {
  const current = targets.find(
    (target) =>
      target.providerId === report.target.providerId &&
      target.modelId === report.target.modelId &&
      target.keyId === report.target.keyId,
  );
  if (!current) return "目标已删除或不再兼容";
  return (
    current.reason ||
    (current.requestModel !== report.target.requestModel ||
    current.url !== report.target.url
      ? "实际请求模型或地址已变化，请从计划执行"
      : "")
  );
}
