import assert from "node:assert/strict";
import { test } from "node:test";
import { allocateColumns, providerColumns, recordColumns } from "./columns.ts";

/** 通过实际分配器核对默认宽度预算、窄屏可读下限和宽屏上限。 */
test("默认窗口含240px侧栏可装下全部列，窄容器才出现预算溢出", () => {
  for (const columns of [providerColumns, recordColumns]) {
    for (const available of [899, 971, 1210]) {
      const widths = allocateColumns(columns, available, columns.map(() => 2000));
      assert.ok(widths.reduce((sum,value) => sum+value,0) <= available+.01);
      widths.forEach((width,index) => {
        assert.ok(width >= columns[index].min-.01);
        assert.ok(width <= Math.max(columns[index].min, Math.min(columns[index].max, available*columns[index].cap))+.01);
      });
    }
    const widths=allocateColumns(columns,600,[]);
    assert.deepEqual(widths,columns.map(column=>column.min));
  }
});

/** 短名称不强占百分比，长名称有上限，表格达到上限后余量留在表外。 */
test("内容决定名称宽度，有限舒展不会制造大段名称空白", () => {
  const short=allocateColumns(providerColumns,1200,[132,0,100]);
  const long=allocateColumns(providerColumns,1200,[2000,0,2000]);
  assert.equal(short[0],144);
  assert.equal(short[2],112);
  assert.ok(long[0] > short[0]);
  assert.ok(long[2] > short[2]);
  assert.ok(short.reduce((sum,value)=>sum+value,0)<1200);
  assert.deepEqual(allocateColumns(providerColumns,1200,[132,0,100]),short);
});

/** 实际恢复为1049px窗口时，默认侧栏与页面留白后约822px必须完整容纳九列。 */
test("使用记录1049px常用窗口无需横向滚动", () => {
  const widths = allocateColumns(recordColumns, 822, recordColumns.map(() => 2000));
  assert.ok(widths.reduce((sum, width) => sum + width, 0) <= 822.01);
});
