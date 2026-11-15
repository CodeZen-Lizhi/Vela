<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { Hint } from "$lib/components/ui/tooltip";
  import { VelaService, type RecordsRecord } from "$lib/generated/mygo";
  import { tick } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import * as Card from "$lib/components/ui/card";
  import * as Sheet from "$lib/components/ui/sheet";
  import * as Collapsible from "$lib/components/ui/collapsible";
  import * as Table from "$lib/components/ui/table";
  import * as Tabs from "$lib/components/ui/tabs";
  import { X } from "@lucide/svelte";
  import { errorMessage } from "$lib/settings/sections";
  import { rateNames, rateUnit, unitLabel } from "$lib/models/display";
  import Exchange from "./Exchange.svelte";
  import ExecutionPath from "./ExecutionPath.svelte";
  import { elapsedClock } from "./elapsed-clock.svelte";
  import {
    states,
    protocol,
    label,
    number,
    compact,
    money,
    duration,
    date,
  } from "./display";
  /** 单次请求详情按身份加载，保留真实尝试顺序与正文到期状态。 */
  let {
    id,
    onclose,
    notify,
  }: {
    /** 请求 ID 为空时关闭。 */ id: string;
    /** 关闭返回原列表。 */ onclose: () => void;
    /** 操作反馈。 */ notify: (message: string) => void;
  } = $props();
  /** 详情状态与异步代次，切换记录时不展示旧响应。 */
  let record = $state<RecordsRecord | null>(null),
    error = $state(""),
    loading = $state(false),
    attempt = $state("0"),
    source = $state("client");
  /** 返回原列表行的身份独立于熔断追溯身份。 */
  let originID = "";
  /** 三个页签按执行链路、请求响应和计费组织详情，默认先查看真实执行链路。 */
  let section = $state("execution");
  /** 与列表共用逐帧时钟语义，按真实起点累计，不推断完成状态。 */
  const clock = elapsedClock(() => record?.state === "active");
  let generation = 0;
  /** 当前阅读身份允许追溯熔断来源。 */
  let viewedID = $state("");
  /** 头部状态来自最后一次真实 HTTP，逻辑失败仍单独显示最终状态。 */
  const lastStatus = $derived(record?.attempts.at(-1)?.status);
  const elapsed = $derived(record?.state === "active" ? Math.max(0, clock.now - record.startedAt) : record?.durationMillis);
  /** 详情失败可重试，活跃时持续读取到真实终态。 */
  async function load(requestID: string): Promise<void> {
    const version = ++generation;
    loading = true;
    try {
      const next = await VelaService.requestRecord(requestID);
      if (version !== generation) return;
      record = next;
      error = "";
    } catch (cause) {
      if (version === generation) error = errorMessage(cause);
    } finally {
      if (version === generation) loading = false;
    }
  }
  $effect(() => {
    const requestID = id;
    if (!requestID) {
      record = null;
      generation++;
      return;
    }
    record = null;
    error = "";
    attempt = "0";
    source = "client";
    section = "execution";
    originID = requestID;
    viewedID = requestID;
    void load(requestID);
    const timer = setInterval(() => {
      if (record?.state === "active" && !loading) void load(viewedID);
    }, 1000);
    return () => {
      clearInterval(timer);
      generation++;
    };
  });
  /** 切换熔断来源时先清除旧事实，失败不把旧请求冒充来源请求。 */
  function openSource(requestID: string): void {
    generation++;
    record = null;
    error = "";
    attempt = "0";
    section = "execution";
    viewedID = requestID;
    void load(requestID);
  }
  /** 关闭抽屉后恢复原行焦点，行已不在当前页时回到导航入口。 */
  function restoreFocus(event: Event): void {
    event.preventDefault();
    const row = document.querySelector<HTMLElement>(`.record-row[data-record-id="${CSS.escape(originID)}"]`);
    (row ?? document.querySelector<HTMLElement>('[aria-label="使用记录"]'))?.focus();
  }
  /** 路径入口展开对应尝试的真实响应，并把数据区带入可见范围。 */
  async function showResponse(): Promise<void> {
    source = `attempt:${attempt}`;
    section = "payload";
    await tick();
    document.querySelector(".record-detail-tabs")?.scrollIntoView({ block: "nearest" });
  }
  /** 复制只处理已脱敏可见文本，不发送网络调用。 */
  async function copy(value: string): Promise<void> {
    try {
      await VelaService.copyText(value);
      notify("已复制");
    } catch (cause) {
      notify(errorMessage(cause));
    }
  }
</script>

<Sheet.Root open={!!id} onOpenChange={(open) => { if (!open) onclose(); }}>
  <Sheet.Content side="right" class="record-detail" showCloseButton={false} onCloseAutoFocus={restoreFocus}>
    <header class="record-detail-header">
      <div class="record-detail-title-row">
        <div class="record-detail-title-main">
          <Sheet.Title>请求详情</Sheet.Title>
          {#if record}
            <Hint overflowOnly text={record.model}>{#snippet children(hintProps0)}{#if record}<Badge {...hintProps0} variant="neutral" class="max-w-full truncate">{record.model || "未知模型"}</Badge>{/if}{/snippet}</Hint>
            {#if record.effort}<Badge variant="parameter">{record.effort}</Badge>{/if}
            {#if record.tier && record.tier !== "standard"}<Badge variant="parameter">{record.tier}</Badge>{/if}
            <Badge variant={record.state === "failed" ? "negative" : record.state === "success" ? "positive" : record.state === "local" ? "warning" : "processing"}>{label(states, record.state)}{lastStatus ? ` · ${lastStatus}` : ""}</Badge>
            <Badge variant={record.stream || record.transport === "websocket" ? "transport" : "metadata"}>{record.transport === "websocket" ? "WebSocket" : record.stream ? "HTTP 流式" : "HTTP 标准"}</Badge>
          {/if}
        </div>
        <div class="records-actions">
          <Button onclick={onclose} variant="ghost" size="icon-sm" aria-label="关闭请求详情"  ><X /></Button>
        </div>
      </div>
      <Sheet.Description class="record-detail-meta">
        <Hint text={`ID: ${record?.id ?? viewedID}`}>{#snippet children(props)}<span {...props} class="truncate">ID: {record?.id ?? viewedID}</span>{/snippet}</Hint>
        {#if record}<span>{date(record.startedAt, "day")} {date(record.startedAt)}</span><span>{protocol(record.protocol)}</span>{/if}
      </Sheet.Description>
    </header>
    <div class="record-detail-body">
      {#if error}<Alert.Root variant="destructive"><Alert.Description>{error}<Button variant="outline" onclick={() => load(viewedID)}>重试</Button></Alert.Description></Alert.Root>{/if}
      {#if loading && !record}<p role="status">正在读取请求详情…</p>{/if}
      {#if record}
        <section class="record-detail-metrics" aria-label="费用与性能概览">
          <Card.Root size="sm"><Card.Content><span>总费用</span><strong class="text-parameter">{money(record.cost)}</strong></Card.Content></Card.Root>
          <Card.Root size="sm"><Card.Content><span>总耗时</span><strong>{duration(elapsed)}</strong></Card.Content></Card.Root>
          <Card.Root size="sm"><Card.Content><span>{record.usage?.unit ? "计费数量" : "输出 Token"}</span><strong>{record.usage?.unit ? `${number(record.usage.quantity)} ${record.usage.unit === "image" ? "张" : "秒"}` : compact(record.usage?.output)}</strong></Card.Content></Card.Root>
          <Card.Root size="sm"><Card.Content><span>输出速度</span><strong>{record.outputRate == null ? "—" : `${compact(record.outputRate)} TPS`}</strong></Card.Content></Card.Root>
          <Card.Root size="sm"><Card.Content><span>首字耗时</span><strong>{duration(record.firstByteMillis)}</strong></Card.Content></Card.Root>
        </section>
        {#if record.error}<Alert.Root variant="destructive"><Alert.Description>{record.error}</Alert.Description></Alert.Root>{/if}
        {#if !record.usage}<p class="record-detail-note">{record.state === "active" ? "请求进行中，用量与估算费用尚未结算。" : record.state === "local" ? "请求未发上游，无上游用量。" : "上游未提供可验证用量，未知值不显示为零。"}</p>{/if}
        {#if record.circuitSourceId}<Alert.Root variant="info"><Alert.Description>本地熔断 · 触发请求 <Button variant="link" size="inline" onclick={() => openSource(record!.circuitSourceId)}>{record.circuitSourceId}</Button></Alert.Description></Alert.Root>{/if}

        <Tabs.Root bind:value={section} class="record-detail-tabs">
          <Tabs.List aria-label="请求详情分区" class="w-full justify-start">
            <Tabs.Trigger value="execution">执行链路</Tabs.Trigger><Tabs.Trigger value="payload">请求与响应</Tabs.Trigger><Tabs.Trigger value="billing">计费明细</Tabs.Trigger>
          </Tabs.List>
          <Tabs.Content value="execution">{#key record.id}<ExecutionPath {record} bind:attempt onresponse={showResponse} onsource={openSource} />{/key}</Tabs.Content>
          <Tabs.Content value="billing" class="record-detail-billing">
            <p class="record-detail-note">按请求开始时价格快照和实际用量估算，非上游已对账扣款。</p>
            <dl class="record-context-grid">
              <div><dt>输入</dt>{#if record}<dd>{number(record.usage?.input)} Token</dd>{/if}</div>
              <div><dt>输出</dt>{#if record}<dd>{number(record.usage?.output)} Token</dd>{/if}</div>
              <div><dt>缓存读取</dt>{#if record}<dd>{number(record.usage?.cacheRead)} Token</dd>{/if}</div>
              <div><dt>缓存创建 · 5 分钟 / 1 小时</dt>{#if record}<dd>{number(record.usage?.cacheWrite)} / {number(record.usage?.cacheWrite1h)} Token</dd>{/if}</div>
              <div><dt>完整输入上下文</dt>{#if record}<dd>{number(record.usage?.context)} Token</dd>{/if}</div>
              <div><dt>执行倍率</dt><dd>{record.multiplier == null ? "未知" : `${record.multiplier}×`}</dd></div>
              {#if record.usage?.unit}<div><dt>媒体数量 / 规格</dt><dd>{number(record.usage.quantity)} {record.usage.unit === "image" ? "张" : "秒"} · {record.usage.specification || "规格未记录"}</dd></div>{/if}
            </dl>
            {#if record.charges.length}<Table.Root><Table.Header><Table.Row><Table.Head>计价维度</Table.Head><Table.Head>实际数量</Table.Head><Table.Head>美元单价</Table.Head><Table.Head>估算金额</Table.Head></Table.Row></Table.Header><Table.Body>
              {#each record.charges as charge}<Table.Row><Table.Cell>{rateNames[charge.key] ?? charge.key}</Table.Cell><Table.Cell>{number(charge.quantity)}</Table.Cell><Table.Cell>{money(charge.price)}<small>{unitLabel(rateUnit(charge.key))}</small></Table.Cell><Table.Cell>{money(charge.amount)}</Table.Cell></Table.Row>{/each}
            </Table.Body></Table.Root>{:else}<p class="record-detail-note">{record.cost == null ? "费用未知，未生成分项金额。" : "未记录费用分项。"}</p>{/if}
            <!-- 未定价模型的历史快照可返回 null，不能让非当前计费页阻断整个详情。 -->
            {#if record.pricing.base?.length}<h4>开始时基础价格</h4><Table.Root><Table.Header><Table.Row><Table.Head>维度</Table.Head><Table.Head>价格</Table.Head><Table.Head>单位</Table.Head></Table.Row></Table.Header><Table.Body>
              {#each record.pricing.base as rate}<Table.Row><Table.Cell>{rateNames[rate.key] ?? rate.key}</Table.Cell><Table.Cell>{money(rate.price)}</Table.Cell><Table.Cell>{unitLabel(rate.unit)}</Table.Cell></Table.Row>{/each}
            </Table.Body></Table.Root>{/if}
            <Collapsible.Root><Collapsible.Trigger>{#snippet child({ props })}<Button {...props} variant="text" size="inline">完整历史价格快照</Button>{/snippet}</Collapsible.Trigger><Collapsible.Content><pre>{JSON.stringify(record.pricing, null, 2)}</pre></Collapsible.Content></Collapsible.Root>
          </Tabs.Content>
          <Tabs.Content value="payload">{#key record.id}<Exchange {record} bind:source oncopy={copy} />{/key}</Tabs.Content>
        </Tabs.Root>
      {/if}
    </div>
  </Sheet.Content>
</Sheet.Root>
