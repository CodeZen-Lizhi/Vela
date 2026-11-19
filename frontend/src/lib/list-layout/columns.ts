/** 单列预算包含单元格留白；名称按内容测量，其他列使用稳定的常规展示预算。 */
export interface ColumnBudget {
  /** 最小可读宽度、常规宽度与内容预算上限，单位逻辑像素。 */
  min: number;
  preferred: number;
  max: number;
  /** 占容器宽度的上限（0～1），不强制占满；下限优先。 */
  cap: number;
  /** 内容预算阶段的舒展权重；铺满阶段所有列按预算比例扩展。 */
  grow: number;
  /** 内容列由真实文字和装饰组合测量；其他列使用常规预算。 */
  content?: boolean;
}

/** 供应商八列预算；操作为四个32px按钮、三段2px间距及16px单元格留白。 */
export const providerColumns: ColumnBudget[] = [
  { min: 132, preferred: 140, max: 260, cap: .23, grow: 1, content: true },
  { min: 82, preferred: 94, max: 108, cap: .13, grow: 1 },
  { min: 100, preferred: 112, max: 220, cap: .20, grow: 1, content: true },
  { min: 70, preferred: 80, max: 94, cap: .11, grow: 1 },
  { min: 64, preferred: 70, max: 82, cap: .09, grow: 1 },
  { min: 132, preferred: 144, max: 166, cap: .18, grow: 1 },
  { min: 52, preferred: 56, max: 64, cap: .07, grow: 0 },
  { min: 150, preferred: 150, max: 158, cap: .18, grow: 0 },
];

/** 使用记录九列最小预算合计790px，适配1049px恢复窗口；模型测量包含胶囊，供应商测量包含Key倍率。 */
export const recordColumns: ColumnBudget[] = [
  { min: 74, preferred: 82, max: 94, cap: .10, grow: 1 },
  { min: 126, preferred: 160, max: 310, cap: .28, grow: 2, content: true },
  { min: 98, preferred: 120, max: 230, cap: .21, grow: 1, content: true },
  { min: 78, preferred: 96, max: 108, cap: .12, grow: 1 },
  { min: 66, preferred: 78, max: 88, cap: .10, grow: 1 },
  { min: 48, preferred: 58, max: 68, cap: .08, grow: 1 },
  { min: 100, preferred: 126, max: 144, cap: .15, grow: 1 },
  { min: 96, preferred: 108, max: 120, cap: .13, grow: 1 },
  { min: 104, preferred: 124, max: 142, cap: .15, grow: 1 },
];

/** 内容先受上下限约束，预算不足按剩余可收缩空间分摊；预算充足时先有限舒展，再按各列预算比例分配余量以铺满容器。 */
export function allocateColumns(columns: readonly ColumnBudget[], available: number, measured: readonly number[]): number[] {
  const tracks = columns.map((column, index) => {
    const desired = !column.content ? column.preferred : Math.max(column.min, measured[index] ?? column.min);
    // 名称短时最多增加12px呼吸空间，不把百分比上限变成保留空白。
    const limit = Math.max(column.min, Math.min(column.max, Math.max(available, 0) * column.cap, !column.content ? column.max : desired + 12));
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
  // 内容预算上限不限制表格总宽度；所有列同比例承接余量，避免整表居中及名称列独吞空白。
  const used = tracks.reduce((sum, track) => sum + track.width, 0);
  return tracks.map(track => used > 0 ? track.width * available / used : track.width);
}

/** 验证结果七列共用内容测量与比例铺满；模型不再独占剩余宽度。 */
export const verificationColumns: ColumnBudget[] = [
  { min: 104, preferred: 120, max: 200, cap: .20, grow: 1, content: true },
  { min: 140, preferred: 170, max: 280, cap: .26, grow: 1, content: true },
  { min: 110, preferred: 130, max: 230, cap: .22, grow: 1, content: true },
  { min: 96, preferred: 112, max: 140, cap: .15, grow: 1 },
  { min: 88, preferred: 96, max: 108, cap: .12, grow: 1 },
  { min: 116, preferred: 140, max: 156, cap: .17, grow: 1 },
  { min: 92, preferred: 92, max: 100, cap: .12, grow: 0 },
];

/** 检测计划五列按各自预算铺满，保留自动运行开关及两项操作的可读下限。 */
export const verificationPlanColumns: ColumnBudget[] = [
  { min: 104, preferred: 120, max: 220, cap: .25, grow: 1, content: true },
  { min: 88, preferred: 100, max: 120, cap: .18, grow: 1 },
  { min: 150, preferred: 164, max: 180, cap: .26, grow: 1 },
  { min: 116, preferred: 140, max: 156, cap: .22, grow: 1 },
  { min: 92, preferred: 92, max: 100, cap: .15, grow: 0 },
];
