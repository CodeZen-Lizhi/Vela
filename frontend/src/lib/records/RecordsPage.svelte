<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import * as Empty from "$lib/components/ui/empty";
  import { Hint } from "$lib/components/ui/tooltip";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import { onMount } from "svelte";
  import { isMyGo } from "mygo-runtime";
  import {
    Search,
    RefreshCw,
    Activity,
    ChevronLeft,
    ChevronRight,
    Check,
    Shuffle,
  } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import * as InputGroup from "$lib/components/ui/input-group";
  import * as Table from "$lib/components/ui/table";
  import * as Select from "$lib/components/ui/select";
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
    recordMoney,
    seconds,
    cache,
    date,
    emptyQuery,
    executionCounts,
    recordTarget,
  } from "./display";
  import "./records.css";
  import { tableLayout } from "$lib/list-layout/table-layout";
  import { recordColumns } from "$lib/list-layout/columns";
  /** 使用记录工作台读取真实分页；浏览器预览不生成演示请求或伪造空数据库。 */
  let {
    notify,
  }: {
    /** 操作成功与失败的轻量反馈。 */ notify: (message: string) => void;
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
  /** 本机实时刷新偏好；未设置时默认关闭，切页或重启后恢复。 */
  const realtimeStorageKey = "vela-records-realtime";
  let generation = 0,
    disposed = false,
    ready = false,
    refreshing = false,
    activeReading = false;
  /** 当前分页查询是否由实时轮询发起；关闭开关时只使这类查询失效。 */
  let realtimeLoading = false;
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
  /** 样本身份跟随已成功返回的查询，避免新筛选使用旧分页内容测量。 */
  let columnIdentity = $state<string | null>(null);
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
  /** 按本地自然日筛选；昨天截止到今天零点前一毫秒，全部不限制时间。 */
  function query(): Query {
    const query = { ...q };
    query.end = 0;
    if (range !== "all") {
      const day = new Date();
      day.setHours(0, 0, 0, 0);
      if (range === "yesterday") query.end = day.getTime() - 1;
      day.setDate(
        day.getDate() - (range === "today" ? 0 : range === "yesterday" ? 1 : range === "last7" ? 6 : 29),
      );
      query.start = day.getTime();
    } else query.start = 0;
    return query;
  }
  /** 读取分页及摘要并登记成功查询的测量身份；实时查询可在关闭时失效，失败保留已有快照。 */
  async function load(reset = false, automatic = false): Promise<void> {
    if (!desktop || disposed) return;
    const request = query();
    const requestedColumns = filterIdentity;
    if (reset) request.before = 0;
    const version = ++generation;
    realtimeLoading = automatic;
    loading = true;
    error = "";
    try {
      const next = await VelaService.requestRecords(request);
      if (disposed || version !== generation) return;
      result = next;
      columnIdentity = requestedColumns;
      q.before = next.before;
    } catch (cause) {
      if (!disposed && version === generation) error = errorMessage(cause);
    } finally {
      if (!disposed && version === generation) {
        loading = false;
        realtimeLoading = false;
      }
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
  /** 先持久化开关；关闭时废弃在途实时查询并保留列表，保存失败仍维持原状态。 */
  function toggleRealtime(): void {
    try {
      const next = !realtime;
      localStorage.setItem(realtimeStorageKey, String(next));
      realtime = next;
      if (!next && realtimeLoading) {
        generation++;
        loading = false;
        realtimeLoading = false;
      }
    } catch (cause) {
      notify(`实时设置保存失败：${errorMessage(cause)}`);
    }
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
    if (desktop) {
      try {
        realtime = localStorage.getItem(realtimeStorageKey) === "true";
      } catch (cause) {
        notify(`实时设置读取失败：${errorMessage(cause)}`);
      }
    }
    ready = true;
    void load(true);
    const active = setInterval(() => {
      void updateActive();
    }, 1000);
    const live = setInterval(() => {
      if (realtime && q.page === 1 && !document.hidden && !loading)
        void load(true, true);
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
  <PageHeader title="使用记录" description="请求明细 · 用量、估算费用与执行路径" />
  <section class="records-surface list-surface" aria-label="请求记录">
  {#if !desktop}<Alert.Root variant="info"><Alert.Description>
      浏览器预览：请在 Vela 桌面应用中查询真实请求记录。
    </Alert.Description></Alert.Root>{/if}
  {#if error}<Alert.Root variant="destructive"><Alert.Description>
      {error}<Button variant="outline" onclick={refresh}>重试</Button>
    </Alert.Description></Alert.Root>{/if}
    <div class="records-filters">
        <InputGroup.Root class="records-search"
          ><InputGroup.Addon><Search /></InputGroup.Addon><InputGroup.Input
            aria-label="搜索请求记录"
            placeholder="搜索记录"

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
              { value: "yesterday", label: "昨天" },
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
          id="execution"
          title="链路"
          options={executions}
          multiple
          bind:values={q.executions}
          bind:openID
          disabled={!desktop}
        />
        <div class="records-actions records-toolbar-actions">
          <Button
            variant={realtime ? "live" : "outline"}
            size="sm"
            disabled={!desktop}
            aria-pressed={realtime}
            onclick={toggleRealtime}
            ><Activity aria-hidden="true" />实时</Button
          ><Button
            variant="outline"
            size="sm"
            disabled={!desktop || loading}
            onclick={refresh}><RefreshCw aria-hidden="true" />刷新</Button
          >
        </div>
    </div>
    <!-- 独立滚动区需要键盘焦点，支持方向键与翻页键浏览列表。 -->
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <div class="list-scroll" role="region" aria-label="使用记录列表内容" tabindex="0" use:tableLayout={{columns: recordColumns, identity: filterIdentity, ready: columnIdentity === filterIdentity && Boolean(result?.records.length)}}>
    {#if loading && !result}<Empty.Root role="status">
        正在读取请求记录…
      </Empty.Root>{:else if result && !result.records.length}<Empty.Root>
        <Empty.Title>
          {summary?.total
            ? "当前页没有记录"
            : filtered
              ? "没有匹配的请求记录"
              : "暂无请求记录"}
        </Empty.Title>
        <Empty.Description>调整筛选，或在产生真实请求后刷新。</Empty.Description>
      </Empty.Root>{/if}
    <div class="records-table" aria-busy={loading}>
      <Table.Root>
        <colgroup>
          <col class="record-col-time" /><col class="record-col-model" />
          <col class="record-col-provider" /><col class="record-col-protocol" />
          <col class="record-col-state" /><col class="record-col-type" />
          <col class="record-col-usage" /><col class="record-col-cost" /><col class="record-col-timing" />
        </colgroup>
        <Table.Header
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
            ><Table.Head><Filter id="status" column title="状态" options={states} multiple bind:values={q.states} bind:openID disabled={!desktop} /></Table.Head
            ><Table.Head
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
            ><Table.Head class="normal-case" ><div class="record-column-heading"><span>Token</span><span class="record-column-help">输入 / 输出<br />缓存 / 命中</span></div></Table.Head
            ><Table.Head>费用</Table.Head><Table.Head
              ><div class="record-column-heading"><span>端到端</span><span class="record-column-help">首字 / 总耗时<br />输出速度</span></div></Table.Head
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
                ><strong class="record-value">{date(row.startedAt)}</strong
                ><small>{date(row.startedAt, "day")}</small></Table.Cell
              ><Table.Cell>{@render modelCell(row)}</Table.Cell><Table.Cell
                >{@render providerCell(row)}</Table.Cell
              ><Table.Cell
                ><span class="record-protocol"><span>{protocol(row.protocol).split(" ")[0]}</span><span>{protocol(row.protocol).split(" ").slice(1).join(" ")}</span></span
                ></Table.Cell
              ><Table.Cell>{@render stateCell(row)}</Table.Cell><Table.Cell>{@render typeCell(row)}</Table.Cell
              ><Table.Cell>{@render usageCell(row)}</Table.Cell><Table.Cell
                >{#if row.state === "active" || row.standardCost == null && row.cost == null}
                  <span class="record-missing" aria-label={row.state === "active" ? "费用待请求结束后展示" : "费用未知"}>—</span>
                {:else}
                  <div class="record-cost-pair">
                    <span class="record-value record-cost" class:record-missing={row.standardCost == null || row.standardCost === 0} aria-label={`标准费用 ${recordMoney(row.standardCost)}`}>{recordMoney(row.standardCost)}</span>
                    <small class="record-value" aria-label={`倍率后费用 ${recordMoney(row.cost)}`}>{recordMoney(row.cost)}</small>
                  </div>
                {/if}</Table.Cell
              ><Table.Cell>{@render timingCell(row)}</Table.Cell></Table.Row
            >{/each}</Table.Body
        ></Table.Root
      >
    </div>
    </div>
    <footer class="records-pagination list-footer">
      <div class="records-pagination-summary">
        <span>共 {number(summary?.total)} 条</span>
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
<!-- 参数紧随模型名且保持同一行；长名称省略，上游映射保留独立信息行。 -->
{#snippet modelCell(row: RecordsRecord)}
  <div class="record-model-line" data-column-line>
    <Hint overflowOnly text={row.model || "未知模型"}>{#snippet children(hintProps4)}<strong {...hintProps4} class="record-model" data-column-text>{row.model || "未知模型"}</strong>{/snippet}</Hint>
    {#if row.effort}<Badge variant="parameter">{row.effort}</Badge>{/if}
    {#if row.tier && row.tier.toLowerCase() !== "standard"}<Badge variant="parameter">{row.tier}</Badge>{/if}
  </div>
  <Hint overflowOnly text={`→ ${row.upstreamModel || "—"}`}>{#snippet children(hintProps5)}<small {...hintProps5} class="record-truncate" data-column-line data-column-text >→ {row.upstreamModel || "—"}</small>{/snippet}</Hint>
{/snippet}
<!-- 执行供应商或已知本地阻断供应商与 Key 分两行展示，只有真实尝试才计算重试/转移。 -->
{#snippet providerCell(row: RecordsRecord)}
  {@const activity = executionCounts(row)}
  {@const target = recordTarget(row)}
  <div class="record-provider-line" data-column-line><Hint overflowOnly text={target.providerName}>{#snippet children(hintProps6)}<strong {...hintProps6} class="record-truncate" data-column-text>{target.providerName}</strong>{/snippet}</Hint>
    {#if activity.transfers}<Badge  variant="warning"  aria-label={`转移 ${activity.transfers} 次`}><Shuffle aria-hidden="true" />{activity.transfers}</Badge>{/if}
    {#if activity.retries}<Badge  variant="info"  aria-label={`重试 ${activity.retries} 次`}><RefreshCw aria-hidden="true" />{activity.retries}</Badge>{/if}
  </div>
  <!-- 仅实际执行身份显示倍率快照；本地阻断及未采集记录不伪造倍率。 -->
  <small class="record-key-line" data-column-line><Hint overflowOnly text={target.keyName}>{#snippet children(hintProps9)}<span {...hintProps9} class="record-truncate" data-column-text>{target.keyName}</span>{/snippet}</Hint>{#if row.state !== "local" && row.keyName && row.multiplier != null}<span class="record-key-multiplier">（×{row.multiplier}）</span>{/if}</small>
{/snippet}
<!-- 状态单独显示和筛选，进行中保留既定动画。 -->
{#snippet stateCell(row: RecordsRecord)}
  {#if row.state === "active"}<Badge variant="processing"><span class="vela-processing-label">进行中</span></Badge>
  {:else}<Badge variant={row.state === "success" ? "positive" : row.state === "failed" ? "negative" : row.state === "local" ? "warning" : "neutral"}>{#if row.state === "success"}<Check aria-hidden="true" />{/if}{label(states, row.state)}</Badge>{/if}
{/snippet}
<!-- 类型与客户端传输独立于最终状态，未知类型保留原值。 -->
{#snippet typeCell(row: RecordsRecord)}<div class="record-type-status">
  <Badge variant="category">{label(types, row.type)}</Badge>
  <Badge variant={row.transport === "websocket" || row.stream ? "positive" : "neutral"}>
    {#if row.transport === "websocket"}WS{:else if row.stream}流式{:else}标准{/if}
  </Badge>
</div>{/snippet}
<!-- 进行中及整项未知只显示一个短横线，结束后区分真实零值、媒体和 Token 用量。 -->
{#snippet usageCell(row: RecordsRecord)}{#if row.state === "active"}<span
      class="text-muted-foreground"  aria-label="用量待请求结束后展示">—</span
    >{:else if row.state === "local"}<span
      class="text-muted-foreground" aria-label="未发上游，无上游用量">—</span
    >{:else if !row.usage}<span class="text-muted-foreground"  aria-label="用量未知">—</span
    >{:else if row.usage.unit}<strong>{number(row.usage.quantity)}
      {row.usage.unit === "image" ? "张" : "秒"}</strong
    ><small
      >{row.usage.specification} · {row.usage.unit === "image"
        ? "按次"
        : "按时长"}</small
    >{:else}<div class="records-token-usage" aria-label="Token 用量" >
      <div><span class="record-input" class:record-missing={!row.usage} aria-label={`输入 ${row.usage?.input ?? "未知"} Token`}>{compact(row.usage?.input)}</span><span class="record-separator" aria-hidden="true">/</span><span class="record-output" class:record-missing={!row.usage} aria-label={`输出 ${row.usage?.output ?? "未知"} Token`}>{compact(row.usage?.output)}</span></div>
      <div><span class="record-cache" aria-label={`缓存读取 ${row.usage?.cacheRead ?? "未知"} Token`}>{compact(row.usage?.cacheRead)}</span><span class="record-separator" aria-hidden="true">/</span><span class="record-hit" class:record-missing={row.usage.context <= 0} aria-label={`缓存命中率 ${cache(row.usage)}`}>{cache(row.usage)}</span></div>
    </div>{/if}{/snippet}
{#snippet timingCell(row: RecordsRecord)}<div class="record-timing-pair" >
      <span class="record-first-byte">{seconds(row.state === "local" ? null : row.firstByteMillis)}</span><span class="record-separator">/</span><span class="record-total-time">{seconds(row.state === "active" ? Math.max(0, clock.now - row.startedAt) : row.durationMillis)}</span>
    </div>{#if row.state !== "active" && row.state !== "local"}<span class="record-speed" class:record-missing={row.outputRate == null}>{row.outputRate == null ? "—" : `${compact(row.outputRate)} tps`}</span>{/if}{/snippet}
<Detail id={selected} onclose={() => (selected = "")} {notify} />
