import type { Pricing, Rate } from "$lib/generated/mygo";

/** 类型名称供列表、筛选和详情共用。 */
export const kinds: Record<string, string> = {
  chat: "对话",
  image: "图片",
  video: "视频",
  vector: "向量",
};
/** 价格组名与 Go 侧修改保护一致。 */
export const groupNames: Record<string, string> = {
  base: "基础价格",
  tiers: "上下文阶梯",
  modes: "Fast 模式",
  schedules: "峰谷时段",
  images: "图片矩阵",
  videos: "视频分辨率",
};
/** 计价维度名称；单价统一使用美元。 */
export const rateNames: Record<string, string> = {
  input: "输入",
  output: "输出",
  cache_read: "缓存读取",
  cache_write: "5 分钟缓存创建",
  cache_write_1h: "1 小时缓存创建",
  image_input: "图片输入 Token",
  image_output: "图片输出 Token",
  audio_input: "音频输入 Token",
  audio_output: "音频输出 Token",
  per_image: "每张图片",
  per_second: "每秒视频",
  per_request: "每次请求",
};
/** 按维度返回价格单位，不从模型名称猜单位。 */
export function rateUnit(key: string): string {
  return (
    (
      {
        per_image: "image",
        per_second: "second",
        per_request: "request",
      } as Record<string, string>
    )[key] ?? "million_tokens"
  );
}
/** 单位标签在编辑及只读详情保持一致。 */
export function unitLabel(unit: string): string {
  return (
    (
      {
        million_tokens: "美元 / 百万 Token",
        image: "美元 / 张",
        second: "美元 / 秒",
        request: "美元 / 次",
      } as Record<string, string>
    )[unit] ?? unit
  );
}
/** 单价保留实际小数，不把未知值格式化为零。 */
export function money(value: number): string {
  return `$${value.toLocaleString("en-US", { maximumFractionDigits: 8 })}`;
}
/** 列表只展示当前类型主价，其余规则在详情中展开。 */
export function summary(p: Pricing): string {
  const rates = p.base ?? [];
  return (
    rates
      .slice(0, 2)
      .map((r) => `${rateNames[r.key] ?? r.key} ${money(r.price)}`)
      .join(" / ") ||
    (p.images?.[0]
      ? `${money(p.images[0].price)} / 张`
      : p.videos?.[0]
        ? `${money(p.videos[0].price)} / 秒`
        : "价格未知")
  );
}
/** 时间以本机时区展示，尚未取得来源时保持未知。 */
export function dateLabel(value: string): string {
  return value
    ? new Date(value).toLocaleString("zh-CN", { hour12: false })
    : "尚未同步";
}
/** 构造用户主动新增的价格项，零值只在明确填写的行中表示免费。 */
export function newRate(key = "input"): Rate {
  return { key, price: 0, unit: rateUnit(key) };
}
