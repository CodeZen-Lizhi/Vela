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
  import * as ToggleGroup from "$lib/components/ui/toggle-group";
  import Choice from "$lib/providers/Choice.svelte";
  import { Copy, RefreshCw, X, Terminal } from "@lucide/svelte";
  import { errorMessage } from "$lib/settings/sections";
  import { rateNames, rateUnit, unitLabel } from "$lib/models/display";
  import DetailSection from "./DetailSection.svelte";
  import ExecutionPath from "./ExecutionPath.svelte";
  import { elapsedClock } from "./elapsed-clock.svelte";
  import {
    states,
    sources,
    types,
    protocol,
    label,
    number,
    compact,
    money,
    duration,
    date,
    pretty,
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
    panel = $state("request"),
    side = $state("provider"),
    view = $state("json"),
    compare = $state(false),
    expanded = $state(true);
  /** 返回原列表行的身份独立于熔断追溯身份。 */
  let originID = "";
  /** 四个页签沿原型组织详情，默认先查看真实执行链路。 */
  let section = $state("execution");
  /** 与列表共用逐帧时钟语义，按真实起点累计，不推断完成状态。 */
  const clock = elapsedClock(() => record?.state === "active");
  let generation = 0;
  /** 当前阅读身份允许追溯熔断来源。 */
  let viewedID = $state("");
  /** 当前实际尝试、数据源与可见内容。 */
  const current = $derived(record?.attempts[Number(attempt)]);
  const payload = $derived(
    side === "client" ? record?.client : current?.payload,
  );
  const content = $derived(
    panel === "headers"
      ? (payload?.requestHeaders ?? "")
      : panel === "response-headers"
        ? (payload?.responseHeaders ?? "")
        : panel === "request"
          ? (payload?.requestBody ?? "")
          : (payload?.responseBody ?? ""),
  );
  /** 对话解析和头部比较只计算当前可见正文，避免在模板中反复解析。 */
  const bodyPanel = $derived(panel === "request" || panel === "response");
  const conversationRows = $derived(bodyPanel && view === "conversation" ? conversation(content) : []);
  const comparedHeaders = $derived(!bodyPanel && compare ? headers(
    panel === "headers" ? (record?.client?.requestHeaders ?? "") : (record?.client?.responseHeaders ?? ""),
    panel === "headers" ? (current?.payload?.requestHeaders ?? "") : (current?.payload?.responseHeaders ?? ""),
  ) : []);
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
    panel = "request";
    side = "provider";
    view = "json";
    compare = false;
    expanded = true;
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
    panel = "response";
    side = "provider";
    compare = false;
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
  /** cURL 使用占位地址与认证，缺少正文时不生成。 */
  function curl(): string {
    if (
      !current?.payload?.requestBody ||
      current.payload.bodyState !== "available"
    )
      return "";
    const quote = (text: string): string =>
      "'" + text.replaceAll("'", "'\\''") + "'";
    const auth = record?.protocol.includes("gemini")
      ? "x-goog-api-key: YOUR_API_KEY"
      : ["anthropic", "messages", "claude:messages"].includes(
            record?.protocol ?? "",
          )
        ? "x-api-key: YOUR_API_KEY"
        : "Authorization: Bearer YOUR_API_KEY";
    return `curl -X POST ${quote("https://UPSTREAM_HOST" + current.path)} -H 'Content-Type: application/json' -H ${quote(auth)} --data-binary ${quote(current.payload.requestBody)}
# 替换上游地址和认证；正文已脱敏，媒体未保存，可能无法完整复现。`;
  }
  /** 对话行读取文本、思考和工具调用；不能解析时保留 JSON 入口。 */
  function conversation(text: string): { role: string; text: string }[] {
    const rows: { role: string; text: string }[] = [];
    const visit = (value: unknown, role = "内容"): void => {
      if (typeof value === "string") {
        rows.push({ role, text: value });
        return;
      }
      if (Array.isArray(value)) {
        for (const item of value) visit(item, role);
        return;
      }
      if (!value || typeof value !== "object") return;
      const item = value as { [key: string]: unknown };
      const who = typeof item.role === "string" ? item.role : role;
      if (typeof item.text === "string")
        rows.push({ role: who, text: item.text });
      for (const key of [
        "messages",
        "input",
        "output",
        "content",
        "contents",
        "parts",
        "choices",
        "message",
        "delta",
        "candidates",
      ])
        if (item[key]) visit(item[key], who);
      for (const key of [
        "tool_calls",
        "functionCall",
        "functionResponse",
        "thinking",
        "reasoning_content",
      ])
        if (item[key])
          rows.push({
            role: key,
            text:
              typeof item[key] === "string"
                ? (item[key] as string)
                : JSON.stringify(item[key], null, 2),
          });
    };
    try {
      visit(JSON.parse(text));
    } catch {
      for (const line of text.split("\n")) {
        if (line.startsWith("data:")) {
          try {
            visit(JSON.parse(line.slice(5)));
          } catch {
            /* SSE 终止标记不是对话数据。 */
          }
        }
      }
    }
    return rows;
  }
  /** 对比头按名称合并，两侧共享行高和滚动。 */
  function headers(
    left: string,
    right: string,
  ): { name: string; left: string; right: string }[] {
    let a: { [key: string]: unknown } = {},
      b: { [key: string]: unknown } = {};
    try {
      a = JSON.parse(left || "{}");
      b = JSON.parse(right || "{}");
    } catch {
      return [];
    }
    return [...new Set([...Object.keys(a), ...Object.keys(b)])]
      .sort()
      .map((name) => ({
        name,
        left: JSON.stringify(a[name]) ?? "—",
        right: JSON.stringify(b[name]) ?? "—",
      }));
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
          <Button onclick={() => load(viewedID)} variant="ghost" size="icon-sm" aria-label="刷新请求详情"  disabled={loading} ><RefreshCw /></Button>
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
          <Card.Root size="sm"><Card.Content><span>总费用</span><strong class="text-parameter">{money(record.cost)}</strong><small>本地估算 · 价格快照</small></Card.Content></Card.Root>
          <Card.Root size="sm"><Card.Content><span>总耗时</span><strong>{duration(elapsed)}</strong><small>{record.state === "active" ? "实时累计" : "端到端"}</small></Card.Content></Card.Root>
          <Card.Root size="sm"><Card.Content><span>{record.usage?.unit ? "计费数量" : "输出 Token"}</span><strong>{record.usage?.unit ? `${number(record.usage.quantity)} ${record.usage.unit === "image" ? "张" : "秒"}` : compact(record.usage?.output)}</strong><small>{record.usage?.unit ? "实际计费量" : "上游返回用量"}</small></Card.Content></Card.Root>
          <Card.Root size="sm"><Card.Content><span>输出速度</span><strong>{record.outputRate == null ? "—" : `${compact(record.outputRate)} TPS`}</strong><small>有效输出时长</small></Card.Content></Card.Root>
          <Card.Root size="sm"><Card.Content><span>首字耗时</span><strong>{duration(record.firstByteMillis)}</strong><small>端到端首字</small></Card.Content></Card.Root>
        </section>
        {#if record.error}<Alert.Root variant="destructive"><Alert.Description>{record.error}</Alert.Description></Alert.Root>{/if}
        {#if !record.usage}<p class="record-detail-note">{record.state === "active" ? "请求进行中，用量与估算费用尚未结算。" : record.state === "local" ? "请求未发上游，无上游用量。" : "上游未提供可验证用量，未知值不显示为零。"}</p>{/if}
        {#if record.circuitSourceId}<Alert.Root variant="info"><Alert.Description>本地熔断 · 触发请求 <Button variant="link" size="inline" onclick={() => openSource(record!.circuitSourceId)}>{record.circuitSourceId}</Button></Alert.Description></Alert.Root>{/if}

        <Tabs.Root bind:value={section} onValueChange={value => { if (value === "headers") panel = "headers"; else if (value === "payload") panel = "request"; }} class="record-detail-tabs">
          <Tabs.List aria-label="请求详情分区" class="w-full justify-start">
            <Tabs.Trigger value="execution">执行链路</Tabs.Trigger><Tabs.Trigger value="payload">请求与响应</Tabs.Trigger><Tabs.Trigger value="headers">请求头</Tabs.Trigger><Tabs.Trigger value="billing">计费与上下文</Tabs.Trigger>
          </Tabs.List>
          <Tabs.Content value="execution">{#key record.id}<ExecutionPath {record} bind:attempt onresponse={showResponse} onsource={openSource} />{/key}</Tabs.Content>
          <Tabs.Content value="billing" class="record-detail-billing">
        <DetailSection title="计费明细" description="价格、Token 与媒体费用">
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
        </DetailSection>

        <DetailSection title="请求上下文" description="来源、模型映射与执行目标">
          <dl class="record-context-grid">
            <div><dt>API 格式 / 请求类型</dt><dd>{protocol(record.protocol)} · {label(types, record.type)}</dd></div>
            <div><dt>来源</dt><dd>{label(sources, record.source)}</dd></div>
            <div><dt>统一模型 → 上游模型</dt><dd>{record.model || "未知"} → {record.upstreamModel || "未记录"}</dd></div>
            <div><dt>请求端点</dt><dd>{current?.path || "未发上游或未记录"}</dd></div>
            <div><dt>供应商 / 执行 Key</dt><dd>{record.providerName || "未发上游"} / {record.keyName || "—"}</dd></div>
            <div><dt>思考强度 / 处理层级</dt><dd>{record.effort || "未记录"} / {record.tier || "未记录"}</dd></div>
            <div><dt>请求时名称</dt><dd>{record.modelName || record.model} / {record.providerName || "—"} / {record.keyName || "—"}</dd></div>
            <div><dt>当前名称</dt><dd>{record.currentModelName || "—"} / {record.currentProviderName || "—"} / {record.currentKeyName || "—"}</dd></div>
          </dl>
        </DetailSection>

          </Tabs.Content>
          {#if section === "payload" || section === "headers"}
          <Tabs.Content value={section}>
            <div class="record-raw-section">
              <Tabs.Root bind:value={panel}>
                <Tabs.List aria-label="原始请求与响应" class="record-payload-tabs">
                  {#if section === "headers"}<Tabs.Trigger value="headers">请求头</Tabs.Trigger><Tabs.Trigger value="response-headers">响应头</Tabs.Trigger>{:else}<Tabs.Trigger value="request">请求正文</Tabs.Trigger><Tabs.Trigger value="response">响应正文</Tabs.Trigger>{/if}
                </Tabs.List>
                <div class="record-payload-toolbar">
                  {#if record.attempts.length}<Choice bind:value={attempt} label="实际尝试" options={record.attempts.map((item, index) => ({ value: String(index), label: `${item.number}. ${item.providerName} / ${item.keyName} · HTTP ${item.status || "—"}` }))} />{/if}
                  <div class="records-actions">
                    <ToggleGroup.Root type="single" value={side} onValueChange={(next) => { if (next) side = next; }} aria-label="数据来源" variant="segmented" size="sm" spacing={1}>
                      <ToggleGroup.Item value="client" aria-label="客户端数据">客户端</ToggleGroup.Item><ToggleGroup.Item value="provider" aria-label="上游数据">上游</ToggleGroup.Item>
                    </ToggleGroup.Root>
                    <div class="record-payload-view">
                      <div class:invisible={!bodyPanel} inert={!bodyPanel} aria-hidden={!bodyPanel}><ToggleGroup.Root type="single" value={view} onValueChange={(next) => { if (next) view = next; }} aria-label="正文视图" variant="segmented" size="sm" spacing={1}><ToggleGroup.Item value="json">JSON</ToggleGroup.Item><ToggleGroup.Item value="conversation">对话</ToggleGroup.Item></ToggleGroup.Root></div>
                      <Button class={bodyPanel ? "invisible" : undefined} variant={compare ? "secondary" : "ghost"} size="sm" aria-pressed={compare} aria-hidden={bodyPanel} disabled={bodyPanel} onclick={() => (compare = !compare)}>对比上下游</Button>
                    </div>
                    <Button class={panel === "response" || panel === "response-headers" ? "invisible" : undefined} onclick={() => copy(curl())} variant="ghost" size="icon-sm" aria-label="复制 cURL" aria-hidden={panel === "response" || panel === "response-headers"} disabled={panel === "response" || panel === "response-headers" || !curl()} ><Terminal /></Button>
                    <Button onclick={() => copy(content)} variant="ghost" size="icon-sm" aria-label="复制当前内容"  disabled={!content || (!bodyPanel && compare)} ><Copy /></Button>
                  </div>
                </div>
                {#each ["headers", "request", "response-headers", "response"] as tab}
                  <Tabs.Content value={tab}>
                    {#if panel === tab}
                      {#if !bodyPanel && compare}
                        {#if comparedHeaders.length}<Table.Root><Table.Header><Table.Row><Table.Head>头名称</Table.Head><Table.Head>客户端</Table.Head><Table.Head>上游</Table.Head></Table.Row></Table.Header><Table.Body>{#each comparedHeaders as row}<Table.Row><Table.Cell>{row.name}</Table.Cell><Table.Cell>{row.left}</Table.Cell><Table.Cell>{row.right}</Table.Cell></Table.Row>{/each}</Table.Body></Table.Root>{:else}<Alert.Root variant="info"><Alert.Description>没有可对比的头部数据。</Alert.Description></Alert.Root>{/if}
                      {:else if bodyPanel && payload?.bodyState === "expired"}<Alert.Root variant="info"><Alert.Description>正文已超过 7 天保留期限，摘要与诊断仍可查询。</Alert.Description></Alert.Root>
                      {:else if !content}<Alert.Root variant="info"><Alert.Description>{record.state === "active" ? "请求进行中，尚无此项数据。" : "此侧数据未采集或不适用。"}</Alert.Description></Alert.Root>
                      {:else if bodyPanel && view === "conversation" && conversationRows.length}
                        {#each conversationRows as line}<article class="record-message"><strong>{line.role}</strong><pre>{line.text}</pre></article>{/each}
                      {:else}
                        {#if bodyPanel && view === "conversation"}<p class="record-detail-note">此内容无法解析为对话，显示原始数据。</p>{/if}
                        <Collapsible.Root bind:open={expanded}><Collapsible.Trigger>{#snippet child({ props })}<Button {...props} variant="text" size="inline">{expanded ? "收起 JSON" : "展开 JSON"}</Button>{/snippet}</Collapsible.Trigger><Collapsible.Content><pre>{pretty(content)}</pre></Collapsible.Content></Collapsible.Root>
                      {/if}
                      {#if bodyPanel && payload?.bodyState === "truncated"}<p class="record-detail-note">快照已截断，不表示上游响应被截断。</p>{/if}
                    {/if}
                  </Tabs.Content>
                {/each}
              </Tabs.Root>
            </div>
          </Tabs.Content>
          {/if}
        </Tabs.Root>
      {/if}
    </div>
  </Sheet.Content>
</Sheet.Root>
