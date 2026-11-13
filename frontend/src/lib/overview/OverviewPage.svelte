<script lang="ts">
  import { onMount } from "svelte";
  import { isMyGo } from "mygo-runtime";
  import { Activity, ArrowRight, BarChart3, CalendarDays, Check, DollarSign, LineChart, Maximize2, RefreshCw, Settings2, ShieldAlert, TrendingDown, TrendingUp, Wallet, X, Zap } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { Skeleton } from "$lib/components/ui/skeleton";
  import { Hint } from "$lib/components/ui/tooltip";
  import * as Card from "$lib/components/ui/card";
  import * as Alert from "$lib/components/ui/alert";
  import * as Empty from "$lib/components/ui/empty";
  import * as Dialog from "$lib/components/ui/dialog";
  import * as ToggleGroup from "$lib/components/ui/toggle-group";
  import * as Field from "$lib/components/ui/field";
  import Choice from "$lib/providers/Choice.svelte";
  import AttentionSettings from "$lib/settings/AttentionSettings.svelte";
  import { errorMessage } from "$lib/settings/sections";
  import { VelaService, type OverviewSnapshot } from "$lib/generated/mygo";
  import { attention, applyAttentionPreferences, markRateRead, refreshAttention } from "./attention.svelte";
  import { cacheRate, compact, dateKey, formatMetric, groupRows, heatDays, shiftDay, successRate, summarize, timeBuckets, type Dimension, type Metric } from "./data";
  import Trend from "./Trend.svelte";
  import Details from "./Details.svelte";
  import "./overview.css";

  /** 总览通过现有主导航进入配置，关注事项自身不提供跳转。 */
  let { navigate, notify }: {
    /** 首次配置与接入提示的导航目标。 */ navigate: (target: "providers" | "connection") => void;
    /** 设置保存反馈。 */ notify: (message: string) => void;
  } = $props();
  /** 浏览器预览不伪造已连接数据库或真实统计。 */
  const desktop = isMyGo();
  /** 最后成功快照；后续读取失败保留它并展示错误。 */
  let snapshot = $state<OverviewSnapshot | null>(null), error = $state(""), loading = $state(desktop);
  /** 统计时钟与日期草稿仅属于当前总览，不修改调用记录。 */
  let now = $state(Date.now()), range = $state("today"), customStart = $state(""), customEnd = $state("");
  /** 日期、明细与关注设置均按需打开。 */
  let dateOpen = $state(false), detailsOpen = $state(false), settingsOpen = $state(false), guideDismissed = $state(false);
  /** 分布与明细共用维度；趋势与分布共用指标和费用口径。 */
  let dimension = $state<Dimension>("model"), metricTab = $state("cost"), basis = $state("standard"), chartStyle = $state("bar");
  /** 年度热力图独立保留全年范围，点击日期才改变顶部范围。 */
  let heatMetric = $state<Metric>("tokens"), heatYear = $state(String(new Date().getFullYear()));
  /** 固定统计日和时区与服务端一致。 */
  const timezone = $derived(snapshot?.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone);
  const today = $derived(dateKey(now, timezone));
  const firstDay = $derived(snapshot?.firstDay || today);
  const start = $derived(range === "all" ? firstDay : range === "custom" ? customStart : shiftDay(today, range === "week" ? -6 : range === "month" ? -29 : 0));
  const end = $derived(range === "custom" ? customEnd : today);
  const dateError = $derived(range === "custom" && (!/^\d{4}-\d{2}-\d{2}$/.test(start) || !/^\d{4}-\d{2}-\d{2}$/.test(end) || start > end || start < firstDay || end > today));
  const period = $derived(range === "today" ? new Intl.DateTimeFormat("zh-CN", { timeZone: timezone, month: "long", day: "numeric", weekday: "long" }).format(now) : `${start} — ${end}${range === "all" ? " · 全部累计" : ""}`);
  const rows = $derived(snapshot?.rows ?? []);
  const logical = $derived(rows.filter(row => !row.keyId));
  const filtered = $derived(dateError ? [] : rows.filter(row => row.day >= start && row.day <= end));
  const total = $derived(summarize(filtered.filter(row => !row.keyId)));
  const success = $derived(successRate(total)), cache = $derived(cacheRate(total));
  const metric = $derived<Metric>(metricTab === "tokens" ? "tokens" : metricTab === "requests" ? "requests" : basis === "actual" ? "cost" : "standardCost");
  const metricTitle = $derived(metric === "standardCost" ? "标准费用" : metric === "cost" ? "实际支出" : metric === "tokens" ? "Token" : "请求");
  const buckets = $derived(dateError ? [] : timeBuckets(filtered.filter(row => !row.keyId), start, end, timezone));
  const distribution = $derived(groupRows(filtered, dimension).sort((a, b) => b.total[metric] - a.total[metric] || a.id.localeCompare(b.id)));
  const distributionTotal = $derived(distribution.reduce((sum, group) => sum + group.total[metric], 0));
  const days = $derived(heatDays(Number(heatYear), firstDay, today, logical));
  const heatMaximum = $derived(Math.max(1e-9, ...days.map(day => day.total[heatMetric])));
  const heatYears = $derived(Array.from({ length: Math.max(1, Number(today.slice(0, 4)) - Number(firstDay.slice(0, 4)) + 1) }, (_, index) => ({ value: String(Number(today.slice(0, 4)) - index), label: `${Number(today.slice(0, 4)) - index} 年` })));
  const attentionCount = $derived(attention.preferences ? [attention.preferences.balance, attention.preferences.difference, attention.preferences.health, attention.preferences.rate].filter(Boolean).length : 0);
  /** 图标与文字共同表达事实，颜色不作为唯一识别依据。 */
  const attentionIcons = { balance: Wallet, difference: ShieldAlert, health: Activity, "rate-up": TrendingUp, "rate-down": TrendingDown };

  /** 更新本地统计，读取失败不丢失上一次成功快照。 */
  async function load(): Promise<void> {
    try { snapshot = await VelaService.overview(); now = Date.now(); error = ""; }
    catch (cause) { error = errorMessage(cause); }
    finally { loading = false; }
  }
  /** 切换普通范围关闭日期草稿面板，已有自定义日期保留。 */
  function changeRange(value: string): void { if (value) { range = value; dateOpen = false; } }
  /** 自定义日期默认最近七天且不早于统计起点。 */
  function toggleDates(): void {
    if (!customStart) { customStart = [firstDay, shiftDay(today, -6)].sort().at(-1) ?? today; customEnd = today; }
    dateOpen = range === "custom" ? !dateOpen : true; range = "custom";
  }
  /** 热力图选日联动全部卡片、趋势、分布和明细，年度视图继续保持全年。 */
  function selectDay(day: string): void { customStart = day; customEnd = day; range = "custom"; dateOpen = false; }
  /** 关闭首次接入提示只影响当前本机会话。 */
  function dismissGuide(): void {
    guideDismissed = true;
    try { sessionStorage.setItem("vela-connect-dismissed", "1"); } catch { /* 存储不可用时仍允许当前页关闭提示。 */ }
  }
  /** 保存成功后同步同一份偏好并通知，设置编辑器负责持久化与失败草稿。 */
  function settingsSaved(message: string): void {
    if (attention.preferences) applyAttentionPreferences(attention.preferences);
    notify(message);
  }
  /** 失败重试同时重读统计与关注事实，不访问上游。 */
  async function retry(): Promise<void> {
    await load();
    try { await refreshAttention(); } catch (cause) { attention.error = errorMessage(cause); }
  }
  onMount(() => {
    try { guideDismissed = sessionStorage.getItem("vela-connect-dismissed") === "1"; } catch { /* 不影响统计查询。 */ }
    if (!desktop) return;
    let stopped = false, running = false;
    let timer: ReturnType<typeof setTimeout>;
    /** 页面可见时串行重读，卸载释放轮询。 */
    const poll = async (): Promise<void> => {
      if (stopped || running || document.hidden) return;
      running = true; await load(); running = false;
      if (!stopped) timer = setTimeout(() => void poll(), attention.seconds * 1000);
    };
    /** 恢复窗口后立即更新统计，旧定时器不再重复触发。 */
    const visible = (): void => { if (!document.hidden) { clearTimeout(timer); void poll(); } };
    document.addEventListener("visibilitychange", visible);
    void poll();
    return () => { stopped = true; clearTimeout(timer); document.removeEventListener("visibilitychange", visible); };
  });
</script>

<div class="overview-page">
  <header class="overview-header"><div class="overview-title"><h1>总览</h1><p>{period}</p></div>
    <div class="overview-actions">
      <ToggleGroup.Root spacing={1} type="single" value={range === "custom" ? "" : range} onValueChange={changeRange} variant="plain" size="xs" aria-label="统计时间范围" disabled={!snapshot}>
        <ToggleGroup.Item value="today">今日</ToggleGroup.Item><ToggleGroup.Item value="week">7 天</ToggleGroup.Item><ToggleGroup.Item value="month">30 天</ToggleGroup.Item><ToggleGroup.Item value="all">全部</ToggleGroup.Item>
      </ToggleGroup.Root>
      <Hint text="自定义日期">{#snippet children(props)}<Button {...props} variant="ghost" size="icon-sm" aria-label="自定义日期" aria-expanded={dateOpen} aria-pressed={range === "custom"} onclick={toggleDates} disabled={!snapshot}><CalendarDays /></Button>{/snippet}</Hint>
    </div>
  </header>
  {#if !desktop}<Alert.Root><Alert.Title>请在 Vela 桌面应用中查看用量</Alert.Title><Alert.Description>总览读取本机真实请求统计，浏览器预览未连接本机数据库。</Alert.Description></Alert.Root>{/if}
  {#if error}<Alert.Root variant="destructive"><Alert.Title>统计更新失败</Alert.Title><Alert.Description>{error}{snapshot ? "，已保留上次数据。" : ""}<Button variant="text" size="xs" onclick={retry}><RefreshCw />重试</Button></Alert.Description></Alert.Root>{/if}
  {#if dateOpen}
    <section class="overview-date-panel" aria-label="自定义日期范围"><Field.Group class="overview-date-fields"><Field.Field><Label for="overview-date-start">开始日期</Label><Input id="overview-date-start" type="date" min={firstDay} max={today} bind:value={customStart} aria-invalid={dateError} /></Field.Field><span aria-hidden="true">—</span><Field.Field><Label for="overview-date-end">结束日期</Label><Input id="overview-date-end" type="date" min={firstDay} max={today} bind:value={customEnd} aria-invalid={dateError} /></Field.Field></Field.Group><div class="overview-date-actions"><span>按统计时区 {timezone} 统计全部调用</span><Button size="xs" variant="secondary" onclick={() => dateOpen = false} disabled={dateError}>完成</Button></div></section>
  {/if}
  {#if dateError}<p class="overview-error" role="alert">请选择统计起始至今日之间的有效日期，开始日期不能晚于结束日期。</p>{/if}
  {#if loading}<div class="overview-metrics" aria-label="正在读取统计" aria-busy="true">{#each [1,2,3] as item}<Skeleton class="h-[166px]" />{/each}</div>{/if}
  {#if snapshot}
    {#if attention.loaded && !attention.providerCount}
      <Card.Root aria-label="首次配置"><Card.Header><Card.Title>完成首次配置</Card.Title></Card.Header><Card.Content><ol class="overview-setup"><li><span>1</span><Button variant="text" size="inline" onclick={() => navigate("providers")}>添加供应商 <ArrowRight /></Button></li><li><span>2</span>配置 Key 与模型</li><li><span>3</span>接入常用工具</li></ol></Card.Content></Card.Root>
    {:else if attention.loaded && !logical.some(row => row.metrics.requests > 0) && !guideDismissed}
      <Alert.Root class="overview-connect"><Alert.Title>{attention.ready ? "模型已就绪，试一次调用" : "继续完成模型配置"}</Alert.Title><Alert.Description>{attention.ready ? "在接入设置中获取地址与访问凭据。首次调用后，这条提示会自动收起。" : "为供应商添加 Key，并关联需要使用的模型。"}</Alert.Description><div class="overview-connect-actions"><Button variant="secondary" size="xs" onclick={() => navigate(attention.ready ? "connection" : "providers")}>{attention.ready ? "查看接入配置" : "配置供应商"}<ArrowRight /></Button><Button variant="ghost" size="icon-xs" aria-label="关闭首次调用提示" onclick={dismissGuide}><X /></Button></div></Alert.Root>
    {/if}
    <section class="overview-metrics" aria-label="当前范围用量汇总">
      <Card.Root class="overview-stat"><Card.Header><Card.Title><span class="overview-metric-head"><span class="overview-metric-icon overview-request"><Activity size={15} /></span><span class="text-caution">请求</span></span></Card.Title></Card.Header><Card.Content><strong class="overview-stat-number">{total.requests.toLocaleString("en-US")}</strong><div class="overview-facts"><span>成功 <b class="text-positive">{total.detailsKnown === total.requests ? total.success.toLocaleString("en-US") : "—"}</b></span><span>失败 <b class:text-destructive={total.failed > 0}>{total.detailsKnown === total.requests ? total.failed.toLocaleString("en-US") : "—"}</b></span></div><Badge variant="neutral">成功率 {success === null ? "—" : `${success.toFixed(1)}%`}</Badge></Card.Content></Card.Root>
      <Card.Root class="overview-stat"><Card.Header><Card.Title><span class="overview-metric-head"><span class="overview-metric-icon overview-tokens"><Zap size={15} /></span><span class="text-muted-foreground">Token</span></span></Card.Title></Card.Header><Card.Content><Hint text={`已知 Token ${total.tokens.toLocaleString("en-US")}，未知用量 ${total.unknownUsage} 次`}>{#snippet children(props)}<strong {...props} class="overview-stat-number">{formatMetric(total,"tokens")}</strong>{/snippet}</Hint><div class="overview-facts"><span>输入 <b>{total.detailsKnown === total.requests && !total.unknownUsage ? compact(total.context - total.cached) : "—"}</b></span><span>输出 <b>{total.detailsKnown === total.requests && !total.unknownUsage ? compact(total.output) : "—"}</b></span><span>缓存读取 <b>{total.detailsKnown === total.requests && !total.unknownUsage ? compact(total.cached) : "—"}</b></span></div><Badge variant="neutral">缓存命中 {cache === null ? "—" : `${cache.toFixed(1)}%`}</Badge></Card.Content></Card.Root>
      <Card.Root class="overview-stat overview-cost-stat"><Card.Header><Card.Title><span class="overview-metric-head"><span class="overview-metric-icon overview-cost"><DollarSign size={15} /></span><span class="text-muted-foreground">费用</span></span></Card.Title></Card.Header><Card.Content><strong class="overview-stat-number text-primary">{formatMetric(total,"standardCost")}</strong><div class="overview-facts"><span>实际支出 <b>{formatMetric(total,"cost")}</b></span></div><Badge variant="neutral">标准费用 · USD</Badge></Card.Content></Card.Root>
    </section>
    {#if total.unknownUsage || total.unknownCost || total.unknownStandard || total.detailsKnown < total.requests}<p class="overview-data-note">部分历史字段或上游用量未知；“≥”表示已知部分合计，缺失项显示“—”。</p>{/if}
    {#if logical.length}
      <div class="overview-grid">
        <Card.Root class="overview-trend-card"><Card.Header class="overview-card-header"><Card.Title>用量趋势</Card.Title><div class="overview-chart-actions"><ToggleGroup.Root spacing={1} type="single" value={metricTab} onValueChange={value => {if(value) metricTab=value;}} variant="underline" size="xs" aria-label="趋势指标"><ToggleGroup.Item value="cost">费用</ToggleGroup.Item><ToggleGroup.Item value="tokens">Token</ToggleGroup.Item><ToggleGroup.Item value="requests">请求</ToggleGroup.Item></ToggleGroup.Root><ToggleGroup.Root spacing={1} type="single" value={chartStyle} onValueChange={value => {if(value)chartStyle=value;}} size="xs" variant="plain" aria-label="图表样式"><ToggleGroup.Item value="bar" aria-label="柱状图"><BarChart3 /></ToggleGroup.Item><ToggleGroup.Item value="line" aria-label="折线图"><LineChart /></ToggleGroup.Item></ToggleGroup.Root></div></Card.Header>
          <Card.Content class="overview-trend-caption"><span>{metricTitle}合计</span><div>{#if metricTab === "cost"}{@render costBasis()}{/if}<span>{buckets.length === 24 && start === end ? "每小时" : buckets.length && start !== end && buckets.length < (Date.parse(end)-Date.parse(start))/86400000 ? "每周" : "每天"}</span></div></Card.Content>
          {#if total.requests && !dateError}<Trend {buckets} {metric} style={chartStyle} />{:else}<Empty.Root><Empty.Header><Empty.Media variant="icon"><Activity /></Empty.Media><Empty.Title>{dateError ? "请调整日期范围" : "当前时间范围内暂无用量"}</Empty.Title><Empty.Description>换一个时间范围看看。</Empty.Description></Empty.Header></Empty.Root>{/if}
        </Card.Root>
        <Card.Root><Card.Header class="overview-card-header"><Card.Title>用量分布</Card.Title>{#if metricTab === "cost"}{@render costBasis()}{:else}<span class="overview-caption">{metricTitle}占比</span>{/if}<Button variant="text" size="inline" aria-label="查看用量明细" aria-haspopup="dialog" onclick={() => detailsOpen = true}>明细 <Maximize2 /></Button></Card.Header><Card.Content>
          <ToggleGroup.Root spacing={1} type="single" value={dimension} onValueChange={value => {if(value)dimension=value as Dimension;}} variant="segmented" size="xs" aria-label="分布分析维度"><ToggleGroup.Item value="model">模型</ToggleGroup.Item><ToggleGroup.Item value="provider">供应商</ToggleGroup.Item><ToggleGroup.Item value="key">上游 Key</ToggleGroup.Item></ToggleGroup.Root>
          {#if dimension === "key"}<p class="overview-caption mt-2">按实际出站尝试统计</p>{/if}
          <div class="overview-rank-list">{#each distribution.slice(0,5) as group,index}{@const share=distributionTotal?group.total[metric]/distributionTotal*100:0}<div class="overview-rank-row"><span class="overview-entity" style:opacity={1-index*.12}>{dimension === "key" ? "K" : group.name.slice(0,1)}</span><div class="overview-rank-name"><strong>{group.name}</strong>{#if group.detail}<small>{group.detail}</small>{/if}</div><div class="overview-rank-value"><span>{formatMetric(group.total,metric)}</span><small>{share.toFixed(1)}%</small></div><div class="overview-rank-track"><i style:width={`${share}%`} style:opacity={1-index*.12}></i></div></div>{:else}<p class="overview-caption py-6 text-center">暂无用量分布</p>{/each}</div>
        </Card.Content></Card.Root>
      </div>
    {/if}
    {#if attention.error}<Alert.Root variant="destructive"><Alert.Title>关注事项未能更新</Alert.Title><Alert.Description>{attention.error}<Button variant="text" size="xs" onclick={retry}>重试</Button></Alert.Description></Alert.Root>{/if}
    {#if attention.preferences && attentionCount}
      <Card.Root class="overview-attention" aria-label="需要关注">
        {#if attention.items.length}<Card.Header class="overview-card-header"><Card.Title>需要关注 <Badge variant="warning">{attention.items.length}</Badge></Card.Title><div class="overview-attention-tools"><span>已启用的调用范围</span>{@render attentionButton()}</div></Card.Header><Card.Content><ul>{#each attention.items as item}{@const Icon=attentionIcons[item.kind]}<li><span class="overview-attention-icon" class:overview-rate-down={item.kind === "rate-down"}><Icon size={16} /></span><div class="overview-attention-copy"><strong>{item.title}</strong><span>{item.path}</span></div><div class="overview-attention-detail"><span>{item.detail}</span>{#if item.kind.startsWith("rate-")}<Button variant="text" size="xs" aria-label={`标为已读：${item.path} ${item.detail}`} onclick={() => markRateRead(item.id)}><Check />已读</Button>{/if}</div></li>{/each}</ul></Card.Content>
        {:else}<Card.Content class="overview-attention-clear"><span class="overview-clear-icon"><Check size={15} /></span><div><strong>当前没有需要关注的事项</strong><small>{attentionCount} 项提醒已开启</small></div>{@render attentionButton()}</Card.Content>{/if}
      </Card.Root>
    {/if}
    {#if logical.length}
      <Card.Root class="overview-heatmap" aria-label="用量热力图"><Card.Header class="overview-card-header"><div><Card.Title>用量热力图</Card.Title><Card.Description>{heatYear} 年每日用量 · 统计始于 {firstDay}</Card.Description></div><div class="overview-heat-controls"><ToggleGroup.Root spacing={1} type="single" value={heatMetric} onValueChange={value => {if(value)heatMetric=value as Metric;}} variant="underline" size="xs" aria-label="热力图指标"><ToggleGroup.Item value="tokens">Token</ToggleGroup.Item><ToggleGroup.Item value="requests">请求</ToggleGroup.Item><ToggleGroup.Item value="standardCost">标准费用</ToggleGroup.Item></ToggleGroup.Root><div class="overview-year"><Choice bind:value={heatYear} options={heatYears} label="热力图年份" /></div></div></Card.Header><Card.Content>
        <div class="overview-heat-layout"><div class="overview-weekdays"><span>一</span><span>三</span><span>五</span><span>日</span></div><div class="overview-heat-scroll"><div class="overview-months" style:grid-template-columns={`repeat(${Math.ceil(days.length/7)},minmax(12px,1fr))`}>{#each days.filter((_,index)=>index%7===0) as day,index}{@const monthDay=days.slice(index*7,index*7+7).find(item=>item.day.endsWith("-01"))}<span>{monthDay?`${Number(monthDay.day.slice(5,7))}月`:""}</span>{/each}</div><div class="overview-heat-days" style:grid-template-columns={`repeat(${Math.ceil(days.length/7)},minmax(12px,1fr))`}>{#each days as day}{@const label=`${day.day} · ${day.recorded?`${formatMetric(day.total,heatMetric)} · ${day.total.requests} 次请求`:"未记录"}`}<Hint text={day.within?label:undefined}>{#snippet children(props)}<Button {...props} variant="heatmap" size="heatmap" class={day.within?"":"invisible"} style={`--heat-strength:${day.total[heatMetric]?25+day.total[heatMetric]/heatMaximum*75:0}%`} disabled={!day.recorded} aria-label={label} onclick={() => selectDay(day.day)} />{/snippet}</Hint>{/each}</div></div></div>
        <div class="overview-heat-legend"><span>点击日期查看当天趋势<span class="overview-heat-hint">热力图可左右滑动查看全年</span></span><span>少 {#each [0,25,50,75,100] as strength}<i style:background={`color-mix(in srgb,var(--primary) ${strength}%,var(--muted))`}></i>{/each} 多</span></div>
      </Card.Content></Card.Root>
    {/if}
    <p class="overview-footnote">全部调用（进行中不计入成功率） · 费用为价格快照估算（USD） · {timezone}</p>
  {/if}
</div>

{#snippet costBasis()}<ToggleGroup.Root spacing={1} type="single" value={basis} onValueChange={value=>{if(value)basis=value;}} variant="segmented" size="xs" aria-label="费用口径"><ToggleGroup.Item value="standard">标准</ToggleGroup.Item><ToggleGroup.Item value="actual">实际</ToggleGroup.Item></ToggleGroup.Root>{/snippet}
{#snippet attentionButton()}<Hint text="关注设置">{#snippet children(props)}<Button {...props} variant="ghost" size="icon-xs" aria-label="关注设置" aria-haspopup="dialog" onclick={()=>settingsOpen=true}><Settings2 /></Button>{/snippet}</Hint>{/snippet}
<Details bind:open={detailsOpen} rows={filtered} {period} bind:dimension />
{#if attention.preferences}<Dialog.Root bind:open={settingsOpen}><Dialog.Content class="w-[min(640px,92vw)] max-w-[92vw] sm:max-w-[min(640px,92vw)]"><Dialog.Header><Dialog.Title>关注设置</Dialog.Title><Dialog.Description>与设置页共用提醒偏好，仅控制展示。</Dialog.Description></Dialog.Header><AttentionSettings bind:saved={attention.preferences} {desktop} notify={settingsSaved} /></Dialog.Content></Dialog.Root>{/if}
