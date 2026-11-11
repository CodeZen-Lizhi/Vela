import type { CatalogEntry } from "$lib/generated/mygo";

/**
 * 统一模型目录搜索文本：忽略大小写与全半角差异，并把 ID 中常见的连字符、
 * 下划线、斜杠和点号视为空格，使粘贴的真实模型 ID 能匹配展示名称。
 */
export function normalizeCatalogSearch(value: string): string {
  return value
    .normalize("NFKC")
    .toLowerCase()
    .replace(/[^\p{L}\p{N}]+/gu, " ")
    .trim();
}

/**
 * 按关键词和模型类型筛选目录。官方来源始终排在非官方变体之前，
 * 同一来源分组内保持 Go 服务返回的发布日期顺序。
 */
export function searchCatalogEntries(
  entries: CatalogEntry[],
  query: string,
  kind: string,
  kindLabels: Record<string, string> = {},
): CatalogEntry[] {
  const words = normalizeCatalogSearch(query).split(" ").filter(Boolean);
  return entries
    .filter((entry) => {
      if (kind !== "all" && entry.kind !== kind) return false;
      if (!words.length) return true;
      const text = normalizeCatalogSearch(
        `${entry.name} ${entry.modelId} ${entry.canonicalId} ${entry.providerName} ${entry.family} ${kindLabels[entry.kind] ?? ""}`,
      );
      return words.every((word) => text.includes(word));
    })
    .sort((a, b) => Number(b.official) - Number(a.official));
}
