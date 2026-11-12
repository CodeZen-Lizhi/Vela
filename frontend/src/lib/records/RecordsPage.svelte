<script lang="ts">
  import { onMount } from "svelte";
  import { isMyGo } from "mygo-runtime";
  import {
    Search,
    RefreshCw,
    Radio,
    ChevronLeft,
    ChevronRight,
    Activity,
    CheckCircle2,
    Cpu,
    CircleDollarSign,
    Timer,
  } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import * as InputGroup from "$lib/components/ui/input-group";
  import * as Table from "$lib/components/ui/table";
  import * as Collapsible from "$lib/components/ui/collapsible";
  import {
    VelaService,
    events,
    type Page,
    type RecordsRecord,
    type Query,
    type Aggregate,
  } from "$lib/generated/mygo";
  import Choice from "$lib/providers/Choice.svelte";
  import { errorMessage } from "$lib/settings/sections";
  import Filter from "./Filter.svelte";
  import Detail from "./Detail.svelte";
  import {
    states,
    executions,
    transports,
    sources,
    types,
    protocol,
    label,
    number,
    money,
    duration,
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
    range = $state("last30"),
    openID = $state("");
  /** 页面生命周期、异步代次和刷新计时，离页后不回写旧结果。 */
  const desktop = isMyGo();
  let loading = $state(false),
    error = $state(""),
    realtime = $state(false),
    selected = $state("");
  let analysisGeneration = 0;
  let generation = 0,
    disposed = false,
    ready = false,
    refreshing = false,
    activeReading = false;
  /** now 只用于活跃耗时展示；统计数据来自后端而非页面计时推算。 */
  let pageSize = $state("20");
  let now = $state(Date.now()),
    analysisOpen = $state(false),
    totals = $state<Aggregate[]>([]),
    analysisError = $state("");
  /** 总页数与完整筛选结果成功率；未发上游包含在已结束分母。 */
  const pages = $derived(
    Math.max(1, Math.ceil((result?.summary.total ?? 0) / q.size)),
  );
  const summary = $derived(result?.summary);
  const settled = $derived(summary ? summary.total - summary.active : 0);
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
      range !== "last30",
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
      if (analysisOpen) await readAnalysis();
    } finally {
      refreshing = false;
    }
  }
  /** 翻页保留已读取快照，返回第一页重新取最新结果。 */
  function turn(page: number): void {
    q.page = page;
    void load(page === 1);
  }
  /** 清空条件遵循默认最近三十天，保留用户每页数量。 */
  function clear(): void {
    const size = q.size;
    q = { ...emptyQuery(), size };
    range = "last30";
  }
  /** 分析查询仅使用长期统计支持的维度，界面明确其范围与明细不同。 */
  async function readAnalysis(): Promise<void> {
    if (!desktop || disposed) return;
    const version = ++analysisGeneration;
    analysisError = "";
    try {
      const scope = query();
      const next = await VelaService.requestTotals({
        ...emptyQuery(),
        start: scope.start,
        end: scope.end,
        modelId: q.modelId,
        providerId: q.providerId,
        protocol: q.protocol,
      });
      if (!disposed && version === analysisGeneration) totals = next;
    } catch (cause) {
      if (!disposed && version === analysisGeneration)
        analysisError = errorMessage(cause);
    }
  }
  /** 按稳定身份合并长期日统计，不将同名重建实体合并。 */
  function breakdown(kind: "model" | "provider"): {
    id: string;
    name: string;
    requests: number;
    tokens: number;
    cost: number;
    unknown: number;
  }[] {
    const values = new Map<
      string,
      {
        id: string;
        name: string;
        requests: number;
        tokens: number;
        cost: number;
        unknown: number;
      }
    >();
    for (const row of totals) {
      const id = kind === "model" ? row.modelId : row.providerId;
      const item = values.get(id) ?? {
        id,
        name:
          (kind === "model" ? row.modelName : row.providerName) || "未发上游",
        requests: 0,
        tokens: 0,
        cost: 0,
        unknown: 0,
      };
      item.requests += row.requests;
      item.tokens += row.tokens;
      item.cost += row.cost;
      item.unknown += row.unknownCost;
      values.set(id, item);
    }
    return [...values.values()].sort((a, b) => b.requests - a.requests);
  }
  $effect(() => {
    filterIdentity;
    if (!ready || !desktop) return;
    generation++;
    analysisGeneration++;
    const timer = setTimeout(() => {
      q.page = 1;
      q.size = Number(pageSize);
      void load(true);
      if (analysisOpen) void readAnalysis();
    }, 180);
    return () => clearTimeout(timer);
  });
  onMount(() => {
    q.providerId = initialProvider;
    selected = initialRecord;
    ready = true;
    void load(true);
    const active = setInterval(() => {
      now = Date.now();
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

<div class="records-page">
  <header class="records-heading">
    <div>
      <h1>使用记录</h1>
      <p>请求明细 · 摘要、诊断、估算费用与执行路径</p>
    </div>
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
  </header>
  {#if !desktop}<p class="records-notice" role="status">
      浏览器预览：请在 Vela 桌面应用中查询真实请求记录。
    </p>{/if}
  {#if error}<div class="records-error" role="alert">
      {error}<Button variant="outline" onclick={refresh}>重试</Button>
    </div>{/if}
  <section class="records-stats" aria-label="全部筛选结果摘要">
    <article>
      <Activity />
      <div>
        <span>当前请求</span><strong>{number(summary?.total)}</strong><small
          >{number(summary?.active)} 条进行中</small
        >
      </div>
    </article>
    <article>
      <CheckCircle2 />
      <div>
        <span>成功率</span><strong
          >{summary && settled
            ? `${((100 * summary.success) / settled).toFixed(1)}%`
            : "—"}</strong
        ><small
          >{summary
            ? `${summary.success} 成功 · ${summary.failed} 失败 · ${summary.local} 未发上游`
            : "等待读取"}</small
        >
      </div>
    </article>
    <article>
      <Cpu />
      <div>
        <span>Token 总量</span><strong>{number(summary?.tokens)}</strong><small
          >缓存命中 {summary?.inputContext
            ? `${((100 * summary.cacheRead) / summary.inputContext).toFixed(1)}%`
            : "—"}{summary?.unknownUsage
            ? ` · ${summary.unknownUsage} 条未知`
            : ""}</small
        >
      </div>
    </article>
    <article>
      <CircleDollarSign />
      <div>
        <span>估算费用</span><strong>{money(summary?.cost)}</strong><small
          >{summary?.unknownCost
            ? `${summary.unknownCost} 条费用未知，未计入合计`
            : "不包含进行中请求"}</small
        >
      </div>
    </article>
    <article>
      <Timer />
      <div>
        <span>平均总耗时</span><strong
          >{summary?.timed ? duration(summary.averageMillis) : "—"}</strong
        ><small>{number(summary?.timed)} 条有效耗时</small>
      </div>
    </article>
  </section>
  <section class="records-surface" aria-labelledby="records-title">
    <div class="records-list-heading">
      <div>
        <h2 id="records-title">请求记录</h2>
        <p>明细保留 30 天 · 正文保留 7 天</p>
      </div>
      <Button variant="ghost" disabled={!desktop} onclick={clear}
        >清除筛选</Button
      >
    </div>
    <div class="records-filters">
      <div class="records-query">
        <InputGroup.Root class="records-search"
          ><InputGroup.Addon><Search /></InputGroup.Addon><InputGroup.Input
            aria-label="搜索请求记录"
            placeholder="搜索请求 ID、模型、供应商、Key 或错误"
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
      </div>
      <div class="records-filter-options">
        <Filter
          id="transport"
          title="传输方式"
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
          title="执行链路"
          options={executions}
          multiple
          bind:values={q.executions}
          bind:openID
          disabled={!desktop}
        />
      </div>
    </div>
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
            ><Table.Head>状态</Table.Head><Table.Head
              ><Filter
                id="type"
                column
                title="类型"
                options={types}
                multiple
                bind:values={q.types}
                bind:openID
                disabled={!desktop}
              /></Table.Head
            ><Table.Head
              >用量<small title="Token 按量，图片按实际张数，视频按实际时长"
                >Token / 媒体</small
              ></Table.Head
            ><Table.Head>估算费用</Table.Head><Table.Head
              >端到端<small>首字 / 总耗时</small></Table.Head
            >
          </Table.Row></Table.Header
        ><Table.Body
          >{#each result?.records ?? [] as row (row.id)}<Table.Row
              ><Table.Cell
                ><Button
                  variant="link"
                  size="inline"
                  data-record-id={row.id}
                  onclick={() => (selected = row.id)}
                  >{date(row.startedAt)}</Button
                ><small>{date(row.startedAt, "day")}</small></Table.Cell
              ><Table.Cell>{@render modelCell(row)}</Table.Cell><Table.Cell
                >{@render providerCell(row)}</Table.Cell
              ><Table.Cell
                ><span class="record-truncate" title={protocol(row.protocol)}
                  >{protocol(row.protocol)}</span
                ></Table.Cell
              ><Table.Cell>{@render stateCell(row)}</Table.Cell><Table.Cell
                >{label(types, row.type)}<small
                  >{row.stream ? "流式" : "标准"}</small
                ></Table.Cell
              ><Table.Cell>{@render usageCell(row)}</Table.Cell><Table.Cell
                ><span class="record-value" title={money(row.cost)}
                  >{money(row.cost)}</span
                ></Table.Cell
              ><Table.Cell>{@render timingCell(row)}</Table.Cell></Table.Row
            >{/each}</Table.Body
        ></Table.Root
      >
    </div>
    <footer class="records-pagination">
      <span>共 {number(summary?.total)} 条 · 第 {q.page} / {pages} 页</span>
      <div class="records-actions">
        <Choice
          bind:value={pageSize}
          label="每页条数"
          options={[10, 20, 50, 100].map((value) => ({
            value: String(value),
            label: `${value} 条/页`,
          }))}
          disabled={!desktop}
        /><Button
          variant="outline"
          size="icon-sm"
          aria-label="上一页"
          disabled={!desktop || loading || q.page <= 1}
          onclick={() => turn(q.page - 1)}><ChevronLeft /></Button
        ><Button
          variant="outline"
          size="icon-sm"
          aria-label="下一页"
          disabled={!desktop || loading || q.page >= pages}
          onclick={() => turn(q.page + 1)}><ChevronRight /></Button
        >
      </div>
    </footer>
  </section>
  <Collapsible.Root
    bind:open={analysisOpen}
    onOpenChange={(open) => {
      if (open) void readAnalysis();
    }}
    ><Collapsible.Trigger class="records-analysis-trigger"
      >{analysisOpen ? "收起" : "展开"}模型 / 供应商用量分析</Collapsible.Trigger
    ><Collapsible.Content
      ><p class="records-analysis-note">
        长期累计 ·
        仅跟随时间、模型、供应商和协议筛选，清理明细后仍保留。费用为本地估算。
      </p>
      {#if analysisError}<p role="alert">{analysisError}</p>{/if}
      <div class="records-analysis">
        {#each ["model", "provider"] as kind}<section>
            <h2>{kind === "model" ? "模型" : "供应商"}统计</h2>
            <Table.Root
              ><Table.Header
                ><Table.Row
                  ><Table.Head>名称</Table.Head><Table.Head>请求</Table.Head
                  ><Table.Head>Token</Table.Head><Table.Head
                    >估算费用</Table.Head
                  ></Table.Row
                ></Table.Header
              ><Table.Body
                >{#each breakdown(kind as "model" | "provider") as item (item.id)}<Table.Row
                    ><Table.Cell>{item.name}</Table.Cell><Table.Cell
                      >{number(item.requests)}</Table.Cell
                    ><Table.Cell>{number(item.tokens)}</Table.Cell><Table.Cell
                      >{money(item.cost)}{#if item.unknown}<small
                          >{item.unknown} 条未知</small
                        >{/if}</Table.Cell
                    ></Table.Row
                  >{/each}</Table.Body
              ></Table.Root
            >
          </section>{/each}
      </div></Collapsible.Content
    ></Collapsible.Root
  >
</div>
{#snippet modelCell(row: RecordsRecord)}<strong
    class="record-model record-truncate"
    title={row.model || "未知模型"}>{row.model || "未知模型"}</strong
  >
  <div class="records-badges">
    {#if row.effort}<Badge variant="secondary">{row.effort}</Badge
      >{/if}{#if row.tier && row.tier !== "standard"}<Badge variant="secondary"
        >{row.tier}</Badge
      >{/if}
  </div>
  {#if row.upstreamModel && row.upstreamModel !== row.model}<small
      >→ {row.upstreamModel}</small
    >{/if}{/snippet}
{#snippet providerCell(row: RecordsRecord)}<strong
    class="record-truncate"
    title={row.providerName || "未发上游"}
    >{row.providerName ||
      (row.state === "local" ? "本地响应" : "未知供应商")}</strong
  ><small class="record-truncate" title={row.keyName || "—"}
    >{row.keyName || "—"}</small
  >{#if row.execution !== "direct"}<Badge variant="outline"
      >{label(executions, row.execution)}</Badge
    >{/if}{/snippet}
{#snippet stateCell(row: RecordsRecord)}<Badge
    variant={row.state === "failed"
      ? "destructive"
      : row.state === "success"
        ? "default"
        : "secondary"}>{label(states, row.state)}</Badge
  >{/snippet}
{#snippet usageCell(row: RecordsRecord)}{#if row.state === "local"}<span
      class="text-muted-foreground">— 无用量</span
    >{:else if row.usage?.unit}<strong
      >{number(row.usage.quantity)}
      {row.usage.unit === "image" ? "张" : "秒"}</strong
    ><small
      >{row.usage.specification} · {row.usage.unit === "image"
        ? "按次"
        : "按时长"}</small
    >{:else}<div class="records-token-usage" aria-label="Token 用量">
      <span>输入 {number(row.usage?.input)} Token</span>
      <span>输出 {number(row.usage?.output)} Token</span>
      <small
        title={`缓存写入 ${number(row.usage ? row.usage.cacheWrite + row.usage.cacheWrite1h : null)} Token`}
        >缓存 {number(row.usage?.cacheRead)} Token</small
      >
      <small>命中 {cache(row.usage)}</small>
    </div>{/if}{/snippet}
{#snippet timingCell(row: RecordsRecord)}{#if row.state === "local"}<span
      >本地响应</span
    ><small>{duration(row.durationMillis)}</small>{:else}<span
      >{duration(row.firstByteMillis)} / {duration(
        row.state === "active" ? now - row.startedAt : row.durationMillis,
      )}</span
    ><small
      >{row.outputRate == null ? "—" : `${number(row.outputRate)} tps`}</small
    >{/if}{/snippet}
<Detail id={selected} onclose={() => (selected = "")} {notify} />
