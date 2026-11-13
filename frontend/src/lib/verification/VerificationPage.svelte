<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import * as Empty from "$lib/components/ui/empty";
  import { Progress } from "$lib/components/ui/progress";
  import { mergeProps } from "bits-ui";
  import { Hint } from "$lib/components/ui/tooltip";
  import { onMount } from "svelte";
  import { isMyGo } from "mygo-runtime";
  import {
    FlaskConical,
    Plus,
    Search,
    Play,
    Eye,
    KeyRound,
  } from "@lucide/svelte";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import * as Tabs from "$lib/components/ui/tabs";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import { Switch } from "$lib/components/ui/switch";
  import * as InputGroup from "$lib/components/ui/input-group";
  import * as Dialog from "$lib/components/ui/dialog";
  import * as Table from "$lib/components/ui/table";
  import Choice from "$lib/providers/Choice.svelte";
  import {
    VelaService,
    events,
    type VerificationSnapshot,
    type VerificationPlan,
    type VerificationReport,
    type ProviderOverview,
    type ReferenceModel,
  } from "$lib/generated/mygo";
  import { errorMessage } from "$lib/settings/sections";
  import PlanEditor from "./PlanEditor.svelte";
  import Report from "./Report.svelte";
  import VerificationActivity from "./VerificationActivity.svelte";
  import {
    conclusions,
    effort,
    probability,
    sameLine,
    time,
    unavailable,
  } from "./display";
  import "./verification.css";
  /** 全局验证页只读本地快照；所有采样均由用户明确启动或后端计划触发。 */
  let {
    notify,
  }: { /** 已确认保存后的轻量通知。 */ notify: (message: string) => void } =
    $props();
  /** 桌面能力、最新有效快照和供应商列表；未读取不能冒充空结果。 */
  const desktop = isMyGo();
  let snapshot = $state<VerificationSnapshot | null>(null),
    providers = $state<ProviderOverview[]>([]);
  /** 按需获取参考目录，普通页面不加载检测器。 */
  let referenceModels = $state<ReferenceModel[]>([]);
  /** 页面筛选与弹窗身份；草稿通过 keyed editor 独立保存。 */
  let tab = $state("results"),
    providerId = $state(""),
    status = $state("all"),
    query = $state("");
  let editor = $state(false),
    editing = $state<VerificationPlan | null>(null),
    reportID = $state("");
  /** 本地请求状态；轮询不重入，离页后的结果不再落入页面状态。 */
  let busy = $state(false),
    error = $state(""),
    formError = $state(""),
    loading = $state(desktop);
  let reading = false,
    disposed = false;
  /** 完成事件与读取并发时，读取结束后必须再取一次最终报告。 */
  let reread = false;
  /** 事件序号防止较早发出的本地读取覆盖较新的推送进度。 */
  let progressRevision = 0;
  /** 可选供应商来自现有本地供应商快照，包含尚无模型绑定的供应商。 */
  const providerOptions = $derived(
    providers.map((row) => ({
      value: row.provider.id,
      label: row.provider.name,
    })),
  );
  const scopedPlans = $derived(
    (snapshot?.plans ?? []).filter(
      (plan) => !providerId || plan.providerId === providerId,
    ),
  );
  const scopedReports = $derived(
    (snapshot?.reports ?? []).filter(
      (report) => !providerId || report.target.providerId === providerId,
    ),
  );
  const selectedReport = $derived(
    snapshot?.reports.find((report) => report.id === reportID),
  );
  const running = $derived(snapshot?.progress.running ?? false);
  /** 结论检索包含中文显示名与思考强度；差异优先，再按时间倒序。 */
  const visible = $derived(
    scopedReports
      .filter((report) => {
        const target = report.target,
          words = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
        const text =
          `${target.providerName} ${target.modelName} ${target.modelId} ${target.requestModel} ${target.keyName} ${effort(target.effort)} ${conclusions[report.conclusion] ?? ""}`.toLowerCase();
        return (
          (status === "all" || status === report.conclusion) &&
          words.every((word) => text.includes(word))
        );
      })
      .sort(
        (a, b) =>
          Number(b.conclusion === "difference") -
            Number(a.conclusion === "difference") || b.time - a.time,
      ),
  );
  /** 只读 verification 不访问上游；失败时保留有效快照并提示。 */
  async function load(): Promise<void> {
    if (!desktop || disposed) return;
    if (reading) {
      reread = true;
      return;
    }
    reading = true;
    const revision = progressRevision;
    try {
      const next = await VelaService.verification();
      if (!disposed) {
        if (revision !== progressRevision && snapshot)
          next.progress = snapshot.progress;
        snapshot = next;
        error = "";
      }
    } catch (cause) {
      if (!disposed) error = errorMessage(cause);
    } finally {
      reading = false;
      if (!disposed) loading = false;
      if (reread && !disposed) {
        reread = false;
        void load();
      }
    }
  }
  onMount(() => {
    disposed = false;
    if (desktop) {
      void load();
      void VelaService.providers()
        .then((value) => {
          if (!disposed) providers = value.providers;
        })
        .catch((cause) => {
          if (!disposed) error = errorMessage(cause);
        });
    }
    const refresh = () => {
      if (document.visibilityState === "visible") void load();
    };
    const off = desktop
      ? events.verificationProgress.on((progress) => {
          if (disposed) return;
          progressRevision++;
          if (snapshot) snapshot.progress = progress;
          if (!progress.running) void load();
        })
      : undefined;
    const timer = setInterval(refresh, 10000);
    document.addEventListener("visibilitychange", refresh);
    return () => {
      disposed = true;
      off?.();
      clearInterval(timer);
      document.removeEventListener("visibilitychange", refresh);
    };
  });
  /** 按需读取内置参考目录再打开独立草稿；读取失败保留当前页面。 */
  async function openPlan(plan?: VerificationPlan): Promise<void> {
    busy = true;
    try {
      if (!referenceModels.length)
        referenceModels = await VelaService.verificationReferenceModels();
    } catch (cause) {
      error = errorMessage(cause);
      return;
    } finally {
      busy = false;
    }
    tab = "plans";
    editing =
      plan ??
      snapshot?.plans.find((item) => item.providerId === providerId) ??
      null;
    formError = "";
    editor = true;
  }
  /** 保存成功后重读本地计划，失败保留编辑草稿。 */
  async function save(plan: VerificationPlan): Promise<void> {
    busy = true;
    formError = "";
    try {
      await VelaService.saveVerificationPlan(plan);
      editor = false;
      await load();
      notify("检测计划已保存");
    } catch (cause) {
      formError = errorMessage(cause);
    } finally {
      busy = false;
    }
  }
  /** 自动运行仅改启停，不触发即时检测；失败时仍展示最后确认值。 */
  async function toggle(
    plan: VerificationPlan,
    enabled: boolean,
  ): Promise<void> {
    busy = true;
    try {
      await VelaService.setVerificationPlanEnabled(plan.providerId, enabled);
      await load();
    } catch (cause) {
      error = errorMessage(cause);
    } finally {
      busy = false;
    }
  }
  /** 显式启动整计划或固定报告组合，不把接受任务显示为检测成功。 */
  async function start(provider: string, report = ""): Promise<void> {
    busy = true;
    error = "";
    try {
      await VelaService.startVerification(provider, report);
      await load();
    } catch (cause) {
      error = errorMessage(cause);
    } finally {
      busy = false;
    }
  }
  /** 停止当前任务后重读状态，已发出请求的费用不会被撤销。 */
  async function stop(): Promise<void> {
    busy = true;
    try {
      await VelaService.stopVerification();
      await load();
    } catch (cause) {
      error = errorMessage(cause);
    } finally {
      busy = false;
    }
  }
  /** 当前计划有效线路与已完成可用结果构成覆盖数；失败或取消不计成功覆盖。 */
  function coverage(plan: VerificationPlan): { total: number; done: number } {
    const valid = plan.lines.filter((line) =>
      snapshot?.targets.some(
        (target) =>
          target.providerId === plan.providerId &&
          target.modelId === line.modelId &&
          target.keyId === line.keyId &&
          !target.reason,
      ),
    );
    return {
      total: valid.length,
      done: valid.filter((line) =>
        snapshot?.reports.some(
          (report) =>
            report.target.providerId === plan.providerId &&
            sameLine(line, report.target) &&
            report.state === "complete" &&
            report.conclusion !== "insufficient" &&
            !unavailable(report, snapshot?.targets ?? []),
        ),
      ).length,
    };
  }
  /** 模型摘要仅显示两组，不在方卡中追加滚动区域。 */
  function summaries(
    plan: VerificationPlan,
  ): { model: string; keys: string; effort: string }[] {
    return [...new Set(plan.lines.map((line) => line.modelId))]
      .slice(0, 2)
      .map((model) => ({
        model:
          snapshot?.targets.find(
            (target) =>
              target.providerId === plan.providerId && target.modelId === model,
          )?.modelName || model,
        keys: plan.lines
          .filter((line) => line.modelId === model)
          .map(
            (line) =>
              snapshot?.targets.find(
                (target) =>
                  target.providerId === plan.providerId &&
                  target.keyId === line.keyId,
              )?.keyName || line.keyId,
          )
          .join("、"),
        effort: effort(
          plan.lines.find((line) => line.modelId === model)?.effort ?? "auto",
        ),
      }));
  }
</script>

<section class="v-page list-page">
  <PageHeader title="模型验证" description="按供应商、模型、Key 与思考强度追踪固定线路的覆盖和差异。">
    {#snippet actions()}
      <Button
          size="sm"
          disabled={!desktop || !snapshot || !providers.length}
          onclick={() => openPlan()}
          ><Plus data-icon="inline-start" />{providerId &&
          snapshot?.plans.some((plan) => plan.providerId === providerId)
            ? "编辑当前计划"
            : "创建检测计划"}</Button
        >
    {/snippet}
  </PageHeader>
  {#if !desktop}<Alert.Root variant="info"><Alert.Description>
      浏览器预览 · 计划、检测及报告请在 Vela
      桌面窗口中使用；此处不生成演示结果。
    </Alert.Description></Alert.Root>{/if}
  {#if error}<Alert.Root variant="inline-error"><Alert.Description>
      {error}<Button variant="ghost" size="sm" onclick={load}>重新读取</Button>
    </Alert.Description></Alert.Root>{/if}
  <Tabs.Root bind:value={tab} class="v-surface list-surface" aria-label="模型验证工作台">
    <div class="v-toolbar">
        <Tabs.List aria-label="模型验证视图">
          <Tabs.Trigger value="results">验证结果 <span>{scopedReports.length}</span></Tabs.Trigger>
          <Tabs.Trigger value="plans">检测计划 <span>{scopedPlans.length}</span></Tabs.Trigger>
        </Tabs.List>
      <div class="v-filters">
        <Choice
          bind:value={providerId}
          label="验证供应商"
          options={[{ value: "", label: "全部供应商" }, ...providerOptions]}
        />
      </div>
      {#if tab === "results"}<div class="v-result-filters">
          <InputGroup.Root class="v-search"
            ><InputGroup.Input
              bind:value={query}
              aria-label="搜索验证结果"
              placeholder="搜索模型、Key、供应商或思考强度"
            /><InputGroup.Addon><Search /></InputGroup.Addon></InputGroup.Root
          ><Choice
            bind:value={status}
            label="验证结论"
            options={[
              { value: "all", label: "全部状态" },
              ...Object.entries(conclusions).map(([value, label]) => ({
                value,
                label,
              })),
            ]}
          />
        </div>{/if}
    </div>
    {#if running && snapshot}
      <VerificationActivity progress={snapshot.progress} {busy} onstop={stop} />
    {/if}
    {#if snapshot?.progress.error}<Alert.Root variant="inline-error"><Alert.Description>
        任务结束：{snapshot.progress.error}
      </Alert.Description></Alert.Root>{/if}
    <Tabs.Content value={tab} class="list-scroll" aria-label={tab === "results" ? "验证结果列表内容" : "检测计划列表内容"}>
    {#if loading}<Empty.Root role="status">
        正在读取本地验证数据…
      </Empty.Root>{:else if desktop && !snapshot}<Empty.Root>
        验证数据尚未读取成功，请重试。
      </Empty.Root>{:else if tab === "results"}
      <Table.Root class="v-table"
        ><Table.Header
          ><Table.Row
            >{#each ["供应商", "执行模型", "执行 Key", "结论", "候选置信度", "最近检测", "操作"] as title}<Table.Head data-align={title==="操作"?"control":title==="候选置信度"?"number":undefined}
                >{title}</Table.Head
              >{/each}</Table.Row
          ></Table.Header
        ><Table.Body
          >{#each visible as report (report.id)}{@const reason = unavailable(
              report,
              snapshot?.targets ?? [],
            )}<Table.Row
              ><Table.Cell
                ><div class="v-identity">
                  <span class="v-avatar"
                    >{report.target.providerName.slice(0, 1)}</span
                  ><Hint text={report.target.providerName}>{#snippet children(hintProps0)}<strong {...hintProps0} >{report.target.providerName}</strong>{/snippet}</Hint>
                </div></Table.Cell
              ><Table.Cell
                ><strong>{report.target.modelName}</strong><small
                  >{report.target.requestModel} · 思考{effort(
                    report.target.effort,
                  )}</small
                ></Table.Cell
              ><Table.Cell
                ><div class="v-identity">
                  <KeyRound class="size-3" /><Hint text={report.target.keyName}>{#snippet children(hintProps1)}<span {...hintProps1} class="v-key-name" >{report.target.keyName}</span>{/snippet}</Hint>
                </div>
                {#if reason}<small class="text-destructive">{reason}</small
                  >{/if}</Table.Cell
              ><Table.Cell
                ><Badge
                  variant={report.conclusion === "difference"
                    ? "destructive"
                    : "secondary"}
                  >{conclusions[report.conclusion] ??
                    conclusions.insufficient}</Badge
                >{#if report.state !== "complete"}<small
                    >{report.state === "cancelled" ? "已取消" : "执行失败"} · {report.reason}</small
                  >{:else if report.conclusion === "insufficient"}<small>{report.reason}</small>{/if}</Table.Cell
              ><Table.Cell data-align="number"
                ><strong
                  >{probability(report.candidates[0]?.probability)}</strong
                ><small
                  >{report.candidates[0]?.name ||
                    report.candidates[0]?.model ||
                    "无有效候选"} · 参考库内</small
                ></Table.Cell
              ><Table.Cell>{time(report.time)}</Table.Cell><Table.Cell data-align="control"
                ><div class="v-actions">
                  <Hint text={reason || "固定目标复测会产生上游消耗"}>{#snippet children(hintProps2)}<Button {...mergeProps(hintProps2, { onclick: () => start(report.target.providerId, report.id) })}
                    variant="ghost"
                    size="icon-xs"
                    aria-label={`复测 ${report.target.modelName}`}

                    disabled={!desktop || running || busy || !!reason}

                    ><Play /></Button
                  >{/snippet}</Hint><Hint text="验证详情">{#snippet children(hintProps3)}<Button {...mergeProps(hintProps3, { onclick: () => (reportID = report.id) })}
                    variant="ghost"
                    size="icon-xs"
                    aria-label={`查看 ${report.target.modelName} 验证详情`}

                    ><Eye /></Button
                  >{/snippet}</Hint>
                </div></Table.Cell
              ></Table.Row
            >{/each}</Table.Body
        ></Table.Root
      >
      {#if !visible.length}<Empty.Root>
          <FlaskConical class="size-6" /><Empty.Title
            >{scopedReports.length
              ? "没有匹配的验证结果"
              : "还没有对应的验证结果"}</Empty.Title
          >
          <Empty.Description>创建计划并执行一轮，结果会按固定组合独立记录。</Empty.Description>
          {#if query || status !== "all" || providerId}<Button
              variant="outline"
              onclick={() => {
                query = "";
                status = "all";
                providerId = "";
              }}>清空筛选</Button
            >{:else}<Button variant="outline" onclick={() => (tab = "plans")}
              >查看检测计划</Button
            >{/if}
        </Empty.Root>{/if}
    {:else}<div class="v-plans">
        {#each scopedPlans as plan (plan.providerId)}{@const covered =
            coverage(plan)}{@const name =
            providerOptions.find((item) => item.value === plan.providerId)
              ?.label ||
            snapshot?.targets.find(
              (target) => target.providerId === plan.providerId,
            )?.providerName ||
            plan.providerId}{@const modelCount = new Set(
            plan.lines.map((line) => line.modelId),
          ).size}
          <article class="v-plan">
            <header>
              <div class="v-identity">
                <span class="v-avatar">{name.slice(0, 1)}</span>
                <div>
                  <Hint text={name}>{#snippet children(hintProps4)}<strong {...hintProps4} >{name}</strong>{/snippet}</Hint><small
                    >{plan.enabled
                      ? `自动运行 · 每 ${plan.hours} 小时`
                      : "仅手动检测"}</small
                  >
                </div>
              </div>
              <Switch
                checked={plan.enabled}
                disabled={busy || !desktop}
                aria-label={`${name} 自动运行`}
                onCheckedChange={(enabled) => toggle(plan, enabled)}
              />
            </header>
            <div class="v-plan-facts">
              <div><small>固定组合</small><strong>{covered.total}</strong></div>
              <div><small>模型线路</small><strong>{modelCount}</strong></div>
              <div><small>最近执行</small><span>{time(plan.lastAt)}</span></div>
            </div>
            <div class="v-coverage">
              <div>
                <span>组合覆盖</span><strong
                  >{covered.done} / {covered.total}</strong
                >
              </div>
              <Progress
                aria-label={`${name} 组合覆盖`}
                value={covered.done}
                max={Math.max(covered.total, 1)}
               />
            </div>
            <section class="v-bindings">
              <div>
                <strong>验证线路</strong><small
                  >{modelCount > 2 ? `2 / ${modelCount}` : modelCount} 模型</small
                >
              </div>
              {#each summaries(plan) as summary}<div>
                  <strong>{summary.model}</strong><Hint text={summary.keys}>{#snippet children(hintProps5)}<small {...hintProps5}
                    >{summary.keys}</small
                  >{/snippet}</Hint><span>思考{summary.effort}</span>
                </div>{/each}
            </section>
            <footer>
              <small
                >{plan.lines.length} Key · 每轮 {covered.total * 3} 样本</small
              >
              <div class="v-actions">
                <Button
                  variant="outline"
                  size="sm"
                  onclick={() => openPlan(plan)}>编辑范围</Button
                ><Hint text="检测会产生上游消耗">{#snippet children(hintProps6)}<Button {...mergeProps(hintProps6, { onclick: () => start(plan.providerId) })}
                  size="sm"
                  disabled={!desktop || busy || running || !covered.total}

                  ><Play data-icon="inline-start" />立即检测</Button
                >{/snippet}</Hint>
              </div>
            </footer>
          </article>{/each}{#if !scopedPlans.length}<Empty.Root class="col-span-full">
            <FlaskConical class="size-6" /><Empty.Title>还没有检测计划</Empty.Title>
            <Empty.Description>先为供应商选择要验证的模型、Key、协议与思考强度。</Empty.Description>
            <Button
              variant="outline"
              disabled={!desktop || !snapshot || !providers.length}
              onclick={() => openPlan()}
              ><Plus data-icon="inline-start" />创建检测计划</Button
            >
          </Empty.Root>{/if}
      </div>{/if}
    </Tabs.Content>
    {#if tab === "results"}
      <footer class="v-surface-footer">
        <span>差异优先，其次按最近检测倒序 · 仅保留最近一轮</span><span
          >检测会产生上游消耗；结果不是身份认证。</span
        >
      </footer>
    {/if}
  </Tabs.Root>
</section>
<Dialog.Root
  open={editor}
  onOpenChange={(value) => {
    if (!busy) editor = value;
  }}
  ><Dialog.Content
    class="v-wide-dialog"
    showCloseButton={!busy}
    onInteractOutside={(event) => {
      if (busy) event.preventDefault();
    }}
    onEscapeKeydown={(event) => {
      if (busy) event.preventDefault();
    }}
    ><Dialog.Header
      ><Dialog.Title>{editing ? "编辑检测计划" : "创建检测计划"}</Dialog.Title
      ><Dialog.Description
        >固定每条线路的模型、Key、请求协议与思考强度。</Dialog.Description
      ></Dialog.Header
    >{#if editor}<PlanEditor
        plan={editing}
        providers={providerId
          ? [
              ...providerOptions.filter((item) => item.value === providerId),
              ...providerOptions.filter((item) => item.value !== providerId),
            ]
          : providerOptions}
        targets={snapshot?.targets ?? []}
        {referenceModels}
        {busy}
        error={formError}
        onsave={save}
        onclose={() => (editor = false)}
      />{/if}</Dialog.Content
  ></Dialog.Root
>
<Dialog.Root
  open={!!selectedReport}
  onOpenChange={(value) => {
    if (!value) reportID = "";
  }}
  ><Dialog.Content class="flex max-h-[86dvh] w-[calc(100vw-48px)] flex-col gap-0 overflow-hidden p-0 sm:max-w-[min(920px,88vw)]"
    >{#if selectedReport}{#key `${selectedReport.id}:${selectedReport.time}`}<Report
          report={selectedReport}
        />{/key}{/if}
    <Dialog.Footer class="shrink-0 border-t px-6 py-3.5">
      <Button variant="outline" onclick={() => (reportID = "")}>关闭</Button>
    </Dialog.Footer></Dialog.Content
  ></Dialog.Root
>
