/** 美元展示统一为紧贴数值的货币符号和两位小数，仅用于显示。 */
const usdFormat = new Intl.NumberFormat("en-US", {
  style: "currency",
  currency: "USD",
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
});

/**
 * 格式化美元金额；缺失值显示破折号，不改写底层价格、费用或余额。
 * @param {number | null | undefined} value 以美元计的原始金额，null/undefined 表示未知。
 * @returns {string} 带美元符号的两位小数文本，未知时为“—”。
 */
export function formatUSD(value) {
  return value == null ? "—" : usdFormat.format(value);
}
