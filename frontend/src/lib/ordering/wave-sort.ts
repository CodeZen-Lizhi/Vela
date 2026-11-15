/** 一行在拖动开始时的列表内纵向位置与高度，单位均为像素。 */
export interface SortRowGeometry {
  /** 行顶边相对列表顶边的距离。 */
  top: number;
  /** 行外框高度，包含边框。 */
  height: number;
}

/** 位移模型参数：邻近行给出幅度逐层衰减、启动逐层错开的上下波。 */
export const WAVE_SORT = Object.freeze({
  /** 邻近行最大起伏幅度，单位像素。 */
  amplitude: 18,
  /** 每拖动该距离，波峰沿列表推进一行，单位像素。 */
  crestDistance: 96,
  /** 波形沿传播方向的标准差，控制峰宽。 */
  width: 1.15,
  /** 每远一层额外延后启动的时间，单位毫秒。 */
  stepDelay: 24,
  /** 快速甩动时的落位时长上限增量，单位毫秒。 */
  maximumSettleExtension: 80,
});

/** 将拖动位移限制在列表首尾以内，并保留少量边界余量让卡片可被完整感知。 */
export function clampDragOffset(
  geometry: SortRowGeometry[],
  source: number,
  offset: number,
): number {
  const row = geometry[source];
  if (!row || geometry.length === 0) return 0;
  const listBottom = Math.max(...geometry.map((item) => item.top + item.height));
  const overshoot = 2;
  return Math.max(-row.top + overshoot, Math.min(listBottom - row.top - row.height - overshoot, offset));
}

/** 按被拖行中心越过相邻行中心的位置计算目标槽位，不依赖等高假设。 */
export function dragTargetIndex(
  geometry: SortRowGeometry[],
  source: number,
  offset: number,
): number {
  const active = geometry[source];
  if (!active) return source;
  const center = active.top + active.height / 2 + offset;
  let target = source;

  if (offset >= 0) {
    for (let index = source + 1; index < geometry.length; index += 1) {
      const row = geometry[index];
      if (!row || center < row.top + row.height / 2) break;
      target = index;
    }
    return target;
  }

  for (let index = source - 1; index >= 0; index -= 1) {
    const row = geometry[index];
    if (!row || center > row.top + row.height / 2) break;
    target = index;
  }
  return target;
}

/**
 * 计算普通行需要让出的整格位移。
 * pitch 是被拖行的外框高度加一格间距；目标变化时由 CSS 过渡把让位抹平。
 */
export function slotShiftForRow(
  source: number,
  target: number,
  index: number,
  pitch: number,
): number {
  if (target > source && index > source && index <= target) return -pitch;
  if (target < source && index >= target && index < source) return pitch;
  return 0;
}

/**
 * 计算紧邻被拖行上下两行的波浪起伏。
 * 返回值是相对整格让位之外的额外纵向位移；方向和拖动方向一致，近邻先动且幅度更大。
 */
export function neighborWaveOffset(
  source: number,
  index: number,
  offset: number,
): { distance: number; delay: number; value: number } {
  const relative = index - source;
  if (relative === 0 || Math.abs(relative) > 1) return { distance: 0, delay: 0, value: 0 };

  const direction = offset >= 0 ? 1 : -1;
  const distance = direction > 0 ? relative + 1 : 1 - relative;
  const crest = Math.abs(offset) / WAVE_SORT.crestDistance;
  const pulse = Math.exp(-(((distance - crest) / WAVE_SORT.width) ** 2));
  const decay = 0.7 ** Math.max(0, distance);
  return {
    distance,
    delay: Math.round(WAVE_SORT.stepDelay * distance),
    value: direction * WAVE_SORT.amplitude * pulse * decay,
  };
}

/** 快甩落位稍长并轻微过冲，慢放直接收住；速度由相邻位移差近似，不读取系统时钟。 */
export function settleTiming(flick: number): { duration: number; easing: string } {
  return {
    duration: 140 + Math.min(WAVE_SORT.maximumSettleExtension, Math.max(0, flick) * 3),
    easing: flick > 8
      ? "cubic-bezier(0.22, 1.12, 0.36, 1)"
      : "cubic-bezier(0.25, 0.8, 0.3, 1)",
  };
}
