/** 在组件初始化时创建展示时钟；仅有进行中请求时逐帧更新，停止或卸载时释放帧回调。 */
export function elapsedClock(active: () => boolean): { readonly now: number } {
  let now = $state(Date.now());
  $effect(() => {
    if (!active()) return;
    let frame = 0;
    /** 只推进本地显示时间，不改变请求状态，也不触发后端查询。 */
    function tick(): void {
      now = Date.now();
      frame = requestAnimationFrame(tick);
    }
    tick();
    return () => cancelAnimationFrame(frame);
  });
  return {
    /** 当前毫秒时刻，仅用于读取进行中请求的展示耗时。 */
    get now() { return now; },
  };
}
