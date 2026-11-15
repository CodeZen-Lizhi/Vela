import assert from "node:assert/strict";
import { test } from "node:test";
import {
  clampDragOffset,
  dragTargetIndex,
  neighborWaveOffset,
  settleTiming,
  slotShiftForRow,
} from "./wave-sort.ts";

const geometry = [
  { top: 0, height: 40 },
  { top: 46, height: 40 },
  { top: 92, height: 40 },
  { top: 138, height: 40 },
];

/** 被拖行中心越过相邻行中心时切换目标，并支持一次跨过多行。 */
test("按真实行中心计算拖动目标和边界", () => {
  assert.equal(dragTargetIndex(geometry, 1, 20), 1);
  assert.equal(dragTargetIndex(geometry, 1, 47), 2);
  assert.equal(dragTargetIndex(geometry, 1, 92), 3);
  assert.equal(dragTargetIndex(geometry, 2, -47), 1);
  assert.equal(clampDragOffset(geometry, 1, -100), -44);
  assert.equal(clampDragOffset(geometry, 1, 200), 90);
});

/** 普通行按完整一格的间距让位，跨越方向的中间行形成连续空槽。 */
test("普通行按拖动方向整格让位", () => {
  assert.equal(slotShiftForRow(1, 3, 2, 46), -46);
  assert.equal(slotShiftForRow(1, 3, 3, 46), -46);
  assert.equal(slotShiftForRow(3, 1, 2, 46), 46);
  assert.equal(slotShiftForRow(1, 3, 0, 46), 0);
});

/** 邻近行只做同方向起伏，波峰随拖动距离推进且远端幅度更小。 */
test("相邻行形成同向衰减波且不包含左右位移", () => {
  const up = neighborWaveOffset(2, 1, 48);
  const down = neighborWaveOffset(2, 3, 48);
  const upward = neighborWaveOffset(2, 1, -48);
  assert.ok(up.value > 0);
  assert.ok(down.value > 0);
  assert.ok(upward.value < 0);
  assert.ok(Math.abs(up.value) > Math.abs(down.value));
  assert.ok(up.delay < down.delay);
  assert.deepEqual(neighborWaveOffset(2, 0, 48), { distance: 0, delay: 0, value: 0 });
});

/** 快甩仍使用有限时长，静止松手直接收住且不留下常驻动画。 */
test("松手速度只调整有限时长与回弹曲线", () => {
  assert.deepEqual(settleTiming(0), { duration: 140, easing: "cubic-bezier(0.25, 0.8, 0.3, 1)" });
  assert.deepEqual(settleTiming(100), { duration: 220, easing: "cubic-bezier(0.22, 1.12, 0.36, 1)" });
});
