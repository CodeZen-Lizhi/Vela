<script lang="ts">
  import { Hint } from "$lib/components/ui/tooltip";
  import * as Alert from "$lib/components/ui/alert";
  import * as Empty from "$lib/components/ui/empty";
  import { onMount, onDestroy } from "svelte";
  import {
    Activity,
    CheckCircle2,
    ChevronDown,
    ChevronRight,
    FlaskConical,
    KeyRound,
    Layers3,
    Loader2,
    Play,
    RotateCcw,
    Server,
    SlidersHorizontal,
    Square,
    XCircle,
  } from "@lucide/svelte";
  import { Badge } from "$lib/components/ui/badge";
  import { Button } from "$lib/components/ui/button";
  import { Label } from "$lib/components/ui/label";
  import { Textarea } from "$lib/components/ui/textarea";
  import * as Dialog from "$lib/components/ui/dialog";
  import * as Table from "$lib/components/ui/table";
  import { VelaService, type ProviderModel } from "$lib/generated/mygo";
  import Choice from "./Choice.svelte";
  import { Input } from "$lib/components/ui/input";

  /** 诊断输入与结果直接复用绑定契约，避免维护重复协议定义。 */
  type TestRequest = Parameters<typeof VelaService.runProviderModelTest>[0];
  type TestResult = Awaited<ReturnType<typeof VelaService.runProviderModelTest>>;
  type TestOptions = Awaited<ReturnType<typeof VelaService.providerModelTestOptions>>;
  type TestAttempt = TestResult["attempts"][number];
  /** 当前模型固定在调用行；完成后刷新父级最近结果，关闭不清除持久化记录。 */
  let { providerID, model, onclose, oncompleted }: {
    providerID: string;
    model: ProviderModel;
    onclose: () => void;
    oncompleted: () => Promise<void>;
  } = $props();
  /** 每次打开从空结果和展开配置开始；本次测试后收起配置，保留当前范围摘要。 */
  let options = $state<TestOptions | null>(null);
  let loading = $state(true);
  let keyID = $state("");
  let protocol = $state("chat");
  let requestModel = $state("");
  let message = $state("请简短回复 OK。");
  let running = $state(false);
  let stopping = $state(false);
  let requestID = $state("");
  let error = $state("");
  let result = $state<TestResult | null>(null);
  let configExpanded = $state(true);
  let selectedAttempt = $state<number | null>(null);
  /** 仅按统一模型 kind 开放对应真实协议，不从名称猜测能力。 */
  const protocols = $derived(options?.kind === "image"
    ? [{ value: "image", label: "Images" }]
    : options?.kind === "video"
      ? [{ value: "video", label: "Videos" }]
      : options?.kind === "vector"
        ? [{ value: "embedding", label: "Embeddings" }, { value: "rerank", label: "Rerank" }]
        : [
            { value: "chat", label: "OpenAI Chat Completions" },
            { value: "responses", label: "OpenAI Responses" },
            { value: "anthropic", label: "Anthropic Messages" },
            { value: "gemini", label: "Gemini GenerateContent" },
          ]);
  const selectedProtocolLabel = $derived(protocols.find((item) => item.value === protocol)?.label ?? protocol);
  /** 空候选明确说明不可测试；单候选直接显示名称，多候选才提供自动选择。 */
  const selectedKeyLabel = $derived(!options?.keys.length ? "暂无可测试 Key" : options.keys.length === 1 ? options.keys[0]?.label ?? "暂无可测试 Key" : keyID
    ? options.keys.find((item) => item.value === keyID)?.label ?? "指定 Key"
    : "自动选择（按优先级）");
  /** 未配置映射时显示默认调用名，避免将唯一候选误表现为可选择列表。 */
  const selectedMappingLabel = $derived(!model.mappings.length ? `默认模型名：${options?.modelName ?? model.modelName}` : options?.mappings.length === 1 ? options.mappings[0] ?? options.modelName : requestModel || "自动选择（按优先级）");
  const configSummary = $derived(`${selectedProtocolLabel} · ${selectedKeyLabel} · ${selectedMappingLabel}`);
  const promptLabel = $derived(options?.kind === "image" || options?.kind === "video" ? "测试提示词" : "测试消息");
  const statusKind = $derived(running
    ? "testing"
    : result?.state === "success"
      ? "success"
      : result?.state === "failed"
        ? "failure"
        : result?.state === "cancelled"
          ? "stopped"
          : "idle");
  const statusLabel = $derived(running
    ? stopping ? "正在停止" : "测试中"
    : result?.state === "success"
      ? "测试成功"
      : result?.state === "failed"
        ? "测试失败"
        : result?.state === "cancelled"
          ? "已停止"
          : "待开始");
  const resultSuccessful = $derived(result?.state === "success");
  const resultCancelled = $derived(result?.state === "cancelled");
  const resultEyebrow = $derived(resultSuccessful ? "链路验证通过" : resultCancelled ? "测试已停止" : "候选均未通过");
  const resultTitle = $derived(resultSuccessful ? "模型测试成功" : resultCancelled ? "模型测试已停止" : "模型测试失败");
  const resultHTTP = $derived(result?.status ? String(result.status) : "—");
  const resultDuration = $derived(result ? `${result.durationMillis} ms` : "—");
  const panelDescription = $derived(configExpanded
    ? "选择协议、Key 与映射；开始测试后自动收起。"
    : "当前测试范围；可随时展开调整。");
  const recordDescription = $derived(running
    ? "真实请求已发出，正在等待供应商响应。"
    : result
      ? "最终结论、执行顺序与实际耗时。"
      : "一次真实请求，集中呈现最终结论与执行记录。");

  onMount(() => {
    void loadOptions();
  });
  onDestroy(() => {
    if (requestID) void VelaService.cancelProviderModelTest(requestID).catch(() => {});
  });
  /** 读取当前权限和映射候选；失败保留弹窗及本次结果，并提供重试。 */
  async function loadOptions(): Promise<void> {
    loading = true;
    error = "";
    try {
      options = await VelaService.providerModelTestOptions(providerID, model.modelId);
      keyID = options.keys.length === 1 ? options.keys[0]?.value ?? "" : "";
      requestModel = options.mappings.length === 1 ? options.mappings[0] ?? options.modelName : "";
      protocol = options.kind === "image"
        ? "image"
        : options.kind === "video"
          ? "video"
          : options.kind === "vector"
            ? "embedding"
            : "chat";
    } catch (cause) {
      options = null;
      error = errorMessage(cause);
    } finally {
      loading = false;
    }
  }
  /** 将 IPC 和校验异常保留为可操作错误，不伪造测试结论。 */
  function errorMessage(cause: unknown): string {
    return cause instanceof Error ? cause.message : String(cause);
  }
  /** 实际调用非流式诊断；开始后收起范围，只保留结果工作台。 */
  async function run(): Promise<void> {
    if (running || loading || !options || !options.keys.length) return;
    error = "";
    selectedAttempt = null;
    const request: TestRequest = {
      id: crypto.randomUUID(),
      providerId: providerID,
      modelId: model.modelId,
      keyId: keyID,
      requestModel,
      protocol,
      message,
      requestHeaders: "",
      requestBody: "",
    };
    requestID = request.id;
    configExpanded = false;
    running = true;
    try {
      result = await VelaService.runProviderModelTest(request);
      await oncompleted();
    } catch (cause) {
      error = errorMessage(cause);
    } finally {
      requestID = "";
      running = false;
      stopping = false;
    }
  }
  /** 停止等待服务端取消结果，收到终态前不允许改配置或再次运行。 */
  async function stop(): Promise<void> {
    if (!requestID || stopping) return;
    stopping = true;
    try {
      await VelaService.cancelProviderModelTest(requestID);
    } catch (cause) {
      error = errorMessage(cause);
      stopping = false;
    }
  }
  /** 表格和结果卡使用同一组服务端状态文案。 */
  function stateLabel(state: string): string {
    return state === "success"
      ? "成功"
      : state === "failed"
        ? "失败"
        : state === "unused"
          ? "未使用"
          : "已取消";
  }
  /** 展示该次尝试的执行结论，具体上游原因由下方回复正文补充。 */
  function attemptDetail(attempt: TestAttempt): string {
    if (attempt.state === "success") return "响应有效，链路可用";
    if (attempt.state === "failed") return attempt.error || "供应商未返回有效响应";
    if (attempt.state === "unused") return "前序尝试成功，未发送请求";
    return "本次请求已取消";
  }
  /** 读取当前展开的尝试，索引在结果更新后立即失效时返回空。 */
  function attemptAt(attempts: TestAttempt[], index: number): TestAttempt | null {
    return attempts[index] ?? null;
  }
  /** 优先读取逐次回复；旧记录仅可将已有的最后响应归给最后一次收到 HTTP 响应的尝试。 */
  function attemptResponse(result: TestResult, index: number): string {
    const attempt = result.attempts[index];
    if (attempt?.responseBody) return attempt.responseBody;
    if (!attempt?.status || result.attempts.slice(index + 1).some((item) => item.status > 0)) return "";
    return result.responseBody;
  }
  /** 将服务端完成时间转为稳定的本地时间文本。 */
  function formatTime(value: string): string {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? value : date.toLocaleString("zh-CN", { hour12: false });
  }
</script>

<Dialog.Root open onOpenChange={(open) => { if (!open && !running) onclose(); }}>
  <Dialog.Content
    class="provider-form-dialog provider-model-test-dialog"
    showCloseButton={!running}
    interactOutsideBehavior={running ? "ignore" : "close"}
    escapeKeydownBehavior={running ? "ignore" : "close"}
  >
    <header class="model-test-heading">
      <div class="model-test-mark"><FlaskConical /></div>
      <div class="model-test-heading-copy">
        <div class="model-test-title-row">
          <Dialog.Title class="model-test-title">模型测试</Dialog.Title>
          <span class="model-test-eyebrow">连通诊断</span>
        </div>
        <Dialog.Description class="model-test-description">
          验证 {model.name} 在真实供应商、Key 与映射组合下的可用性；测试会产生上游调用。
        </Dialog.Description>
      </div>
      <Badge variant={statusKind === "testing" ? "processing" : statusKind === "success" ? "positive" : statusKind === "failure" ? "negative" : statusKind === "stopped" ? "warning" : "neutral"} role="status" aria-live="polite">{statusLabel}</Badge>
    </header>

    <div class="model-test-body">
      <div class="model-test-workbench" aria-busy={running}>
        <aside class="model-test-panel config-panel" aria-labelledby="model-test-config-title">
          <header class="model-test-panel-heading">
            <div class="min-w-0">
              <div class="model-test-panel-eyebrow"><SlidersHorizontal />测试范围</div>
              <h3 id="model-test-config-title" class="model-test-panel-title">测试配置</h3>
              <Hint text={configExpanded ? configSummary : panelDescription}>{#snippet children(props)}<p {...props} class="model-test-panel-description">
                {configExpanded ? configSummary : panelDescription}
              </p>{/snippet}</Hint>
            </div>
            <Button
              variant="ghost"
              size="icon-sm"
              class="panel-toggle"
              aria-expanded={configExpanded}
              aria-controls="model-test-config-content"
              aria-label={configExpanded ? "收起测试配置" : "展开测试配置"}
              onclick={() => { configExpanded = !configExpanded; }}
            >
              <ChevronDown class={configExpanded ? "rotate-180" : "-rotate-90"} />
            </Button>
          </header>

          {#if loading && !options}
            <div class="model-test-panel-message" role="status">正在读取 Key 与映射…</div>
          {:else if options}
            {#if configExpanded}
              <div id="model-test-config-content" class="config-content">
                <div class="config-field">
                  <Label>请求协议</Label>
                  <Choice bind:value={protocol} disabled={running || loading} label="请求协议" options={protocols} />
                </div>
                <div class="config-field-grid">
                  <div class="config-field">
                    <Label>执行 Key</Label>
                    {#if options.keys.length > 1}
                      <Choice bind:value={keyID} disabled={running || loading} label="执行 Key" options={[{ value: "", label: "自动选择（按优先级）" }, ...options.keys]} />
                    {:else}
                      <Input aria-label="执行 Key" value={selectedKeyLabel} readonly disabled={!options.keys.length} />
                    {/if}
                    {#if !options.keys.length}<p class="text-xs text-muted-foreground">请先添加密钥，并允许它使用当前模型。</p>{/if}
                  </div>
                  <div class="config-field">
                    <Label>模型映射</Label>
                    {#if options.mappings.length > 1}
                      <Choice bind:value={requestModel} disabled={running || loading} label="模型映射" options={[{ value: "", label: "自动选择（按优先级）" }, ...options.mappings.map((name) => ({ value: name, label: name }))]} />
                    {:else}
                      <Input aria-label="模型映射" value={options.mappings[0] ?? options.modelName} readonly />
                      {#if !model.mappings.length}<p class="text-xs text-muted-foreground">默认模型名</p>{/if}
                    {/if}
                  </div>
                </div>
                <div class="config-field">
                  <Label for="model-test-message">{promptLabel}</Label>
                  <Textarea
                    variant="outline"
                    id="model-test-message"
                    rows={3}
                    bind:value={message}
                    disabled={running}
                  />
                </div>
              </div>
            {:else}
              <div class="config-summary">
                <div class="config-summary-row"><Server /><span>协议</span><strong>{selectedProtocolLabel}</strong></div>
                <div class="config-summary-row"><KeyRound /><span>Key</span><Hint text={selectedKeyLabel}>{#snippet children(props)}<strong {...props}>{selectedKeyLabel}</strong>{/snippet}</Hint></div>
                <div class="config-summary-row"><Layers3 /><span>映射</span><strong>{selectedMappingLabel}</strong></div>
                <p>{running ? "测试期间范围已锁定。" : "展开配置可调整本次测试范围。"}</p>
              </div>
            {/if}
          {:else}
            <div class="model-test-panel-message">
              <p>{error || "测试配置读取失败"}</p>
              <Button variant="outline" size="sm" onclick={() => void loadOptions()}>重新读取</Button>
            </div>
          {/if}
        </aside>

        <section class="model-test-panel record-panel" aria-labelledby="model-test-record-title">
          <header class="model-test-panel-heading record-heading">
            <div class="min-w-0">
              <div class="model-test-panel-eyebrow"><Activity />执行与结果</div>
              <h3 id="model-test-record-title" class="model-test-panel-title">测试记录</h3>
              <p class="model-test-panel-description">{recordDescription}</p>
            </div>
            {#if result && !running}
              <time class="record-time" datetime={result.completedAt}>{formatTime(result.completedAt)}</time>
            {/if}
          </header>

          <div class="record-stage">
            {#if running}
              <div class="run-card" role="status" aria-live="polite">
                <span class="run-icon"><Loader2 class="animate-spin" /></span>
                <h4>{stopping ? "正在停止测试" : "正在验证固定请求"}</h4>
                <p>{model.name} · {selectedProtocolLabel} · {selectedKeyLabel}</p>
                <div class="run-track"><span></span></div>
                <small>已向供应商发送真实请求，成功或失败后会在下方保留完整结论。</small>
              </div>
            {:else if result}
              <div class="result-hero" data-state={statusKind}>
                <span class="result-icon">
                  {#if resultSuccessful}<CheckCircle2 />{:else}<XCircle />{/if}
                </span>
                <div class="result-copy">
                  <p class="result-eyebrow">{resultEyebrow}</p>
                  <h4>{resultTitle}</h4>
                  <p class="result-target">
                    <strong>{result.keyName || "—"}</strong>
                    <span>·</span>
                    <code>{result.requestModel || model.modelName}</code>
                  </p>
                </div>
                <div class="result-metrics">
                  <div><span>HTTP</span><strong>{resultHTTP}</strong></div>
                  <div><span>耗时</span><strong>{resultDuration}</strong></div>
                </div>
                {#if result.state !== "success" && result.error}
                  <p class="result-error">{result.error}</p>
                {/if}
              </div>

              <div class="attempt-section">
                <div class="section-heading">
                  <div>
                    <h4>执行明细</h4>
                    <p>{result.attempts.length} 次候选尝试，成功后其余候选不再请求。</p>
                  </div>
                </div>
                {#if result.attempts.length}
                  <div class="attempt-table-wrap">
                    <Table.Root class="model-test-table">
                      <Table.Header>
                        <Table.Row>
                          <Table.Head class="attempt-index">#</Table.Head>
                          <Table.Head>Key</Table.Head>
                          <Table.Head>模型</Table.Head>
                          <Table.Head class="attempt-status">状态</Table.Head>
                          <Table.Head class="attempt-duration">耗时</Table.Head>
                          <Table.Head class="attempt-action"><span class="sr-only">详情</span></Table.Head>
                        </Table.Row>
                      </Table.Header>
                      <Table.Body>
                        {#each result.attempts as attempt, index (attempt.keyId + attempt.requestModel + index)}
                          <Table.Row
                            data-state={attempt.state}
                            class={selectedAttempt === index ? "is-selected" : undefined}
                          >
                            <Table.Cell class="attempt-index"><code>{String(index + 1).padStart(2, "0")}</code></Table.Cell>
                            <Hint text={attempt.keyName || "—"}>{#snippet children(props)}<Table.Cell {...props} class="attempt-key"><strong>{attempt.keyName || "—"}</strong></Table.Cell>{/snippet}</Hint>
                            <Table.Cell class="attempt-model"><Hint text={attempt.requestModel || "—"}>{#snippet children(props)}<code {...props}>{attempt.requestModel || "—"}</code>{/snippet}</Hint></Table.Cell>
                            <Table.Cell class="attempt-status">
                              <Badge variant={attempt.state === "success" ? "positive" : attempt.state === "failed" ? "negative" : "neutral"}>{stateLabel(attempt.state)}</Badge>
                            </Table.Cell>
                            <Table.Cell class="attempt-duration">{attempt.durationMillis} ms</Table.Cell>
                            <Table.Cell class="attempt-action">
                              <Button
                                variant="ghost"
                                size="icon-xs"
                                aria-label={`${selectedAttempt === index ? "收起" : "查看"}第 ${index + 1} 次尝试详情`}
                                aria-expanded={selectedAttempt === index}
                                onclick={() => { selectedAttempt = selectedAttempt === index ? null : index; }}
                              >
                                <ChevronRight class={selectedAttempt === index ? "rotate-90" : ""} />
                              </Button>
                            </Table.Cell>
                          </Table.Row>
                        {/each}
                      </Table.Body>
                    </Table.Root>
                  </div>
                {:else}
                  <Empty.Root size="compact">没有执行记录</Empty.Root>
                {/if}

                {#if selectedAttempt !== null}
                  {@const attempt = attemptAt(result.attempts, selectedAttempt)}
                  {#if attempt}
                    <div class="attempt-inspector">
                      <div class="attempt-inspector-heading">
                        <div><strong>尝试 {selectedAttempt + 1}</strong><span>{stateLabel(attempt.state)}</span></div>
                        <Button variant="ghost" size="sm" onclick={() => { selectedAttempt = null; }}>收起</Button>
                      </div>
                      <dl>
                        <div><dt>执行 Key</dt><dd>{attempt.keyName || "—"}</dd></div>
                        <div><dt>实际模型</dt><dd>{attempt.requestModel || "—"}</dd></div>
                        <div><dt>HTTP</dt><dd>{attempt.status || "—"}</dd></div>
                        <div><dt>耗时</dt><dd>{attempt.durationMillis} ms</dd></div>
                      </dl>
                      <p>{attemptDetail(attempt)}</p>
                      {#if attempt.state !== "unused"}
                        {@const response = attemptResponse(result, selectedAttempt)}
                        <section class="attempt-response" aria-label="上游回复">
                          <h5>上游回复</h5>
                          {#if response}
                            <Textarea variant="outline" readonly value={response} aria-label="上游回复正文" class="attempt-response-body" />
                          {:else}
                            <p>未收到或未保存该次尝试的上游回复。</p>
                          {/if}
                        </section>
                      {/if}
                    </div>
                  {/if}
                {/if}
              </div>
            {:else}
              <div class="idle-stage">
                <div class="idle-flow" aria-hidden="true">
                  <span><Server />协议</span><ChevronRight /><span><KeyRound />Key</span><ChevronRight /><span><Layers3 />映射</span>
                </div>
                <span class="idle-icon"><Play /></span>
                <h4>等待开始测试</h4>
                <p>开始后会按当前范围发送一次真实请求，最终状态、HTTP 与耗时都会保留在这里。</p>
                <Badge variant="metadata" class="mt-3.5 h-auto whitespace-normal text-left">自动模式按映射优先级与 Key 顺序尝试，成功后立即停止。</Badge>
              </div>
            {/if}
            {#if error}
              <Alert.Root variant="inline-error"><Alert.Description>{error}</Alert.Description></Alert.Root>
            {/if}
          </div>
        </section>
      </div>
    </div>

    <Dialog.Footer class="model-test-footer">
      <Button variant="outline" disabled={running} onclick={onclose}>关闭</Button>
      {#if running}
        <Button variant="destructive" disabled={stopping} onclick={() => void stop()}>
          <Square data-icon="inline-start" />{stopping ? "正在停止…" : "停止测试"}
        </Button>
      {:else}
        <Button disabled={loading || !options?.keys.length} onclick={() => void run()}>
          {#if result}<RotateCcw data-icon="inline-start" />{:else}<Play data-icon="inline-start" />{/if}
          {result ? "重新测试" : "开始测试"}
        </Button>
      {/if}
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<style>
  :global(.provider-form-dialog.provider-model-test-dialog[data-slot=dialog-content]) {
    --test-success: #14805a;
    --test-success-soft: color-mix(in srgb, var(--test-success) 8%, var(--background));
    --test-success-border: color-mix(in srgb, var(--test-success) 22%, var(--border));
    width: min(1080px, 94vw);
    max-width: none;
    height: min(740px, 91dvh);
    max-height: none;
    padding: 0;
    gap: 0;
    overflow: hidden;
    background: var(--background);
  }
  .model-test-heading {
    display: flex;
    flex-shrink: 0;
    align-items: center;
    gap: 14px;
    min-height: 88px;
    padding: 17px 72px 17px 24px;
    border-bottom: 1px solid var(--border);
    background:
      linear-gradient(112deg, color-mix(in srgb, var(--primary) 7.5%, transparent), transparent 52%),
      radial-gradient(circle at 1px 1px, color-mix(in srgb, var(--primary) 8%, transparent) 0.65px, transparent 0.75px),
      var(--background);
    background-size: auto, 16px 16px, auto;
  }
  .model-test-mark {
    display: grid;
    width: 44px;
    height: 44px;
    flex-shrink: 0;
    place-items: center;
    border-radius: 15px;
    background: var(--primary);
    color: var(--primary-foreground);
    box-shadow: 0 10px 24px color-mix(in srgb, var(--primary) 24%, transparent);
  }
  .model-test-mark :global(svg) { width: 20px; height: 20px; }
  .model-test-heading-copy { min-width: 0; flex: 1; }
  .model-test-title-row { display: flex; align-items: center; gap: 9px; }

  .model-test-eyebrow {
    display: inline-flex;
    align-items: center;
    min-height: 22px;
    padding: 2px 8px;
    border: 1px solid color-mix(in srgb, var(--primary) 16%, transparent);
    border-radius: 999px;
    background: color-mix(in srgb, var(--primary) 6%, var(--background));
    color: var(--primary);
    font-size: 10px;
    font-weight: 650;
  }
  :global(.model-test-description) { min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }

  .model-test-body { min-height: 0; flex: 1; overflow: auto; padding: 16px; background: #f5f8fb; scrollbar-width: none; }
  .model-test-body::-webkit-scrollbar { display: none; }
  .model-test-workbench { display: grid; grid-template-columns: 300px minmax(0, 1fr); gap: 16px; min-height: 100%; align-items: start; }
  .model-test-panel { min-width: 0; overflow: hidden; border: 1px solid var(--border); border-radius: 16px; background: var(--card); box-shadow: 0 14px 38px color-mix(in srgb, var(--foreground) 4%, transparent); }
  .config-panel { align-self: start; }
  .record-panel { display: flex; min-height: 560px; flex-direction: column; }
  .model-test-panel-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; padding: 14px 16px; border-bottom: 1px solid var(--border); background: linear-gradient(180deg, color-mix(in srgb, var(--primary) 4%, var(--background)), var(--background)); }
  .record-heading { align-items: center; }
  .model-test-panel-eyebrow { display: flex; align-items: center; gap: 5px; color: var(--primary); font-size: 10px; font-weight: 700; letter-spacing: .12em; line-height: 1.3; }
  .model-test-panel-eyebrow :global(svg) { width: 13px; height: 13px; }
  .model-test-panel-title { margin-top: 2px; font-size: 14px; font-weight: 650; letter-spacing: -.01em; line-height: 1.4; }
  .model-test-panel-description { margin-top: 2px; overflow: hidden; color: var(--muted-foreground); font-size: 10px; line-height: 1.55; text-overflow: ellipsis; white-space: nowrap; }
  :global(.panel-toggle) { flex-shrink: 0; }
  :global(.panel-toggle svg) { transition: transform .18s ease; }
  .record-time { flex-shrink: 0; color: var(--muted-foreground); font-size: 9px; font-variant-numeric: tabular-nums; }
  .config-content { display: grid; gap: 15px; padding: 16px; }
  .config-field { display: grid; min-width: 0; gap: 7px; }
  /* 允许长 Key 与映射在所属网格内收缩，避免选择器按最小内容宽度覆盖相邻字段。 */
  .config-field :global([data-slot="select-trigger"]) { min-width: 0; max-width: 100%; }
  .config-field-grid { display: grid; align-items: start; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
  .model-test-panel-message { display: grid; min-height: 170px; place-content: center; justify-items: center; gap: 10px; padding: 20px; color: var(--muted-foreground); font-size: 11px; text-align: center; }
  .config-summary { display: grid; gap: 2px; padding: 8px 16px 14px; }
  .config-summary-row { display: grid; grid-template-columns: 16px 34px minmax(0, 1fr); align-items: center; gap: 7px; min-height: 37px; border-bottom: 1px solid color-mix(in srgb, var(--border) 72%, transparent); font-size: 9.5px; }
  .config-summary-row:last-of-type { border-bottom: 0; }
  .config-summary-row :global(svg) { width: 13px; height: 13px; color: var(--primary); }
  .config-summary-row > span { color: var(--muted-foreground); }
  .config-summary-row > strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
  .config-summary > p { margin-top: 8px; color: var(--muted-foreground); font-size: 10px; line-height: 1.55; }
  .record-stage { display: flex; min-height: 0; flex: 1; flex-direction: column; padding: 16px; }
  .run-card { display: flex; min-height: 390px; flex-direction: column; align-items: center; justify-content: center; border: 1px solid color-mix(in srgb, var(--primary) 18%, var(--border)); border-radius: 14px; background: linear-gradient(135deg, color-mix(in srgb, var(--primary) 7%, var(--background)), var(--background) 58%); padding: 34px; text-align: center; }
  .run-icon { display: grid; width: 46px; height: 46px; place-items: center; border-radius: 15px; background: color-mix(in srgb, var(--primary) 11%, var(--background)); color: var(--primary); }
  .run-icon :global(svg) { width: 20px; height: 20px; }
  .run-card h4 { margin-top: 16px; font-size: 16px; font-weight: 650; letter-spacing: -.02em; }
  .run-card p { margin-top: 5px; color: var(--muted-foreground); font-size: 11px; }
  .run-track { width: min(360px, 80%); height: 5px; margin-top: 22px; overflow: hidden; border-radius: 999px; background: var(--muted); }
  .run-track span { display: block; width: 38%; height: 100%; border-radius: inherit; background: linear-gradient(90deg, var(--primary), color-mix(in srgb, var(--primary) 42%, var(--background))); animation: model-test-run 1.15s ease-in-out infinite; }
  .run-card small { max-width: 420px; margin-top: 14px; color: var(--muted-foreground); font-size: 10px; line-height: 1.6; }
  .result-hero { display: flex; flex-wrap: wrap; align-items: flex-start; gap: 14px; padding: 18px; border: 1px solid var(--border); border-radius: 15px; background: var(--background); }
  .result-hero[data-state=success] { border-color: var(--test-success-border); background: linear-gradient(135deg, color-mix(in srgb, var(--test-success) 8%, transparent), transparent 55%), var(--test-success-soft); }
  .result-hero[data-state=failure] { border-color: color-mix(in srgb, var(--destructive) 22%, var(--border)); background: linear-gradient(135deg, color-mix(in srgb, var(--destructive) 7%, transparent), transparent 55%), color-mix(in srgb, var(--destructive) 4%, var(--background)); }
  .result-hero[data-state=stopped] { border-color: color-mix(in srgb, #b45309 24%, var(--border)); background: color-mix(in srgb, #f59e0b 5%, var(--background)); }
  .result-icon { display: grid; width: 44px; height: 44px; flex-shrink: 0; place-items: center; border-radius: 14px; background: color-mix(in srgb, var(--muted-foreground) 8%, var(--background)); color: var(--muted-foreground); }
  .result-icon :global(svg) { width: 20px; height: 20px; }
  .result-hero[data-state=success] .result-icon { background: color-mix(in srgb, var(--test-success) 12%, var(--background)); color: var(--test-success); }
  .result-hero[data-state=failure] .result-icon { background: color-mix(in srgb, var(--destructive) 10%, var(--background)); color: var(--destructive); }
  .result-hero[data-state=stopped] .result-icon { background: color-mix(in srgb, #f59e0b 11%, var(--background)); color: #b45309; }
  .result-copy { min-width: 0; flex: 1; }
  .result-eyebrow { color: var(--muted-foreground); font-size: 9px; font-weight: 700; letter-spacing: .12em; }
  .result-copy h4 { margin-top: 4px; font-family: "Newsreader Variable", Georgia, serif; font-size: 28px; font-weight: 630; letter-spacing: -.03em; line-height: 1.08; }
  .result-target { display: flex; min-width: 0; flex-wrap: wrap; align-items: center; gap: 5px; margin-top: 7px; color: var(--muted-foreground); font-size: 11px; }
  .result-target strong { color: var(--foreground); font-weight: 650; }
  .result-target code { overflow-wrap: anywhere; }
  .result-metrics { display: grid; grid-template-columns: repeat(2, minmax(74px, 1fr)); gap: 7px; margin-left: auto; }
  .result-metrics > div { min-width: 74px; padding: 8px 10px; border: 1px solid color-mix(in srgb, var(--border) 64%, transparent); border-radius: 10px; background: color-mix(in srgb, var(--background) 72%, transparent); }
  .result-metrics span { display: block; color: var(--muted-foreground); font-size: 9px; font-weight: 700; letter-spacing: .06em; }
  .result-metrics strong { display: block; margin-top: 2px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 14px; font-variant-numeric: tabular-nums; }
  .result-error { width: 100%; padding-top: 10px; border-top: 1px solid color-mix(in srgb, var(--destructive) 14%, transparent); color: var(--destructive); font-size: 11px; line-height: 1.6; overflow-wrap: anywhere; }
  .attempt-section { min-height: 0; padding-top: 18px; }
  .section-heading { margin-bottom: 9px; }
  .section-heading h4 { font-size: 12px; font-weight: 650; }
  .section-heading p { margin-top: 2px; color: var(--muted-foreground); font-size: 10px; }
  .attempt-table-wrap { overflow: auto; border: 1px solid var(--border); border-radius: 12px; background: var(--background); }
  .attempt-table-wrap :global(.model-test-table) { min-width: 520px; table-layout: fixed; font-size: 11px; }
  .attempt-table-wrap :global(th) { height: 35px; padding: 8px 10px; background: var(--muted); font-size: 9px; letter-spacing: .06em; }
  .attempt-table-wrap :global(td) { height: 46px; padding: 8px 10px; border-top: 1px solid color-mix(in srgb, var(--border) 70%, transparent); white-space: normal; }
  .attempt-table-wrap :global(tr[data-state=success]) { box-shadow: inset 2px 0 0 color-mix(in srgb, var(--test-success) 48%, transparent); }
  .attempt-table-wrap :global(tr[data-state=failed]) { box-shadow: inset 2px 0 0 color-mix(in srgb, var(--destructive) 40%, transparent); }
  .attempt-table-wrap :global(tr.is-selected) { background: color-mix(in srgb, var(--primary) 5%, var(--background)); }
  .attempt-table-wrap :global(th:nth-child(1)) { width: 44px; }
  .attempt-table-wrap :global(th:nth-child(2)) { width: 19%; }
  .attempt-table-wrap :global(th:nth-child(3)) { width: 31%; }
  .attempt-table-wrap :global(th:nth-child(4)) { width: 74px; }
  .attempt-table-wrap :global(th:nth-child(5)) { width: 76px; }
  .attempt-table-wrap :global(th:nth-child(6)) { width: 42px; }
  .attempt-table-wrap :global(.attempt-index) { color: var(--muted-foreground); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  .attempt-table-wrap :global(.attempt-key) { overflow: hidden; text-overflow: ellipsis; white-space: nowrap !important; }
  .attempt-table-wrap :global(.attempt-model) { min-width: 0; }
  .attempt-table-wrap :global(.attempt-model code) { display: block; overflow: hidden; color: var(--muted-foreground); text-overflow: ellipsis; white-space: nowrap; }
  .attempt-table-wrap :global(.attempt-duration) { color: var(--primary); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-variant-numeric: tabular-nums; }
  .attempt-inspector { margin-top: 11px; padding: 13px 14px; border: 1px solid var(--border); border-radius: 12px; background: var(--muted); }
  .attempt-inspector-heading { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
  .attempt-inspector-heading > div { display: flex; align-items: center; gap: 8px; }
  .attempt-inspector-heading strong { font-size: 11px; }
  .attempt-inspector-heading span { color: var(--muted-foreground); font-size: 10px; }
  .attempt-inspector dl { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px 18px; margin-top: 12px; }
  .attempt-inspector dt { color: var(--muted-foreground); font-size: 9px; }
  .attempt-inspector dd { margin-top: 2px; overflow-wrap: anywhere; font-size: 11px; font-weight: 600; }
  .attempt-inspector > p { margin-top: 12px; padding-top: 10px; border-top: 1px solid var(--border); color: var(--muted-foreground); font-size: 10px; line-height: 1.6; overflow-wrap: anywhere; }
  .attempt-response { min-width: 0; margin-top: 12px; }
  .attempt-response h5 { margin-bottom: 6px; font-size: 11px; font-weight: 600; }
  .attempt-response :global(.attempt-response-body) { min-height: 96px; max-height: 240px; overflow: auto; resize: none; }
  .attempt-response p { color: var(--muted-foreground); font-size: 10px; }
  .attempt-empty { padding: 28px 16px; border: 1px dashed var(--border); border-radius: 12px; color: var(--muted-foreground); font-size: 11px; text-align: center; }
  .idle-stage { display: flex; min-height: 390px; flex: 1; flex-direction: column; align-items: center; justify-content: center; border: 1px dashed var(--border); border-radius: 14px; background: radial-gradient(circle at 50% 35%, color-mix(in srgb, var(--primary) 7%, transparent), transparent 42%), radial-gradient(circle at 1px 1px, color-mix(in srgb, var(--primary) 8%, transparent) .7px, transparent .8px); background-size: auto, 16px 16px; padding: 32px 20px; text-align: center; }
  .idle-flow { display: flex; align-items: center; gap: 4px; margin-bottom: 22px; }
  .idle-flow > span { display: flex; width: 68px; height: 58px; flex-direction: column; align-items: center; justify-content: center; gap: 5px; border: 1px solid var(--border); border-radius: 12px; background: color-mix(in srgb, var(--background) 90%, transparent); color: var(--primary); font-size: 10px; font-weight: 600; box-shadow: 0 8px 20px color-mix(in srgb, var(--foreground) 4%, transparent); }
  .idle-flow :global(svg) { width: 15px; height: 15px; }
  .idle-flow > :global(svg) { width: 14px; height: 14px; color: color-mix(in srgb, var(--primary) 42%, var(--muted-foreground)); }
  .idle-icon { display: grid; width: 40px; height: 40px; place-items: center; border-radius: 999px; background: color-mix(in srgb, var(--primary) 9%, var(--background)); color: var(--primary); }
  .idle-icon :global(svg) { width: 17px; height: 17px; }
  .idle-stage h4 { margin-top: 12px; font-size: 15px; font-weight: 650; }
  .idle-stage > p { max-width: 400px; margin-top: 5px; color: var(--muted-foreground); font-size: 10px; line-height: 1.7; }


  :global(.provider-model-test-dialog [data-slot=dialog-footer]) { flex-shrink: 0; padding: 14px 24px; border-top: 1px solid var(--border); background: var(--card); }
  :global(.model-test-footer) { justify-content: flex-end; }


  @keyframes model-test-run {
    from { transform: translateX(-110%); }
    to { transform: translateX(280%); }
  }
  @media (prefers-color-scheme: dark) {
    :global(.provider-form-dialog.provider-model-test-dialog[data-slot=dialog-content]) {
      --test-success: #7fe0ba;
      --test-success-soft: color-mix(in srgb, var(--test-success) 9%, var(--background));
      --test-success-border: color-mix(in srgb, var(--test-success) 25%, var(--border));
    }
  }
  @media (max-width: 920px) {
    .model-test-workbench { grid-template-columns: minmax(0, 1fr); }
    .record-panel { min-height: 440px; }
  }
  @media (max-width: 640px) {
    .model-test-heading { flex-wrap: wrap; padding: 16px 60px 16px 18px; }

    .model-test-body { padding: 10px; }
    .model-test-workbench { gap: 10px; }
    .config-field-grid { grid-template-columns: minmax(0, 1fr); }
    .record-stage { padding: 12px; }
    .result-metrics { width: 100%; margin-left: 0; }
    .result-metrics > div { flex: 1; }
    .idle-stage { min-height: 330px; }
    .idle-flow > span { width: 60px; height: 54px; }
    .attempt-inspector dl { grid-template-columns: minmax(0, 1fr); }
  }
  @media (prefers-reduced-motion: reduce) {
    .run-track span { animation: none !important; }
    :global(.panel-toggle svg) { transition: none; }
  }
</style>
