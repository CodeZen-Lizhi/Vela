<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { Activity, Play, Square } from "@lucide/svelte";
  import { Button } from "$lib/components/ui/button";
  import { Label } from "$lib/components/ui/label";
  import { Textarea } from "$lib/components/ui/textarea";
  import * as Dialog from "$lib/components/ui/dialog";
  import * as Collapsible from "$lib/components/ui/collapsible";
  import { VelaService, type ProviderModel } from "$lib/generated/mygo";
  import Choice from "./Choice.svelte";

  /** 诊断输入与结果直接复用绑定契约，避免维护重复协议定义。 */
  type TestRequest = Parameters<typeof VelaService.runProviderModelTest>[0];
  type TestResult = Awaited<ReturnType<typeof VelaService.runProviderModelTest>>;
  type TestOptions = Awaited<ReturnType<typeof VelaService.providerModelTestOptions>>;
  /** 当前模型固定在调用行；完成后刷新父级最近结果，关闭不清除持久化记录。 */
  let { providerID, model, previous = null, onclose, oncompleted }: {
    providerID: string; model: ProviderModel; previous?: TestResult | null;
    onclose: () => void; oncompleted: () => Promise<void>;
  } = $props();
  /** 配置在执行期间锁定；每次运行使用独立 ID，停止只取消当前请求。 */
  let options = $state<TestOptions | null>(null), loading = $state(true);
  let keyID = $state(""), protocol = $state("chat"), requestModel = $state("");
  let message = $state("请简短回复 OK。"), headers = $state(""), body = $state("");
  let running = $state(false), stopping = $state(false), requestID = $state("");
  let error = $state(""), advanced = $state(false);
  let result = $state<TestResult | null>(null);
  /** 仅按统一模型 kind 开放对应真实协议，不从名称猜测能力。 */
  const protocols = $derived(options?.kind === "image" ? [{value:"image",label:"Images"}]
    : options?.kind === "video" ? [{value:"video",label:"Videos"}]
    : options?.kind === "vector" ? [{value:"embedding",label:"Embeddings"},{value:"rerank",label:"Rerank"}]
    : [{value:"chat",label:"OpenAI Chat Completions"},{value:"responses",label:"OpenAI Responses"},{value:"anthropic",label:"Anthropic Messages"},{value:"gemini",label:"Gemini GenerateContent"}]);

  onMount(() => { result = previous; void loadOptions(); });
  onDestroy(() => { if (requestID) void VelaService.cancelProviderModelTest(requestID).catch(() => {}); });
  /** 读取当前权限和映射候选；失败保留弹窗及上次结果，并提供重试。 */
  async function loadOptions(): Promise<void> {
    loading = true; error = "";
    try {
      options = await VelaService.providerModelTestOptions(providerID, model.modelId);
      protocol = options.kind === "image" ? "image" : options.kind === "video" ? "video" : options.kind === "vector" ? "embedding" : "chat";
    } catch (cause) { options = null; error = errorMessage(cause); }
    finally { loading = false; }
  }
  /** 协议切换只改变请求格式，保留当前模型映射与 Key 选择。 */
  function changeProtocol(value: string): void {
    protocol = value;
  }
  /** 将 IPC 和校验异常保留为可操作错误，不伪造测试结论。 */
  function errorMessage(cause: unknown): string { return cause instanceof Error ? cause.message : String(cause); }
  /** 高级输入允许留空使用默认正文，非空时只接受 JSON 对象。 */
  function validateJSON(text: string, label: string): void {
    if (!text.trim()) return;
    let value: unknown;
    try { value = JSON.parse(text); } catch { throw new Error(`${label}必须是有效 JSON 对象。`); }
    if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`${label}必须是 JSON 对象。`);
  }
  /** 实际调用非流式诊断；结果与配置分离，重新运行期间仍保留上次结果。 */
  async function run(): Promise<void> {
    if (running || loading || !options || !options.keys.length) return;
    error = "";
    try { validateJSON(headers, "请求头"); validateJSON(body, "请求体"); }
    catch (cause) { advanced = true; error = errorMessage(cause); return; }
    const request: TestRequest = {id:crypto.randomUUID(),providerId:providerID,modelId:model.modelId,keyId:keyID,requestModel,protocol,message,requestHeaders:headers,requestBody:body};
    requestID = request.id; running = true;
    try {
      result = await VelaService.runProviderModelTest(request);
      await oncompleted();
    } catch (cause) { error = errorMessage(cause); }
    finally { requestID = ""; running = false; stopping = false; }
  }
  /** 停止等待服务端取消结果，收到终态前不允许改配置或再次运行。 */
  async function stop(): Promise<void> {
    if (!requestID || stopping) return;
    stopping = true;
    try { await VelaService.cancelProviderModelTest(requestID); }
    catch (cause) { error = errorMessage(cause); stopping = false; }
  }
  /** 结果使用服务端完成时间与终态，取消不会显示为测试失败或成功。 */
  function stateLabel(state: string): string { return state === "success" ? "成功" : state === "failed" ? "失败" : state === "unused" ? "未使用" : "已取消"; }
</script>

<Dialog.Root open onOpenChange={(open) => { if (!open && !running) onclose(); }}>
  <Dialog.Content class="provider-form-dialog provider-model-test-dialog" style="width:min(1040px,88vw)" showCloseButton={!running} interactOutsideBehavior={running ? "ignore" : "close"} escapeKeydownBehavior={running ? "ignore" : "close"}>
    <Dialog.Header>
      <Dialog.Title>模型连通测试</Dialog.Title>
      <Dialog.Description>{model.name} · {model.modelName}；使用当前供应商真实请求，测试可能产生上游费用。</Dialog.Description>
    </Dialog.Header>
    <div class="model-test-layout" aria-busy={running}>
      <section class="model-test-config" aria-label="测试配置">
        <h3 class="text-xs font-semibold">测试配置</h3>
        {#if loading && !options}<p class="text-xs text-muted-foreground" role="status">正在读取 Key 与映射…</p>
        {:else if options}
          <div class="model-test-field"><Label>执行 Key</Label><Choice bind:value={keyID} disabled={running || loading} label="执行 Key" options={[{value:"",label:"自动（按 Key 顺序尝试）"},...options.keys]} /></div>
          <div class="model-test-field"><Label>请求协议</Label><Choice bind:value={() => protocol, changeProtocol} disabled={running || loading} label="请求协议" options={protocols} /></div>
          <div class="model-test-field"><Label>模型映射</Label><Choice bind:value={requestModel} disabled={running || loading} label="模型映射" options={[{value:"",label:"自动（按映射优先级尝试）"},...options.mappings.map((name) => ({value:name,label:name}))]} /></div>
          <div class="model-test-field"><Label for="model-test-message">{options.kind === "image" || options.kind === "video" ? "测试提示词" : "测试消息"}</Label><Textarea id="model-test-message" rows={3} bind:value={message} disabled={running} /></div>
          <Collapsible.Root bind:open={advanced}>
            <Collapsible.Trigger class="provider-advanced" type="button">高级请求设置</Collapsible.Trigger>
            <Collapsible.Content class="model-test-advanced">
              <div class="model-test-field"><Label for="model-test-headers">请求头（JSON）</Label><Textarea id="model-test-headers" rows={3} bind:value={headers} disabled={running} placeholder={'{"X-Custom-Header":"value"}'} /></div>
              <div class="model-test-field"><Label for="model-test-body">请求体（JSON）</Label><Textarea id="model-test-body" rows={5} bind:value={body} disabled={running} placeholder="留空使用当前协议的默认非流式请求" /></div>
              <p class="text-xs text-muted-foreground">填写请求体将覆盖默认正文；认证信息由当前 Key 提供。</p>
            </Collapsible.Content>
          </Collapsible.Root>
          {#if !options.keys.length}<p class="provider-error" role="alert">没有具备该模型权限且已设置密钥的 Key，请先在密钥管理中配置。</p>{/if}
        {:else}<Button variant="outline" size="sm" onclick={() => loadOptions()}>重新读取配置</Button>{/if}
      </section>
      <section class="model-test-result" aria-label="测试结果">
        <div class="flex items-center justify-between gap-2"><h3 class="text-xs font-semibold">测试结果</h3><span class="text-xs text-muted-foreground" role="status">{running ? stopping ? "正在停止…" : "正在测试…" : result ? "最近一次测试" : "尚未测试"}</span></div>
        {#if result}
          <div class="flex flex-wrap items-center gap-2 text-xs"><strong class={result.state === "failed" ? "text-destructive" : result.state === "success" ? "text-primary" : "text-muted-foreground"}>{stateLabel(result.state)}</strong><time datetime={result.completedAt}>{new Date(result.completedAt).toLocaleString("zh-CN",{hour12:false})}</time>{#if result.status}<span>HTTP {result.status}</span>{/if}</div>
          <dl class="model-test-metrics">
            <div><dt>总耗时</dt><dd>{result.durationMillis} ms</dd></div><div><dt>首响应</dt><dd>{result.status ? `${result.firstByteMillis} ms` : "—"}</dd></div>
            <div><dt>实际模型</dt><dd>{result.requestModel || "—"}</dd></div><div><dt>实际 Key</dt><dd>{result.keyName || "—"}</dd></div><div><dt>请求协议</dt><dd>{result.protocol}</dd></div>
          </dl>
          {#if result.error}<p class="provider-error" role="alert">{result.error}</p>{/if}
          <div class="model-test-field"><Label for="model-test-response">响应正文</Label><Textarea id="model-test-response" readonly rows={9} value={result.responseBody || "未收到响应正文"} class="font-mono text-xs" /></div>
          <Collapsible.Root><Collapsible.Trigger class="provider-advanced" type="button">尝试详情（{result.attempts.length}）</Collapsible.Trigger><Collapsible.Content>
            {#each result.attempts as attempt, index}<div class="model-test-attempt"><div class="flex flex-wrap justify-between gap-2"><strong>{index + 1}. {attempt.requestModel}</strong><span>{stateLabel(attempt.state)}</span></div><p>{attempt.keyName} · {attempt.durationMillis} ms{attempt.status ? ` · HTTP ${attempt.status}` : ""}</p>{#if attempt.error}<p class="text-destructive">{attempt.error}</p>{/if}</div>{/each}
          </Collapsible.Content></Collapsible.Root>
          <Collapsible.Root><Collapsible.Trigger class="provider-advanced" type="button">实际请求快照</Collapsible.Trigger><Collapsible.Content class="model-test-advanced"><div class="model-test-field"><Label for="model-test-snapshot-headers">请求头</Label><Textarea id="model-test-snapshot-headers" readonly rows={3} value={result.requestHeaders} class="font-mono text-xs" /></div><div class="model-test-field"><Label for="model-test-snapshot-body">请求体</Label><Textarea id="model-test-snapshot-body" readonly rows={5} value={result.requestBody} class="font-mono text-xs" /></div></Collapsible.Content></Collapsible.Root>
        {:else}<div class="model-test-empty"><Activity class="size-5" /><p>{running ? "正在等待上游响应，可随时停止。" : "选择配置并开始测试，结果将在这里展示。"}</p></div>{/if}
      </section>
    </div>
    {#if error}<p class="provider-error" role="alert">{error}</p>{/if}
    <Dialog.Footer><Button variant="outline" disabled={running} onclick={onclose}>关闭</Button>{#if running}<Button variant="destructive" disabled={stopping} onclick={stop}><Square data-icon="inline-start" />{stopping ? "正在停止…" : "停止测试"}</Button>{:else}<Button disabled={loading || !options?.keys.length} onclick={run}><Play data-icon="inline-start" />{result ? "重新测试" : "开始测试"}</Button>{/if}</Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<style>
  .model-test-layout { display:grid; grid-template-columns:minmax(230px,0.85fr) minmax(0,1.15fr); gap:24px; overflow:auto; min-height:0; }
  .model-test-config,.model-test-result { display:flex; flex-direction:column; gap:16px; min-width:0; }
  .model-test-result { border-left:1px solid var(--border); padding-left:24px; }
  .model-test-field,:global(.provider-model-test-dialog .model-test-advanced) { display:flex; flex-direction:column; gap:8px; }
  :global(.provider-model-test-dialog .model-test-advanced) { padding-top:12px; }
  .model-test-metrics { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:12px; font-size:12px; }
  .model-test-metrics dt { color:var(--muted-foreground); }
  .model-test-metrics dd { margin-top:4px; overflow-wrap:anywhere; }
  .model-test-empty { display:flex; flex:1; min-height:220px; flex-direction:column; justify-content:center; align-items:center; gap:12px; color:var(--muted-foreground); text-align:center; font-size:12px; }
  .model-test-attempt { border-bottom:1px solid var(--border); padding:12px 0; font-size:12px; overflow-wrap:anywhere; }
  .model-test-attempt p { margin-top:4px; color:var(--muted-foreground); }
</style>
