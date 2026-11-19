import { tick } from "svelte";
import { allocateColumns, type ColumnBudget } from "./columns";

/** 表格内容测量参数；identity只在允许重新测量的业务边界改变。 */
export interface TableLayoutOptions {
  /** 与colgroup顺序一致的列预算。 */
  columns: readonly ColumnBudget[];
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
      measured = measureColumns(node, options.columns);
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

/** 测量文字的真实字体、间距、边框和留白；装饰元素直接读取实际布局，不估算固定附加宽度。 */
function textWidth(element: HTMLElement, context: CanvasRenderingContext2D): number {
  const style = getComputedStyle(element);
  const text = element.textContent ?? "";
  context.font = style.font;
  const spacing = style.letterSpacing === "normal" ? 0 : parseFloat(style.letterSpacing);
  return context.measureText(text).width + Math.max(0, Array.from(text).length - 1) * spacing
    + parseFloat(style.paddingLeft) + parseFloat(style.paddingRight)
    + parseFloat(style.borderLeftWidth) + parseFloat(style.borderRightWidth);
}

/** 同一列取实际信息行的最大宽度，再加入该单元格留白；隐藏的供应商全集样本也沿用真实组件。 */
export function measureColumns(node: HTMLElement, columns: readonly ColumnBudget[]): number[] {
  const context = document.createElement("canvas").getContext("2d");
  if (!context) throw new Error("无法初始化列表文字测量");
  const widths = columns.map(column => column.content ? column.min : column.preferred);
  for (const line of node.querySelectorAll<HTMLElement>("[data-column-line]")) {
    const cell = line.closest("td");
    if (!cell || !columns[cell.cellIndex]?.content) continue;
    const style = getComputedStyle(line);
    const parts = line.hasAttribute("data-column-text")
      ? [line]
      : Array.from(line.children).filter((child): child is HTMLElement =>
          child instanceof HTMLElement && getComputedStyle(child).display !== "none");
    const gap = style.columnGap === "normal" ? 0 : parseFloat(style.columnGap);
    let width = Math.max(0, parts.length - 1) * gap;
    for (const part of parts) {
      const partStyle = getComputedStyle(part);
      const contentWidth = part.hasAttribute("data-column-text")
        ? textWidth(part, context)
        : part.getBoundingClientRect().width;
      width += contentWidth + parseFloat(partStyle.marginLeft) + parseFloat(partStyle.marginRight);
    }
    const cellStyle = getComputedStyle(cell);
    width += parseFloat(cellStyle.paddingLeft) + parseFloat(cellStyle.paddingRight);
    widths[cell.cellIndex] = Math.max(widths[cell.cellIndex] ?? 0, width);
  }
  return widths;
}
