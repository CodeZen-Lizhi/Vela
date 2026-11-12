<script lang="ts">
  import PageHeader from "$lib/components/PageHeader.svelte";
  import { onMount } from "svelte";
  import { isMyGo } from "mygo-runtime";
  import {
    Search,
    RefreshCw,
    Radio,
    ChevronLeft,
    ChevronRight,
    Info,
    Waves,
    ArrowRightLeft,
    Cable,
    Check,
  } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import * as InputGroup from "$lib/components/ui/input-group";
  import * as Table from "$lib/components/ui/table";
  import * as Select from "$lib/components/ui/select";
  import * as Tooltip from "$lib/components/ui/tooltip";
  import { Separator } from "$lib/components/ui/separator";
  import {
    VelaService,
    events,
    type Page,
    type RecordsRecord,
    type Query,
  } from "$lib/generated/mygo";
  import Choice from "$lib/providers/Choice.svelte";
  import { errorMessage } from "$lib/settings/sections";
  import Filter from "./Filter.svelte";
  import Detail from "./Detail.svelte";
  import { elapsedClock } from "./elapsed-clock.svelte";
  import {
    states,
    executions,
    transports,
    sources,
    types,
    protocol,
    label,
    number,
    compact,
    money,
    seconds,
    cache,
    date,
    emptyQuery,
  } from "./display";
  import "./records.css";
  /** 使用记录工作台读取真实分页；浏览器预览不生成演示请求或伪造空数据库。 */
  let {
    notify,
    initialProvider = "",
    initialRecord = "",
  }: {
    /** 操作成功与失败的轻量反馈。 */ notify: (message: string) => void;
    /** 从供应商最近调用带入的筛选和详情身份。 */ initialProvider?: string;
    initialRecord?: string;
  } = $props();
  /** q 是唯一查询草稿；result 是最后一次成功快照，失败不清空。 */
  let q = $state<Query>(emptyQuery()),
    result = $state<Page | null>(null),
    range = $state("today"),
    openID = $state("");
  /** 页面生命周期、异步代次和刷新计时，离页后不回写旧结果。 */
  const desktop = isMyGo();
  let loading = $state(false),
    error = $state(""),
    realtime = $state(false),
    selected = $state("");
  let generation = 0,
    disposed = false,
    ready = false,
    refreshing = false,
    activeReading = false;
  /** 每页条数沿用用户当前选择，筛选变化时保留。 */
  let pageSize = $state("20");
  /** 当前页共享逐帧展示时钟，实际记录查询仍按原周期进行。 */
  const clock = elapsedClock(() => Boolean(result?.records.some((row) => row.state === "active")));
  /** 总页数按后端匹配总数计算，空结果仍保留内部第一页。 */
  const pages = $derived(
    Math.max(1, Math.ceil((result?.summary.total ?? 0) / q.size)),
  );
  const summary = $derived(result?.summary);
  const filterIdentity = $derived(
    JSON.stringify([
      q.search,
      q.modelId,
      q.providerId,
      q.protocol,
      q.source,
      q.states,
      q.types,
      q.transports,
      q.executions,
      pageSize,
      range,
    ]),
  );
  /** 空库与主动筛选无匹配使用不同提示。 */
  const filtered = $derived(
    Boolean(
      q.search ||
      q.modelId ||
      q.providerId ||
      q.protocol ||
      q.source ||
      q.states.length ||
      q.types.length ||
      q.transports.length ||
      q.executions.length ||
      range !== "today",
    ),
  );
  /** 当前筛选从本地自然日开始，全部表示保留期内全部明细。 */
  function query(): Query {
    const query = { ...q };
    if (range !== "all") {
      const day = new Date();
      day.setHours(0, 0, 0, 0);
      day.setDate(
        day.getDate() - (range === "today" ? 0 : range === "last7" ? 6 : 29),
      );
      query.start = day.getTime();
    } else query.start = 0;
    return query;
  }
  /** 读取分页及摘要，较旧响应不能覆盖筛选或翻页后的结果。 */
  async function load(reset = false): Promise<void> {
    if (!desktop || disposed) return;
    if (reset) q.before = 0;
    const version = ++generation;
    loading = true;
    error = "";
    try {
      const next = await VelaService.requestRecords(query());
      if (disposed || version !== generation) return;
      result = next;
      q.before = next.before;
    } catch (cause) {
      if (!disposed && version === generation) error = errorMessage(cause);
    } finally {
      if (!disposed && version === generation) loading = false;
    }
  }
  /** 后续页只同步已展示活跃身份，不能被新请求挤动；摘要按相同快照重读。 */
  async function updateActive(): Promise<void> {
    if (
      !desktop ||
      disposed ||
      document.hidden ||
      activeReading ||
      loading ||
      !result
    )
      return;
    const ids = result.records
      .filter((row) => row.state === "active")
      .map((row) => row.id);
    if (!ids.length) return;
    activeReading = true;
    const version = generation;
    try {
      const updates = await VelaService.activeRequestRecords(ids);
      if (disposed || version !== generation || !result) return;
      if (!updates.length) return;
      const snapshot = await VelaService.requestRecords(query());
      if (disposed || version !== generation || !result) return;
      result = snapshot;
      if (q.page > Math.max(1, Math.ceil(snapshot.summary.total / q.size))) {
        q.page = Math.max(1, Math.ceil(snapshot.summary.total / q.size));
        await load();
      }
    } catch (cause) {
      if (!disposed && version === generation) error = errorMessage(cause);
    } finally {
      activeReading = false;
    }
  }
  /** 手动刷新或窗口恢复刷新允许获取当前页新快照。 */
  async function refresh(): Promise<void> {
    if (refreshing) return;
    refreshing = true;
    try {
      await load(true);
    } finally {
      refreshing = false;
    }
  }
  /** 翻页保留已读取快照，返回第一页重新取最新结果。 */
  function turn(page: number): void {
    q.page = page;
    void load(page === 1);
  }
  /** 完整行点击进入对应详情，选中文本及行内操作不触发跳转。 */
  function openRecord(event: MouseEvent, id: string): void {
    if (event.defaultPrevented || event.button !== 0 || window.getSelection()?.toString().trim()) return;
    if (event.target instanceof Element && event.target.closest("button,a,input,select,textarea")) return;
    selected = id;
  }
  $effect(() => {
    filterIdentity;
    if (!ready || !desktop) return;
    generation++;
    const timer = setTimeout(() => {
      q.page = 1;
      q.size = Number(pageSize);
      void load(true);
    }, 180);
    return () => clearTimeout(timer);
  });
  onMount(() => {
    q.providerId = initialProvider;
    selected = initialRecord;
    ready = true;
    void load(true);
    const active = setInterval(() => {
      void updateActive();
    }, 1000);
    const live = setInterval(() => {
      if (realtime && q.page === 1 && !document.hidden && !loading)
        void load(true);
    }, 3000);
    const visible = () => {
      if (!document.hidden) void refresh();
    };
    document.addEventListener("visibilitychange", visible);
    const unsubscribe = events.windowVisible.on(() => void refresh());

    return () => {
      unsubscribe();
      disposed = true;
      generation++;
      clearInterval(active);
      clearInterval(live);
      document.removeEventListener("visibilitychange", visible);
    };
  });
</script>

<div class="records-page list-page">
  <PageHeader title="使用记录" description="请求明细 · 用量、估算费用与执行路径">
    {#snippet actions()}
    <div class="records-actions">
      <Button
        variant={realtime ? "secondary" : "outline"}
        disabled={!desktop}
        aria-pressed={realtime}
        onclick={() => (realtime = !realtime)}
        ><Radio />{realtime ? "实时刷新中" : "实时刷新"}</Button
      ><Button
        variant="outline"
        disabled={!desktop || loading}
        onclick={refresh}><RefreshCw />刷新</Button
      >
    </div>
    {/snippet}
  </PageHeader>
  {#if !desktop}<p class="records-notice" role="status">
      浏览器预览：请在 Vela 桌面应用中查询真实请求记录。
    </p>{/if}
  {#if error}<div class="records-error" role="alert">
      {error}<Button variant="outline" onclick={refresh}>重试</Button>
    </div>{/if}
  <section class="records-surface list-surface" aria-label="请求记录">
    <div class="records-filters">
        <InputGroup.Root class="records-search"
          ><InputGroup.Addon><Search /></InputGroup.Addon><InputGroup.Input
            aria-label="搜索请求记录"
            placeholder="搜索记录"
            title="搜索请求 ID、模型、供应商、Key 或错误"
            bind:value={q.search}
            disabled={!desktop}
          /></InputGroup.Root
        >
        <div class="records-time-choice">
          <Choice
            bind:value={range}
            label="时间范围"
            options={[
              { value: "today", label: "今天" },
              { value: "last7", label: "最近7天" },
              { value: "last30", label: "最近30天" },
              { value: "all", label: "全部" },
            ]}
            disabled={!desktop}
          />
        </div>
        <Filter
          id="source"
          title="来源"
          options={sources}
          bind:value={q.source}
          bind:openID
          disabled={!desktop}
        />
        <Filter
          id="transport"
          title="传输"
          options={transports}
          multiple
          bind:values={q.transports}
          bind:openID
          disabled={!desktop}
        />
        <Filter
          id="status"
          title="状态"
          options={states}
          multiple
          bind:values={q.states}
          bind:openID
          disabled={!desktop}
        />
        <Filter
          id="execution"
          title="链路"
          options={executions}
          multiple
          bind:values={q.executions}
          bind:openID
          disabled={!desktop}
        />
    </div>
    <!-- 独立滚动区需要键盘焦点，支持方向键与翻页键浏览列表。 -->
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <div class="list-scroll" role="region" aria-label="使用记录列表内容" tabindex="0">
    {#if loading && !result}<p class="records-empty" role="status">
        正在读取请求记录…
      </p>{:else if result && !result.records.length}<div class="records-empty">
        <h3>
          {summary?.total
            ? "当前页没有记录"
            : filtered
              ? "没有匹配的请求记录"
              : "暂无请求记录"}
        </h3>
        <p>调整筛选，或在产生真实请求后刷新。</p>
      </div>{/if}
    <div class="records-table" aria-busy={loading}>
      <Table.Root
        ><Table.Header
          ><Table.Row>
            <Table.Head>时间</Table.Head><Table.Head
              ><Filter
                id="model"
                column
                title="模型"
                options={result?.models ?? []}
                bind:value={q.modelId}
                bind:openID
                disabled={!desktop}
              /></Table.Head
            ><Table.Head
              ><Filter
                id="provider"
                column
                title="供应商"
                options={result?.providers ?? []}
                bind:value={q.providerId}
                bind:openID
                disabled={!desktop}
              /></Table.Head
            ><Table.Head
              ><Filter
                id="protocol"
                column
                title="API 格式"
                options={(result?.protocols ?? []).map((x) => ({
                  ...x,
                  label: protocol(x.value),
                }))}
                bind:value={q.protocol}
                bind:openID
                disabled={!desktop}
              /></Table.Head
            ><Table.Head
              ><Filter
                id="type"
                column
                title="类型 / 状态"
                options={types}
                multiple
                bind:values={q.types}
                bind:openID
                disabled={!desktop}
              /></Table.Head
            ><Table.Head class="normal-case" title="第一行：输入 / 输出 Token；第二行：缓存读取 Token / 命中率">Token</Table.Head
            ><Table.Head>估算费用</Table.Head><Table.Head
              >端到端<small>首字 / 总耗时</small><small>输出速度</small></Table.Head
            >
          </Table.Row></Table.Header
        ><Table.Body
          >{#each result?.records ?? [] as row (row.id)}<Table.Row
              class="record-row"
              data-record-id={row.id}
              data-record-state={row.state}
              role="button"
              tabindex={0}
              aria-label={`查看 ${row.model || "未知模型"} 请求详情`}
              aria-haspopup="dialog"
              onclick={(event) => openRecord(event, row.id)}
              onkeydown={(event) => {
                if (event.target === event.currentTarget && (event.key === "Enter" || event.key === " ")) {
                  event.preventDefault();
                  selected = row.id;
                }
              }}
              ><Table.Cell
                ><span class="record-value">{date(row.startedAt)}</span
                ><small>{date(row.startedAt, "day")}</small></Table.Cell
              ><Table.Cell>{@render modelCell(row)}</Table.Cell><Table.Cell
                >{@render providerCell(row)}</Table.Cell
              ><Table.Cell
                ><span class="record-truncate" title={protocol(row.protocol)}
                  >{protocol(row.protocol)}</span
                ></Table.Cell
              ><Table.Cell>{@render stateCell(row)}</Table.Cell
              ><Table.Cell>{@render usageCell(row)}</Table.Cell><Table.Cell
                ><span class="record-value record-cost" class:record-missing={row.cost == null || row.cost === 0} title={`${money(row.cost)} · 本地估算`}
                  >{money(row.cost)}</span
                ></Table.Cell
              ><Table.Cell>{@render timingCell(row)}</Table.Cell></Table.Row
            >{/each}</Table.Body
        ></Table.Root
      >
    </div>
    </div>
    <footer class="records-pagination list-footer">
      <div class="records-pagination-summary">
        <span>共 {number(summary?.total)} 条</span>
        <Tooltip.Provider delayDuration={150}>
          <Tooltip.Root>
            <Tooltip.Trigger>
              {#snippet child({ props })}
                <Button {...props} variant="ghost" size="icon-xs" aria-label="记录保留期限"><Info /></Button>
              {/snippet}
            </Tooltip.Trigger>
            <Tooltip.Content side="top">明细保留 30 天 · 正文保留 7 天</Tooltip.Content>
          </Tooltip.Root>
        </Tooltip.Provider>
      </div>
      <div class="records-pagination-controls">
        <div class="records-page-size">
          <span>每页</span>
          <Select.Root type="single" bind:value={pageSize} disabled={!desktop || loading}>
            <Select.Trigger variant="ghost" aria-label="每页条数">{pageSize} 条</Select.Trigger>
            <Select.Content side="top" align="end"><Select.Group>
              {#each [10, 20, 50, 100] as size}
                <Select.Item value={String(size)} label={`${size} 条`}>{size} 条</Select.Item>
              {/each}
            </Select.Group></Select.Content>
          </Select.Root>
        </div>
        <Separator orientation="vertical" class="mx-1 h-4 w-px self-center" />
        <nav class="records-pagination-nav" aria-label="使用记录分页">
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="上一页"
          disabled={!desktop || loading || q.page <= 1}
          onclick={() => turn(q.page - 1)}><ChevronLeft /></Button
        >
        <span class="records-page-count" role="status" aria-label={result ? `第 ${summary?.total ? q.page : 0} 页，共 ${summary?.total ? pages : 0} 页` : "等待读取页码"}>
          {#if result}{summary?.total ? q.page : 0} / {summary?.total ? pages : 0}{:else}— / —{/if}
        </span>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="下一页"
          disabled={!desktop || loading || q.page >= pages}
          onclick={() => turn(q.page + 1)}><ChevronRight /></Button
        >
        </nav>
      </div>
    </footer>
  </section>
</div>
{#snippet modelCell(row: RecordsRecord)}<strong
    class="record-model record-truncate"
    title={row.model || "未知模型"}>{row.model || "未知模型"}</strong
  >
  <div class="records-badges">
    {#if row.effort}<Badge variant="parameter">{row.effort}</Badge
      >{/if}{#if row.tier && row.tier !== "standard"}<Badge variant="parameter"
        >{row.tier}</Badge
      >{/if}
  </div>
  {#if row.upstreamModel && row.upstreamModel !== row.model}<small
      >→ {row.upstreamModel}</small
    >{/if}{/snippet}
{#snippet providerCell(row: RecordsRecord)}<span
    class="record-truncate"
    title={row.providerName || "未发上游"}
    >{row.providerName ||
      (row.state === "local" ? "本地响应" : "未知供应商")}</span
  ><small class="record-truncate" title={row.keyName || "—"}
    >{row.keyName || "—"}</small
  >{#if row.execution !== "direct"}<Badge variant="neutral"
      >{label(executions, row.execution)}</Badge
    >{/if}{/snippet}
<!-- 类型与生命周期沿用旧版同列堆叠，成功不重复添加状态标签。 -->
{#snippet stateCell(row: RecordsRecord)}<div class="record-type-status" role="group" aria-label={`请求状态：${label(states, row.state)}`} title={`请求状态：${label(states, row.state)}`}>
    {#if row.state === "active"}<Badge variant="processing">进行中</Badge>
    {:else}
      <Badge variant={row.state === "success" ? "positive" : "info"}>{#if row.state === "success"}<Check aria-hidden="true" />{/if}{label(types, row.type)}</Badge>
      {#if row.state !== "success"}<Badge variant={row.state === "failed" ? "negative" : row.state === "local" ? "warning" : "neutral"}>{label(states, row.state)}</Badge>{/if}
      <Badge variant={row.transport === "websocket" || row.stream ? "transport" : "metadata"}>
        {#if row.transport === "websocket"}<Cable aria-hidden="true" />WS{:else if row.stream}<Waves aria-hidden="true" />流式{:else}<ArrowRightLeft aria-hidden="true" />标准{/if}
      </Badge>
    {/if}
  </div>{/snippet}
{#snippet usageCell(row: RecordsRecord)}{#if row.state === "local"}<span
      class="text-muted-foreground">— 无用量</span
    >{:else if row.usage?.unit}<strong
      >{number(row.usage.quantity)}
      {row.usage.unit === "image" ? "张" : "秒"}</strong
    ><small
      >{row.usage.specification} · {row.usage.unit === "image"
        ? "按次"
        : "按时长"}</small
    >{:else}<div class="records-token-usage" aria-label="Token 用量" title={`输入 ${row.usage?.input ?? "未知"} Token；输出 ${row.usage?.output ?? "未知"} Token；缓存读取 ${row.usage?.cacheRead ?? "未知"} Token；缓存写入 ${row.usage ? row.usage.cacheWrite + row.usage.cacheWrite1h : "未知"} Token；命中 ${cache(row.usage)}`}>
      <div><span class="record-input" class:record-missing={!row.usage} aria-label={`输入 ${row.usage?.input ?? "未知"} Token`}>{compact(row.usage?.input)}</span><span class="record-separator" aria-hidden="true">/</span><span class="record-output" class:record-missing={!row.usage} aria-label={`输出 ${row.usage?.output ?? "未知"} Token`}>{compact(row.usage?.output)}</span></div>
      <div><span class="record-cache" class:record-missing={!row.usage || row.usage.cacheRead === 0} aria-label={`缓存读取 ${row.usage?.cacheRead ?? "未知"} Token`}>{compact(row.usage?.cacheRead)}</span><span class="record-separator" aria-hidden="true">/</span><span class="record-hit" class:record-missing={!row.usage || !row.usage.context} aria-label={`缓存命中率 ${cache(row.usage)}`}>{cache(row.usage)}</span></div>
    </div>{/if}{/snippet}
{#snippet timingCell(row: RecordsRecord)}{#if row.state === "local"}<span
      >本地响应</span
    ><small>{seconds(row.durationMillis)}</small>{:else}<div class="record-timing-pair" title="端到端首字 / 总耗时">
      <span class="record-first-byte">{seconds(row.firstByteMillis)}</span><span class="record-separator">/</span><span class="record-total-time">{seconds(row.state === "active" ? Math.max(0, clock.now - row.startedAt) : row.durationMillis)}</span>
    </div>{#if row.state !== "active"}<span class="record-speed" class:record-missing={row.outputRate == null} title={`输出速度 ${number(row.outputRate)} tps`}>{row.outputRate == null ? "—" : `${compact(row.outputRate)} tps`}</span>{/if}{/if}{/snippet}
<Detail id={selected} onclose={() => (selected = "")} {notify} />
