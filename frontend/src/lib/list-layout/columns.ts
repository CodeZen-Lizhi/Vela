/** 单列预算包含单元格留白；名称按内容测量，其他列使用稳定的常规展示预算。 */
export interface ColumnBudget {
  /** 最小可读宽度、常规宽度与绝对上限，单位逻辑像素。 */
  min: number;
  preferred: number;
  max: number;
  /** 占容器宽度的上限（0～1），不强制占满；下限优先。 */
  cap: number;
  /** 额外空间的分配权重；零表示不舒展。 */
  grow: number;
  /** 名称文字之外的图标、标签及留白；未设置表示不测量内容。 */
  chrome?: number;
}

/** 供应商八列预算；操作为四个32px按钮、三段2px间距及16px单元格留白。 */
export const providerColumns: ColumnBudget[] = [
  { min: 132, preferred: 140, max: 260, cap: .23, grow: 1, chrome: 82 },
  { min: 82, preferred: 94, max: 108, cap: .13, grow: 1 },
  { min: 100, preferred: 112, max: 220, cap: .20, grow: 1, chrome: 20 },
  { min: 70, preferred: 80, max: 94, cap: .11, grow: 1 },
  { min: 64, preferred: 70, max: 82, cap: .09, grow: 1 },
  { min: 132, preferred: 144, max: 166, cap: .18, grow: 1 },
  { min: 52, preferred: 56, max: 64, cap: .07, grow: 0 },
  { min: 150, preferred: 150, max: 158, cap: .18, grow: 0 },
];

/** 使用记录九列预算；模型测量包含思考强度/层级胶囊，供应商测量包含Key倍率。 */
export const recordColumns: ColumnBudget[] = [
  { min: 78, preferred: 82, max: 94, cap: .10, grow: 1 },
  { min: 142, preferred: 160, max: 310, cap: .28, grow: 2, chrome: 90 },
  { min: 108, preferred: 120, max: 230, cap: .21, grow: 1, chrome: 65 },
  { min: 88, preferred: 96, max: 108, cap: .12, grow: 1 },
  { min: 70, preferred: 78, max: 88, cap: .10, grow: 1 },
  { min: 52, preferred: 58, max: 68, cap: .08, grow: 1 },
  { min: 110, preferred: 126, max: 144, cap: .15, grow: 1 },
  { min: 102, preferred: 108, max: 120, cap: .13, grow: 1 },
  { min: 112, preferred: 124, max: 142, cap: .15, grow: 1 },
];

/** 内容先受上下限约束，预算不足按剩余可收缩空间分摊；预算充足时有限舒展，余量留在表外。 */
export function allocateColumns(columns: readonly ColumnBudget[], available: number, measured: readonly number[]): number[] {
  const tracks = columns.map((column, index) => {
    const desired = column.chrome === undefined ? column.preferred : Math.max(column.min, measured[index] ?? column.min);
    // 名称短时最多增加12px呼吸空间，不把百分比上限变成保留空白。
    const limit = Math.max(column.min, Math.min(column.max, Math.max(available, 0) * column.cap, column.chrome === undefined ? column.max : desired + 12));
    return { column, limit, width: Math.max(column.min, Math.min(limit, desired)) };
  });
  const total = tracks.reduce((sum, track) => sum + track.width, 0);
  if (total > available) {
    const capacity = tracks.reduce((sum, track) => sum + track.width - track.column.min, 0);
    const reduction = Math.min(total - available, capacity);
    return tracks.map(({width, column}) => capacity ? width - reduction * (width - column.min) / capacity : width);
  }
  let remaining = available - total;
  // 每轮至少有一列达到上限；轮数最多为列数，不依赖像素逐次迭代。
  for (let pass = 0; pass < columns.length && remaining > .01; pass++) {
    const active = tracks.filter(track => track.width + .01 < track.limit && track.column.grow > 0);
    const weight = active.reduce((sum, track) => sum + track.column.grow, 0);
    if (!weight) break;
    let added = 0;
    for (const track of active) {
      const increment = Math.min(track.limit - track.width, remaining * track.column.grow / weight);
      track.width += increment;
      added += increment;
    }
    remaining -= added;
  }
  return tracks.map(track => track.width);
}
