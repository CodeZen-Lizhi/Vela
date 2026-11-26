<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import * as Card from "$lib/components/ui/card";
  import * as ToggleGroup from "$lib/components/ui/toggle-group";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import Choice from "$lib/providers/Choice.svelte";
  import { ArrowUpRight, ArrowDownLeft, Copy } from "@lucide/svelte";
  import type { RecordsRecord } from "$lib/generated/mygo";
  import { duration, label, states } from "./display";
  import { conversationMessages, conversationCopy, formatSnapshot, messageBlocks } from "./exchange";
  import { elapsedClock } from "./elapsed-clock.svelte";
  import { attemptError } from "./execution";
  import JsonView from "./JsonView.svelte";

  /** 选定一段真实往返后并排查看请求/回复；视图和复制始终使用同一数据源。 */
  let { record, source = $bindable("client"), oncopy }: {
    /** 当前完整记录，包含客户端快照和各次实际尝试。 */ record: RecordsRecord;
    /** client 表示客户端往返，attempt:N 对应零基实际尝试。 */ source?: string;
    /** 复用桌面剪贴板与操作反馈。 */ oncopy: (text: string) => Promise<void>;
  } = $props();
  /** 内容默认可读，HTTP 头与原始正文分别使用格式化 JSON/事件。 */
  let view = $state("content");
  const clock = elapsedClock(() => record.state === "active");
  const current = $derived(source === "client" ? undefined : record.attempts[Number(source.slice(8))]);
  const payload = $derived(source === "client" ? record.client : current?.payload);
  /** 只引用选中往返的失败原因，重试成功不能把前次错误带入成功响应。 */
  const failure = $derived(source === "client" ? record.error : (current?.error || current && current.status >= 400) ? attemptError(current) : "");
  const options = $derived([
    { value: "client", label: "客户端 ↔ Vela" },
    ...record.attempts.map((item, index) => ({ value: `attempt:${index}`, label: `Vela ↔ ${item.providerName || "未知供应商"} / ${item.keyName || "未知 Key"} · 第 ${item.number} 次${item.polling ? " · 状态查询" : ""}` })),
  ]);
  /** 派生时只解析当前视图，避免浏览头部时处理大段正文。 */
  const panes = $derived(["request", "response"].map(direction => {
    const body = (direction === "request" ? payload?.requestBody : payload?.responseBody) ?? "";
    const text = view === "headers" ? (direction === "request" ? payload?.requestHeaders : payload?.responseHeaders) ?? "" : body;
    const expired = view !== "headers" && payload?.bodyState === "expired";
    const rows = view === "content" && !expired ? conversationMessages(body, direction as "request" | "response") : [];
    const formatted = expired ? "" : formatSnapshot(text);
    return { direction, rows, formatted, expired, copy: rows.length ? conversationCopy(rows) : formatted };
  }));

  /** 空数据按选中往返的实际状态解释，不把无响应、空正文和未采集混为一谈。 */
  function emptyMessage(direction: string): string {
    if (record.state === "active" && (!current || !current.endedAt && !current.durationMillis)) return "请求进行中，尚无此项数据。";
    if (view !== "headers") {
      if (payload?.bodyState === "cleared") return "正文已清理。";
      if (payload?.bodyState === "saveFailed") return "正文保存失败。";
      if (payload?.bodyState === "readFailed") return "正文文件无法读取。";
    }
    if (direction !== "response" || !current || !payload || payload.bodyState === "unavailable") return "此侧数据未采集或不适用。";
    if (!current.status) return "未收到上游 HTTP 响应，因此没有响应头和响应正文。";
    return view === "headers" ? "上游响应头为空。" : "上游未返回响应正文。";
  }
</script>

<section class="record-exchange" aria-label="请求与响应往返">
  <div class="record-exchange-toolbar">
    <Choice bind:value={source} label="查看往返" {options} />
    <ToggleGroup.Root type="single" value={view} onValueChange={value => { if (value) view = value; }} aria-label="展示方式" variant="segmented" size="sm" spacing={1}>
      <ToggleGroup.Item value="content">内容</ToggleGroup.Item>
      <ToggleGroup.Item value="headers">HTTP 头</ToggleGroup.Item>
      <ToggleGroup.Item value="raw">原始数据</ToggleGroup.Item>
    </ToggleGroup.Root>
  </div>
  <div class="record-exchange-columns">
    {#each panes as pane (pane.direction)}
      <Card.Root size="sm" class="record-exchange-pane" aria-label={pane.direction === "request" ? "请求内容" : "本次回复"}>
        <Card.Content>
          <header class="record-exchange-heading">
            <div>{#if pane.direction === "request"}<ArrowUpRight aria-hidden="true" />{:else}<ArrowDownLeft aria-hidden="true" />{/if}<h3>{pane.direction === "request" ? "请求内容" : "本次回复"}</h3></div>
            <Button variant="ghost" size="icon-sm" aria-label={`复制${pane.direction === "request" ? "请求" : "回复"}${view === "headers" ? "头 JSON" : view === "raw" ? "原始数据" : "内容"}`} disabled={!pane.copy} onclick={() => oncopy(pane.copy)}><Copy /></Button>
          </header>
          <div class="record-exchange-meta">
            {#if pane.direction === "request"}
              {#if source === "client"}<code>{record.method || ""} {record.path || "未记录请求端点"}</code>{:else}<code>{current?.path || "未记录上游端点"}</code>{/if}
            {:else}
              {#if source === "client"}<Badge variant={record.state === "success" ? "positive" : record.state === "failed" ? "negative" : record.state === "active" ? "processing" : "warning"}>{label(states, record.state)}</Badge><span>总耗时 {duration(record.state === "active" ? Math.max(0, clock.now - record.startedAt) : record.durationMillis)}</span><span>首字 {duration(record.firstByteMillis)}</span>
              {:else if current}<span>{current.status ? `HTTP ${current.status}` : "未收到 HTTP 响应"}</span><span>耗时 {duration(record.state === "active" && !current.endedAt && !current.durationMillis ? Math.max(0, clock.now - current.startedAt) : current.durationMillis)}</span><span>首字 {duration(current.firstByteMillis)}</span>{/if}
            {/if}
          </div>
          <div class="record-exchange-body">
            {#if pane.direction === "response" && failure}
              <Alert.Root variant="destructive"><Alert.Description>{failure}</Alert.Description></Alert.Root>
            {/if}
            {#if pane.expired}
              <Alert.Root variant="info"><Alert.Description>正文已超过保留期限。</Alert.Description></Alert.Root>
            {:else if !pane.formatted}
              <Alert.Root variant="info"><Alert.Description>{emptyMessage(pane.direction)}</Alert.Description></Alert.Root>
            {:else if view === "content" && pane.rows.length}
              {#each pane.rows as row}
                <article class="record-exchange-message"><h4>{row.role}</h4>
                  {#each messageBlocks(row.text) as block}
                    {#if block.code}<pre class="record-exchange-code"><code>{block.text}</code></pre>{:else}<p>{block.text}</p>{/if}
                  {/each}
                </article>
              {/each}
            {:else}
              {#if view === "content"}<p class="record-detail-note">此内容无法解析为会话，保留原始数据。</p>{/if}
              <JsonView text={pane.formatted} />
            {/if}
            {#if view !== "headers" && pane.formatted && payload?.bodyState === "truncated"}<p class="record-detail-note">快照已截断，不表示上游响应被截断。</p>{/if}
          </div>
        </Card.Content>
      </Card.Root>
    {/each}
  </div>
</section>
