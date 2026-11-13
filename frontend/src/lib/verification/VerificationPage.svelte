<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import * as Empty from "$lib/components/ui/empty";
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
    protocols,
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
  /** 检测目标状态直接区分已有可用结果、等待检测和不可执行。 */
  type PlanLineState = "covered" | "pending" | "blocked" | "running";
  /** 单条线路摘要保留定位所需的模型、Key、协议和强度，不在卡内只展示抽象计数。 */
  type PlanLineSummary = {
    model: string;
    key: string;
    protocol: string;
    effort: string;
    state: PlanLineState;
    reason: string;
  };
  const lineStates = {
    covered: { label: "已有结果", variant: "positive" },
    pending: { label: "待检测", variant: "warning" },
    blocked: { label: "不可执行", variant: "negative" },
    running: { label: "检测中", variant: "processing" },
  } as const;
  /** 判断单条线路当前是否有可用结果；兼容关系失效时保留真实原因。 */
  function lineState(
    plan: VerificationPlan,
    line: VerificationPlan["lines"][number],
  ): { state: PlanLineState; reason: string } {
    const target = snapshot?.targets.find(
      (item) =>
        item.providerId === plan.providerId &&
        item.modelId === line.modelId &&
        item.keyId === line.keyId,
    );
    if (!target) return { state: "blocked", reason: "模型或 Key 已不可用" };
    if (target.reason) return { state: "blocked", reason: target.reason };
    if (
      running &&
      snapshot?.progress.target.providerId === plan.providerId &&
      sameLine(line, snapshot.progress.target)
    )
      return { state: "running", reason: "" };
    const report = snapshot?.reports.find(
      (item) =>
        item.target.providerId === plan.providerId &&
        sameLine(line, item.target),
    );
    if (
      report &&
      !unavailable(report, snapshot?.targets ?? []) &&
      report.state === "complete" &&
      report.conclusion !== "insufficient"
    )
      return { state: "covered", reason: "" };
    return { state: "pending", reason: "" };
  }
  /** 卡片最多直接展示两条线路，其余通过编辑范围查看，避免方卡内出现滚动区。 */
  function summaries(plan: VerificationPlan): PlanLineSummary[] {
    return plan.lines.slice(0, 2).map((line) => {
      const target = snapshot?.targets.find(
        (item) =>
          item.providerId === plan.providerId &&
          item.modelId === line.modelId &&
          item.keyId === line.keyId,
      );
      return {
        model: target?.modelName || line.modelId,
        key: target?.keyName || line.keyId,
        protocol:
          protocols.find((item) => item.value === line.protocol)?.label ||
          line.protocol,
        effort: effort(line.effort),
        ...lineState(plan, line),
      };
    });
  }
  /** 计划级状态用可读文案表达整体进度，不把抽象组合比例作为主要信息。 */
  function planState(
    covered: { total: number; done: number },
    providerId: string,
  ): {
    label: string;
    variant: "negative" | "warning" | "info" | "positive" | "processing";
    detail: string;
  } {
    if (running && snapshot?.progress.target.providerId === providerId)
      return {
        label: "检测中",
        variant: "processing",
        detail: "正在采集本轮样本并等待检测结果",
      };
    if (!covered.total)
      return {
        label: "不可执行",
        variant: "negative",
        detail: "当前没有可执行的检测目标",
      };
    if (!covered.done)
      return {
        label: "尚未检测",
        variant: "warning",
        detail: "等待首次检测",
      };
    if (covered.done < covered.total)
      return {
        label: "部分完成",
        variant: "info",
        detail: "仍有检测目标需要补充结果",
      };
    return {
      label: "检测完成",
      variant: "positive",
      detail: "全部目标已有可用结果",
    };
  }
</script>

<section class="v-page list-page">
  <PageHeader title="模型验证" description="按供应商、模型、Key 与思考强度追踪固定线路的覆盖和差异。" />
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
        <Button
          class="v-create-plan"
          size="sm"
          disabled={!desktop || !snapshot || !providers.length}
          onclick={() => openPlan()}
          ><Plus data-icon="inline-start" />{providerId &&
          snapshot?.plans.some((plan) => plan.providerId === providerId)
            ? "编辑当前计划"
            : "创建检测计划"}</Button
        >
      <div class="v-filters">
        <!-- 结果筛选只属于验证结果页签；计划页签不展示也不能聚焦这些控件。 -->
        <div class="v-result-filters" inert={tab !== "results"} aria-hidden={tab !== "results"}>
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
        </div>
        <Choice
          bind:value={providerId}
          label="验证供应商"
          options={[{ value: "", label: "全部供应商" }, ...providerOptions]}
        />
      </div>
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
                  <span class="v-avatar">{report.target.providerName.slice(0, 1)}</span
                  ><Hint overflowOnly text={report.target.providerName}>{#snippet children(hintProps0)}<strong {...hintProps0}>{report.target.providerName}</strong>{/snippet}</Hint>
                </div></Table.Cell
              ><Table.Cell
                ><strong>{report.target.modelName}</strong><small
                  >{report.target.requestModel} · 思考{effort(
                    report.target.effort,
                  )}</small
                ></Table.Cell
              ><Table.Cell
                ><div class="v-identity">
                  <KeyRound class="size-3" /><Hint overflowOnly text={report.target.keyName}>{#snippet children(hintProps1)}<span {...hintProps1} class="v-key-name">{report.target.keyName}</span>{/snippet}</Hint>
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
                  >{/if}</Table.Cell
              ><Table.Cell data-align="number"
                ><strong>{probability(report.candidates[0]?.probability)}</strong></Table.Cell
              ><Table.Cell>{time(report.time)}</Table.Cell><Table.Cell data-align="control"
                ><div class="v-actions">
                  <Button onclick={() => start(report.target.providerId, report.id)}
                    variant="ghost"
                    size="row-action"
                    aria-label={`复测 ${report.target.modelName}`}

                    disabled={!desktop || running || busy || !!reason}

                    ><Play /></Button
                  ><Button onclick={() => (reportID = report.id)}
                    variant="ghost"
                    size="row-action"
                    aria-label={`查看 ${report.target.modelName} 验证详情`}

                    ><Eye /></Button
                  >
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
            plan.providerId}{@const state = planState(covered, plan.providerId)}
          <article class="v-plan">
            <header>
              <div class="v-identity">
                <span class="v-avatar">{name.slice(0, 1)}</span>
                <div>
                  <Hint overflowOnly text={name}>{#snippet children(hintProps4)}<strong {...hintProps4}>{name}</strong>{/snippet}</Hint><small
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
            <section class="v-plan-summary" aria-label={`${name} 检测状态`}>
              <div class="v-plan-state">
                <Badge variant={state.variant}>{state.label}</Badge>
                <span>{state.detail}</span>
              </div>
              <div class="v-plan-last-run">
                <span>最近执行</span><strong>{time(plan.lastAt)}</strong>
              </div>
            </section>
            <section class="v-bindings">
              <div class="v-bindings-heading">
                <strong>检测内容</strong>
              </div>
              {#each summaries(plan) as summary, index}<div class="v-target">
                  <span class="v-target-index"
                    >{String(index + 1).padStart(2, "0")}</span
                  >
                  <div class="v-target-copy">
                    <div class="v-target-heading">
                      <strong>{summary.model}</strong>
                      <Badge variant={lineStates[summary.state].variant}
                        >{lineStates[summary.state].label}</Badge
                      >
                    </div>
                    <Hint overflowOnly text={summary.key}>{#snippet children(hintProps5)}<small {...hintProps5}
                      >{summary.key}</small
                    >{/snippet}</Hint>
                    <div class="v-target-meta">
                      <span>{summary.protocol}</span><span>思考{summary.effort}</span>
                    </div>
                    {#if summary.reason}<small class="v-target-reason"
                        >{summary.reason}</small
                      >{/if}
                  </div>
                </div>{/each}
              {#if plan.lines.length > 2}<small class="v-target-more"
                  >更多线路请通过“编辑范围”查看</small
                >{/if}
            </section>
            <footer>
              <small>检测会产生上游费用</small>
              <div class="v-actions">
                <Button
                  variant="outline"
                  size="sm"
                  onclick={() => openPlan(plan)}>编辑范围</Button
                ><Button onclick={() => start(plan.providerId)}
                  size="sm"
                  disabled={!desktop || busy || running || !covered.total}

                  ><Play data-icon="inline-start" />立即检测</Button
                >
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
      <footer class="v-surface-footer" class:invisible={tab !== "results"} aria-hidden={tab !== "results"}>
        <span>差异优先，其次按最近检测倒序 · 仅保留最近一轮</span><span>检测会产生上游消耗；结果不是身份认证。</span
        >
      </footer>
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
