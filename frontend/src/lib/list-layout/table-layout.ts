import { tick } from "svelte";
import { allocateColumns, type ColumnBudget } from "./columns";

/** 表格内容测量参数；identity只在允许重新测量的业务边界改变。 */
export interface TableLayoutOptions {
  /** 与colgroup顺序一致的列预算及逐列完整文字样本。 */
  columns: readonly ColumnBudget[];
  samples: readonly (readonly string[])[];
  /** 查询身份或供应商名称集合；旧查询加载期间ready必须为false。 */
  identity: string;
  ready: boolean;
}

/** 在既有列表滚动容器上控制同一张shadcn表格；仅内容周期重测，容器缩放复用原测量结果。 */
export function tableLayout(node: HTMLElement, initial: TableLayoutOptions) {
  let options = initial;
  let identity: string | undefined;
  let measured: number[] = [];
  let disposed = false;
  let generation = 0;

  /** 只写col与表格宽度，不改数据、字号、控件或滚动位置。 */
  function layout(): void {
    const table = node.querySelector("table");
    if (!table || !node.clientWidth) return;
    const widths = allocateColumns(options.columns, node.clientWidth, measured);
    table.style.width = `${widths.reduce((sum, width) => sum + width, 0)}px`;
    table.querySelectorAll("col").forEach((column, index) => { column.style.width = `${widths[index]}px`; });
  }

  /** 等待真实字体和当前DOM提交后冻结本周期文字需求；异步旧周期不会回写新布局。 */
  async function update(next: TableLayoutOptions): Promise<void> {
    options = next;
    const version = ++generation;
    await tick();
    await document.fonts.ready;
    if (disposed || version !== generation) return;
    if (options.ready && identity !== options.identity) {
      const table = node.querySelector("table");
      if (!table) return;
      const context = document.createElement("canvas").getContext("2d");
      if (!context) return;
      const style = getComputedStyle(table.querySelector("tbody td") ?? table);
      context.font = `600 ${style.fontSize} ${style.fontFamily}`;
      measured = options.columns.map((column, index) => {
        const chrome = column.chrome;
        if (chrome === undefined) return column.preferred;
        return (options.samples[index] ?? []).reduce((width, text) => Math.max(width, context.measureText(text).width + chrome), column.min);
      });
      identity = options.identity;
    }
    layout();
  }

  const observer = new ResizeObserver(layout);
  observer.observe(node);
  void update(initial);
  return {
    update: (next: TableLayoutOptions) => { void update(next); },
    destroy: () => { disposed = true; generation++; observer.disconnect(); },
  };
}
