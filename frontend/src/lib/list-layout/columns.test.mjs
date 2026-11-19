import assert from "node:assert/strict";
import { test } from "node:test";
import { allocateColumns, providerColumns, recordColumns, verificationColumns, verificationPlanColumns } from "./columns.ts";

/** 通过实际分配器核对默认宽度预算、窄屏可读下限和宽屏上限。 */
test("默认窗口含240px侧栏可装下全部列，窄容器才出现预算溢出", () => {
  for (const columns of [providerColumns, recordColumns, verificationColumns, verificationPlanColumns]) {
    for (const available of [899, 971, 1210]) {
      const widths = allocateColumns(columns, available, columns.map(() => 2000));
      assert.ok(widths.reduce((sum,value) => sum+value,0) <= available+.01);
      assert.ok(Math.abs(widths.reduce((sum,value) => sum+value,0)-available)<.01);
      widths.forEach((width,index) => {
        assert.ok(width >= columns[index].min-.01);

      });
    }
    const widths=allocateColumns(columns,400,[]);
    assert.deepEqual(widths,columns.map(column=>column.min));
  }
});

/** 短长内容保留各自预算比例，宽窗口所有列共同铺满，不能留下居中窄表。 */
test("宽窗口表格铺满容器，余量按各列预算比例分配", () => {
  for (const columns of [providerColumns, recordColumns, verificationColumns, verificationPlanColumns]) {
    for (const available of [973, 1573, 3000]) {
      const short = allocateColumns(columns, available, columns.map(column => column.min));
      const long = allocateColumns(columns, available, columns.map(() => 2000));
      for (const widths of [short, long]) {
        assert.ok(Math.abs(widths.reduce((sum, width) => sum + width, 0) - available) < .01);
        assert.ok(widths.every((width, index) => width >= columns[index].min));
      }
      assert.ok(long[columns.findIndex(column => column.content)] > short[columns.findIndex(column => column.content)]);
    }
    const base = allocateColumns(columns, 3000, []);
    const wide = allocateColumns(columns, 6000, []);
    base.forEach((width, index) => assert.ok(Math.abs(wide[index] / width - 2) < .001));
  }
});

/** 实际恢复为1049px窗口时，默认侧栏与页面留白后约822px必须完整容纳九列。 */
test("使用记录1049px常用窗口无需横向滚动", () => {
  const widths = allocateColumns(recordColumns, 822, recordColumns.map(() => 2000));
  assert.ok(widths.reduce((sum, width) => sum + width, 0) <= 822.01);
});
