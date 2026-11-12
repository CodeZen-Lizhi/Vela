<script lang="ts">
  import { VelaService, type RecordsRecord } from "$lib/generated/mygo";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import * as Dialog from "$lib/components/ui/dialog";
  import * as Collapsible from "$lib/components/ui/collapsible";
  import * as Table from "$lib/components/ui/table";
  import Choice from "$lib/providers/Choice.svelte";
  import { Copy, RefreshCw } from "@lucide/svelte";
  import { errorMessage } from "$lib/settings/sections";
  import {
    states,
    sources,
    types,
    protocol,
    label,
    number,
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
    panel = $state("trace"),
    side = $state("provider"),
    view = $state("json"),
    compare = $state(false),
    expanded = $state(true);
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
    attempt = "0";
    panel = "trace";
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

<Dialog.Root
  open={!!id}
  onOpenChange={(open) => {
    if (!open) onclose();
  }}
  ><Dialog.Content
    class="record-detail"
    onCloseAutoFocus={(event) => {
      event.preventDefault();
      const candidates = document.querySelectorAll<HTMLButtonElement>(
        `[data-record-id="${CSS.escape(viewedID)}"]`,
      );
      const trigger = Array.from(candidates).find(
        (element) => element.getClientRects().length > 0,
      );
      (
        trigger ??
        document.querySelector<HTMLButtonElement>('[aria-label="使用记录"]')
      )?.focus();
    }}
    ><Dialog.Header
      ><Dialog.Title>请求详情</Dialog.Title><Dialog.Description
        >{record?.id ?? id}</Dialog.Description
      ></Dialog.Header
    >
    {#if error}<p class="records-error" role="alert">
        {error}<Button variant="outline" onclick={() => load(id)}
          ><RefreshCw />重试</Button
        >
      </p>{/if}
    {#if loading && !record}<p role="status">正在读取请求详情…</p>{/if}
    {#if record}
      <section class="record-detail-heading">
        <div>
          <h2>{record.model || "未知模型"}</h2>
          <Badge
            variant={record.state === "failed" ? "destructive" : "secondary"}
            >{label(states, record.state)}</Badge
          >
        </div>
        <p>
          {date(record.startedAt, "day")}
          {date(record.startedAt)} · {protocol(record.protocol)} · {label(
            types,
            record.type,
          )} / {record.stream ? "流式" : "标准"} · {label(
            sources,
            record.source,
          )}
        </p>
        <p>
          {record.providerName || "未发上游"} / {record.keyName || "—"} · → {record.upstreamModel ||
            record.model}
        </p>
        <small
          >名称为请求发生时快照；价格按请求开始时固定。当前：{record.currentModelName}
          / {record.currentProviderName} / {record.currentKeyName}</small
        >
      </section>
      <section class="record-detail-metrics">
        <div><span>估算费用</span><strong>{money(record.cost)}</strong></div>
        <div>
          <span>端到端耗时</span><strong
            >{duration(record.durationMillis)}</strong
          >
        </div>
        <div>
          <span>首字</span><strong>{duration(record.firstByteMillis)}</strong>
        </div>
        <div>
          <span>输出 Token</span><strong>{number(record.usage?.output)}</strong>
        </div>
        <div>
          <span>输出速度</span><strong
            >{record.outputRate == null
              ? "—"
              : `${number(record.outputRate)} tps`}</strong
          >
        </div>
      </section>
      {#if record.error}<p class="records-error">{record.error}</p>{/if}
      <div class="record-detail-controls">
        <Choice
          bind:value={panel}
          label="详情分区"
          options={[
            { value: "trace", label: "执行链路" },
            { value: "billing", label: "用量与计费" },
            { value: "headers", label: "请求头" },
            { value: "response-headers", label: "响应头" },
            { value: "request", label: "请求正文" },
            { value: "response", label: "响应正文" },
          ]}
        />{#if panel !== "trace" && panel !== "billing"}<Choice
            bind:value={attempt}
            label="实际尝试"
            options={record.attempts.map((item, i) => ({
              value: String(i),
              label: `${item.number}. ${item.providerName} / ${item.keyName} · HTTP ${item.status || "—"}`,
            }))}
          /><Choice
            bind:value={side}
            label="数据来源"
            options={[
              { value: "provider", label: "上游" },
              { value: "client", label: "客户端" },
            ]}
          />{/if}
      </div>
      {#if panel === "trace"}
        <section>
          <h3>实际执行路径</h3>
          {#if record.circuitSourceId}<p class="records-notice">
              本地熔断 · 触发请求 <Button
                variant="link"
                onclick={() => {
                  viewedID = record!.circuitSourceId;
                  void load(viewedID);
                }}>{record.circuitSourceId}</Button
              >，可查看仍在保留期内的 HTTP 状态及原始错误。
            </p>{/if}{#if record.candidates?.length}<p
              class="records-analysis-note"
            >
              排序候选（不代表实际发送）
            </p>
            {#each record.candidates as candidate}<p
                class="records-analysis-note"
              >
                {candidate.providerName} / {candidate.keyName} · {candidate.reason ||
                  (record.attempts.some((a) => a.keyId === candidate.keyId)
                    ? "已尝试"
                    : "未执行")}
              </p>{/each}{/if}{#if !record.attempts.length}<p
              class="records-notice"
            >
              请求只在本地处理，未发送上游，没有上游 HTTP 响应。
            </p>{/if}
          <div class="record-trace">
            {#each record.attempts as item}<article>
                <span>尝试 {item.number}</span><strong
                  >{item.providerName} / {item.keyName}</strong
                ><code>{item.path}</code>
                <p>
                  HTTP {item.status || "未收到响应"} · {duration(
                    item.durationMillis,
                  )}
                </p>
                <small
                  >{item.error ||
                    "已收到上游响应；最终结论以请求状态为准"}</small
                ><Button
                  variant="link"
                  onclick={() => {
                    attempt = String(item.number - 1);
                    panel = "response";
                    side = "provider";
                  }}>查看本次响应</Button
                >
              </article>{/each}
          </div>
        </section>
      {:else if panel === "billing"}
        <section>
          <h3>用量与估算费用</h3>
          <p>不是中转站已对账扣款；未知价格或用量不补零。</p>
          <div class="record-detail-metrics">
            <div>
              <span>输入</span><strong>{number(record.usage?.input)}</strong>
            </div>
            <div>
              <span>输出</span><strong>{number(record.usage?.output)}</strong>
            </div>
            <div>
              <span>缓存读取</span><strong
                >{number(record.usage?.cacheRead)}</strong
              >
            </div>
            <div>
              <span>缓存写入 5m / 1h</span><strong
                >{number(record.usage?.cacheWrite)} / {number(
                  record.usage?.cacheWrite1h,
                )}</strong
              >
            </div>
            <div>
              <span>完整输入上下文</span><strong
                >{number(record.usage?.context)}</strong
              >
            </div>
          </div>
          {#if record.usage?.unit}<p>
              {record.usage.quantity}
              {record.usage.unit} · {record.usage.specification}
            </p>{/if}<Table.Root
            ><Table.Header
              ><Table.Row
                ><Table.Head>计价维度</Table.Head><Table.Head
                  >实际数量</Table.Head
                ><Table.Head>美元单价</Table.Head><Table.Head
                  >估算金额</Table.Head
                ></Table.Row
              ></Table.Header
            ><Table.Body
              >{#each record.charges as charge}<Table.Row
                  ><Table.Cell>{charge.key}</Table.Cell><Table.Cell
                    >{number(charge.quantity)}</Table.Cell
                  ><Table.Cell>{money(charge.price)}</Table.Cell><Table.Cell
                    >{money(charge.amount)}</Table.Cell
                  ></Table.Row
                >{/each}</Table.Body
            ></Table.Root
          >
          <h3>开始时价格快照</h3>
          <pre>{JSON.stringify(record.pricing, null, 2)}</pre>
        </section>
      {:else}
        <section class="record-payload">
          <div class="record-payload-toolbar">
            <h3>
              {panel === "headers"
                ? "请求头"
                : panel === "response-headers"
                  ? "响应头"
                  : panel === "request"
                    ? "请求正文"
                    : "响应正文"}
            </h3>
            <div class="records-actions">
              {#if panel === "request" || panel === "response"}<Button
                  variant="outline"
                  onclick={() =>
                    (view = view === "json" ? "conversation" : "json")}
                  >{view === "json" ? "对话视图" : "JSON 视图"}</Button
                >{:else}<Button
                  variant="outline"
                  aria-pressed={compare}
                  onclick={() => (compare = !compare)}>对比上下游</Button
                >{/if}{#if panel === "request"}<Button
                  variant="outline"
                  disabled={!curl()}
                  onclick={() => copy(curl())}>复制 cURL</Button
                >{/if}<Button
                variant="ghost"
                size="icon-sm"
                aria-label="复制当前内容"
                disabled={!content}
                onclick={() => copy(content)}><Copy /></Button
              >
            </div>
          </div>
          {#if payload?.bodyState === "expired" && (panel === "request" || panel === "response")}<p
              class="records-notice"
            >
              正文已超过 7 天保留期限，摘要与诊断仍可查询。
            </p>{:else if !content}<p class="records-notice">
              {record.state === "active"
                ? "请求进行中，尚无此项数据。"
                : "此侧数据未采集或不适用；没有生成替代内容。"}
            </p>{:else if compare && (panel === "headers" || panel === "response-headers")}<Table.Root
              ><Table.Header
                ><Table.Row
                  ><Table.Head>头名称</Table.Head><Table.Head>客户端</Table.Head
                  ><Table.Head>上游</Table.Head></Table.Row
                ></Table.Header
              ><Table.Body
                >{#each headers(panel === "headers" ? (record.client?.requestHeaders ?? "") : (record.client?.responseHeaders ?? ""), panel === "headers" ? (current?.payload?.requestHeaders ?? "") : (current?.payload?.responseHeaders ?? "")) as row}<Table.Row
                    ><Table.Cell>{row.name}</Table.Cell><Table.Cell
                      >{row.left}</Table.Cell
                    ><Table.Cell>{row.right}</Table.Cell></Table.Row
                  >{/each}</Table.Body
              ></Table.Root
            >{:else if view === "conversation" && (panel === "request" || panel === "response") && conversation(content).length}{#each conversation(content) as line}<article
                class="record-message"
              >
                <strong>{line.role}</strong>
                <pre>{line.text}</pre>
              </article>{/each}{:else}<Collapsible.Root bind:open={expanded}>
              <Collapsible.Trigger
                >{expanded ? "收起 JSON" : "展开 JSON"}</Collapsible.Trigger
              >
              <Collapsible.Content
                ><pre>{pretty(content)}</pre></Collapsible.Content
              >
            </Collapsible.Root>{/if}{#if payload?.bodyState === "truncated"}<p>
              快照已截断，不表示上游响应被截断。
            </p>{/if}
        </section>
      {/if}
    {/if}
  </Dialog.Content></Dialog.Root
>
