<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import type { Attempt, Candidate, RecordsRecord } from "$lib/generated/mygo";
  import * as Card from "$lib/components/ui/card";
  import * as Collapsible from "$lib/components/ui/collapsible";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import { Hint } from "$lib/components/ui/tooltip";
  import { ArrowDown, Check, X, Clock3, ShieldAlert, ChevronDown } from "@lucide/svelte";
  import { date, duration, label, states, protocol, number, pretty } from "./display";
  import { pathEntries, entryError, protectionRemaining, type PathEntry } from "./execution";
  import { elapsedClock } from "./elapsed-clock.svelte";

  /** 请求执行路径沿用 Aether 的供应商轮次、Key 卡片和点击检查器，跳过节点不冒充出站。 */
  let { record, attempt = $bindable("0"), onresponse, onsource }: {
    /** 当前请求。 */ record: RecordsRecord;
    /** 更多数据中选择的真实尝试索引。 */ attempt?: string;
    /** 查看当前真实尝试的响应。 */ onresponse: () => void;
    /** 查看已知的熔断触发请求。 */ onsource: (id: string) => void;
  } = $props();
  /** 每次刷新由记录派生路径，节点按稳定身份保持展开。 */
  const entries = $derived(pathEntries(record));
  /** 连续供应商合并为一轮，同一供应商再次出现仍沿用其颜色。 */
  const groups = $derived.by(() => {
    const result: { id: string; name: string; tone: number; entries: PathEntry[] }[] = [];
    const tones = new Map<string, number>();
    for (const entry of entries) {
      const id = entry.item.providerId || entry.item.providerName;
      if (!tones.has(id)) tones.set(id, tones.size % 6);
      let group = result.at(-1);
      if (!group || group.id !== id) {
        group = { id, name: entry.item.providerName || "未知供应商", tone: tones.get(id)!, entries: [] };
        result.push(group);
      }
      group.entries.push(entry);
    }
    return result;
  });
  /** 与 Aether 共用入口/供应商/连线比例，较长路径仅在自身滚动。 */
  const columns = $derived(["minmax(140px,.9fr)", ...groups.flatMap(() => ["minmax(32px,.45fr)", "minmax(220px,1.55fr)"]), "minmax(32px,.45fr)", "minmax(140px,.9fr)"].join(" "));
  /** 默认收起，刷新后仍选择相同尝试，关闭后不暗中切换到其他 Key。 */
  let inspected = $state<string | null>(null);
  const current = $derived(entries.find(entry => entry.id === inspected));
  const actual = $derived(current?.index == null ? undefined : current.item as Attempt);
  const local = $derived(current?.index == null ? current?.item as Candidate | undefined : undefined);
  /** 已结束请求的冷却倒计时使用独立秒级时钟，不触发请求详情轮询。 */
  let now = $state(Date.now());
  $effect(() => {
    if (!entries.some(entry => entry.item.cooldownUntil > now || entry.item.nextProbe > now)) return;
    const timer = setInterval(() => { now = Date.now(); }, 1000);
    return () => clearInterval(timer);
  });
  const clock = elapsedClock(() => record.state === "active");
  /** 卡片状态包含文本与图标，不能只凭颜色辨认。 */
  function status(entry: PathEntry): string {
    if (entry.state === "local") return (entry.item.circuitOpen || (entry.item as Candidate).reason?.includes("熔断")) ? "本地熔断" : entry.item.cooldownUntil ? "本地限流" : "未发送";
    const item = entry.item as Attempt;
    if (entry.state === "pending") return item.status ? `${item.status} · 进行中` : "进行中";
    return item.status ? String(item.status) : "未收到响应";
  }
  /** 点击只绑定真实尝试，纯本地条目不改变上游数据源。 */
  function select(entry: PathEntry): void {
    inspected = inspected === entry.id ? null : entry.id;
    if (entry.index != null) attempt = String(entry.index);
  }
</script>

<section class="record-execution" aria-label="实际执行路径">
  <header class="record-execution-heading"><div><h3>真实执行路径</h3><p>请求入口 → 供应商与 Key → 请求结果</p></div><Badge variant="neutral">{record.attempts.length} 次上游请求{#if !record.attempts.length && entries.length} · {entries.length} 个本地跳过{/if}</Badge></header>
  <!-- 横向路径独立滚动，键盘可聚焦后查看后续轮次。 -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div class="record-path-scroll" role="region" aria-label="横向执行路径" tabindex="0">
    <div class="record-path-stage" style:grid-template-columns={columns} data-result={record.state}>
      <Card.Root variant="path" class="record-path-node" aria-label="请求入口"><Card.Content><small>请求入口</small><span class="record-path-entry-line">{#if record.method}<b>{record.method}</b>{/if}<code title={record.path || record.attempts[0]?.path}>{record.path || record.attempts[0]?.path || "未记录请求路径"}</code></span></Card.Content></Card.Root>
      {#each groups as group, groupIndex}
        <span aria-hidden="true" class="record-path-arrow"></span>
        <section class="record-provider-round" data-tone={group.tone} aria-label={`供应商 ${groupIndex + 1} ${group.name}`}>
          <h4><span>{String(groupIndex + 1).padStart(2, "0")}</span><Hint overflowOnly text={group.name}>{#snippet children(props)}<strong {...props} class="truncate">{group.name}</strong>{/snippet}</Hint></h4>
          <div class="record-attempt-stack">
            {#each group.entries as entry, keyIndex (entry.id)}
              {#if keyIndex}<ArrowDown aria-hidden="true" class="record-key-hop" />{/if}
              <Button variant="attempt" data-selected={inspected === entry.id} data-result={entry.state} size="attempt" class="record-attempt" aria-expanded={inspected === entry.id} aria-controls="record-execution-inspector" aria-label={`查看 ${entry.item.keyName || "未知 Key"} · ${status(entry)} · ${entry.state === "failed" ? "失败" : entry.state === "success" ? "成功" : entry.state === "pending" ? "进行中" : "未发上游"}`} onclick={() => select(entry)}>
                <span class="record-attempt-head"><span>{#if entry.state === "success"}<Check />{:else if entry.state === "failed"}<X />{:else if entry.state === "local"}<ShieldAlert />{:else}<Clock3 />{/if}{status(entry)}</span><small>{String(keyIndex + 1).padStart(2, "0")}</small></span>
                <span class="record-attempt-body"><span class="record-attempt-identity"><span><small>Key</small><strong title={entry.item.keyName}>{entry.item.keyName || "未知 Key"}</strong></span><small>{protocol(record.protocol)}</small></span><span class="record-attempt-metrics"><span><small>{entry.index == null ? "上游" : "耗时"}</small><b>{entry.index == null ? "未请求" : duration(entry.state === "pending" ? Math.max(0, clock.now - (entry.item as Attempt).startedAt) : (entry.item as Attempt).durationMillis)}</b></span>{#if entry.index != null && (entry.item as Attempt).firstByteMillis != null}<span><small>首字</small><b>{duration((entry.item as Attempt).firstByteMillis)}</b></span>{/if}</span></span>
                {#if entry.item.cooldownUntil || entry.item.circuitOpen}<span class="record-attempt-protection">{#if entry.item.cooldownUntil}<span>限流冷却至 {date(entry.item.cooldownUntil)}</span><span>{protectionRemaining(entry.item.cooldownUntil, now)}</span>{/if}{#if entry.item.circuitOpen}<span>本地熔断{entry.item.nextProbe ? ` · 下次探测 ${date(entry.item.nextProbe)}` : " · 恢复试探中"}</span>{#if entry.item.nextProbe}<span>{protectionRemaining(entry.item.nextProbe, now)}</span>{/if}{/if}</span>{/if}
                {#if entryError(entry)}<span class="record-attempt-reason" title={entryError(entry)}>{entryError(entry)}</span>{/if}
              </Button>
            {/each}
          </div>
        </section>
      {/each}
      <span aria-hidden="true" class="record-path-arrow"></span>
      <Card.Root variant="path" class="record-path-node" aria-label="请求结果"><Card.Content><small>请求结果</small><Badge variant={record.state === "success" ? "positive" : record.state === "failed" ? "negative" : record.state === "local" ? "warning" : "processing"}>{label(states, record.state)}</Badge><span>总耗时 {duration(record.state === "active" ? Math.max(0, clock.now - record.startedAt) : record.durationMillis)}</span></Card.Content></Card.Root>
    </div>
  </div>
  {#if !entries.length}<Alert.Root variant="info"><Alert.Description>没有已发送的上游请求。{record.error || "未记录可展示的本地跳过原因。"}</Alert.Description></Alert.Root>{/if}
  {#if current}
    <section id="record-execution-inspector" class="record-attempt-inspector" aria-label="Key 执行详情" aria-live="polite">
      <dl class="record-context-grid">
        <div><dt>供应商 / Key</dt><dd>{current.item.providerName} / {current.item.keyName || "—"}</dd></div>
        <div><dt>HTTP / 执行结果</dt><dd>{status(current)}{actual ? ` · ${duration(actual.durationMillis)}` : " · 未请求上游"}</dd></div>
        {#if actual}<div><dt>请求时间</dt><dd>{date(actual.startedAt, "day")} {date(actual.startedAt)} → {current.state === "pending" ? "进行中" : date(actual.endedAt || actual.startedAt + actual.durationMillis)}</dd></div><div><dt>首字 / 倍率</dt><dd>{duration(actual.firstByteMillis)} · {actual.multiplier == null ? "倍率未知" : `${actual.multiplier}×`}</dd></div>{/if}
        {#if current.item.cooldownUntil}<div><dt>限流时间</dt><dd>冷却至 {date(current.item.cooldownUntil, "day")} {date(current.item.cooldownUntil)}</dd></div><div><dt>剩余冷却</dt><dd>{protectionRemaining(current.item.cooldownUntil, now)}</dd></div>{/if}
        {#if current.item.circuitOpen}<div><dt>熔断保护</dt><dd>{current.item.nextProbe ? `下次探测 ${date(current.item.nextProbe)} · ${protectionRemaining(current.item.nextProbe, now)}` : "恢复试探中"}</dd></div>{/if}
      </dl>
      {#if actual}<code>{actual.path}</code>{/if}
      {#if local}<Alert.Root variant="info"><Alert.Description>本次没有上游响应，也不会消耗上游额度。{local.reason}{#if local.circuitSourceId}<Button variant="link" size="inline" onclick={() => onsource(local!.circuitSourceId)}>查看触发熔断的请求</Button>{/if}</Alert.Description></Alert.Root>{:else if current.state === "failed"}<Alert.Root variant="destructive"><Alert.Description>{entryError(current) || "上游执行失败，未记录错误内容。"}</Alert.Description></Alert.Root>{/if}
      {#if actual?.imageProgress}<section class="record-image-progress" aria-label="图片生成进度"><header><h4>图片生成进度</h4><Badge variant={actual.imageProgress.phase === "failed" ? "negative" : actual.imageProgress.phase === "completed" ? "positive" : "processing"}>{({connecting:"连接上游",streaming:"上游生成中",completed:"上游已完成",failed:"失败"})[actual.imageProgress.phase] || actual.imageProgress.phase}</Badge></header><dl class="record-context-grid"><div><dt>上游首字</dt><dd>{duration(actual.firstByteMillis)}</dd></div><div><dt>上游事件帧</dt><dd>{number(actual.imageProgress.frames)}</dd></div><div><dt>预览图片</dt><dd>{number(actual.imageProgress.partialImages)}</dd></div><div><dt>最后上游事件</dt><dd>{actual.imageProgress.lastEvent || "未收到事件"}{#if actual.imageProgress.lastEventAt} · {date(actual.imageProgress.lastEventAt)}{/if}</dd></div></dl></section>{/if}
      {#if actual?.usage}<div class="record-execution-usage"><span>输入 {number(actual.usage.input)}</span><span>输出 {number(actual.usage.output)}</span><span>缓存创建 {number(actual.usage.cacheWrite)}</span><span>缓存读取 {number(actual.usage.cacheRead)}</span></div>{/if}
      {#if actual?.payload?.responseBody && current.state === "failed"}<Collapsible.Root><Collapsible.Trigger>{#snippet child({ props })}<Button {...props} variant="text" size="inline">上游响应<ChevronDown /></Button>{/snippet}</Collapsible.Trigger><Collapsible.Content><pre>{pretty(actual.payload.responseBody)}</pre></Collapsible.Content></Collapsible.Root>{/if}
      {#if actual}<Button variant="text" size="inline" onclick={onresponse}>查看本次响应</Button>{/if}
    </section>
  {/if}
</section>
