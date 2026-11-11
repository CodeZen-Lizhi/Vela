<script lang="ts">
  import { untrack } from "svelte";
  import { Button } from "$lib/components/ui/button";
  import { Switch } from "$lib/components/ui/switch";
  import * as Field from "$lib/components/ui/field";
  import Choice from "./Choice.svelte";
  import type {
    VerificationPlan,
    VerificationLine,
    VerificationTarget,
  } from "$lib/generated/mygo";
  import { efforts, protocols } from "./display";
  /** 编辑草稿独立于快照，保存失败保留全部输入；providers 为供应商单选值。 */
  let {
    plan,
    providers,
    targets,
    busy,
    error,
    onsave,
    onclose,
  }: {
    /** 原计划；null 表示新建。 */ plan: VerificationPlan | null;
    /** 全部供应商的稳定身份及名称。 */ providers: {
      value: string;
      label: string;
    }[];
    /** 当前完整目标快照，包括不可执行原因。 */ targets: VerificationTarget[];
    /** 保存中锁定输入；错误由父页保存流程提供。 */ busy: boolean;
    error: string;
    /** 保存真实计划及取消草稿的回调。 */ onsave: (
      plan: VerificationPlan,
    ) => Promise<void>;
    onclose: () => void;
  } = $props();
  /** 草稿行增加局部身份，避免移除后控件焦点串行。 */
  type Draft = VerificationLine & {
    /** 仅用于编辑器键控，不写入后端。 */ id: string;
  };
  /** 空白线路不预选模型、Key 或协议。 */
  function blank(): Draft {
    return {
      id: crypto.randomUUID(),
      modelId: "",
      keyId: "",
      protocol: "",
      effort: "auto",
    };
  }
  /** 表单运行范围、线路与本地校验信息。 */
  let providerId = $state(
    untrack(() => plan?.providerId ?? providers[0]?.value ?? ""),
  );
  let enabled = $state(untrack(() => plan?.enabled ?? false)),
    hours = $state(untrack(() => String(plan?.hours ?? 6)));
  let lines = $state<Draft[]>(
    untrack(() => [
      ...(plan?.lines.map((line) => ({ ...line, id: crypto.randomUUID() })) ??
        []),
      blank(),
    ]),
  );
  let validation = $state("");
  /** 当前供应商可选目标和已完整配置的固定组合。 */
  const scope = $derived(
    targets.filter((target) => target.providerId === providerId),
  );
  const complete = $derived(
    lines.filter((line) => line.modelId && line.keyId && line.protocol),
  );
  const models = $derived(new Set(complete.map((line) => line.modelId)).size);
  /** 当前行兼容 Key 排除同模型其他行已占用的 Key。 */
  function keys(line: Draft): { value: string; label: string }[] {
    return Array.from(
      new Map(
        scope
          .filter(
            (target) =>
              target.modelId === line.modelId &&
              !target.reason &&
              !lines.some(
                (other) =>
                  other.id !== line.id &&
                  other.modelId === line.modelId &&
                  other.keyId === target.keyId,
              ),
          )
          .map((target) => [
            target.keyId,
            { value: target.keyId, label: target.keyName },
          ]),
      ).values(),
    );
  }
  /** 所有关联模型去重；无可新增 Key 的模型仅保留当前值与不可执行提示。 */
  function modelOptions(line: Draft): { value: string; label: string }[] {
    return Array.from(
      new Map(
        scope
          .filter(
            (target) =>
              target.modelId === line.modelId ||
              !scope.some(
                (item) => item.modelId === target.modelId && !item.reason,
              ) ||
              keys({ ...line, modelId: target.modelId }).length,
          )
          .map((target) => [
            target.modelId,
            {
              value: target.modelId,
              label: target.modelName || target.modelId,
            },
          ]),
      ).values(),
    );
  }
  /** 模型改变时清 Key 并沿用同模型强度；选择末行 Key 后保留空白尾行。 */
  function change(
    line: Draft,
    field: "modelId" | "keyId" | "protocol" | "effort",
    value: string,
  ): void {
    if (field === "modelId") {
      line.keyId = "";
      line.effort =
        lines.find((other) => other.id !== line.id && other.modelId === value)
          ?.effort ?? "auto";
    }
    line[field] = value;
    if (lines.at(-1)?.keyId) lines = [...lines, blank()];
  }
  /** 新建时切换供应商清空草稿，避免跨供应商提交绑定。 */
  function changeProvider(value: string): void {
    providerId = value;
    lines = [blank()];
    validation = "";
  }
  /** 移除线路后始终保留可继续输入的一行。 */
  function remove(id: string): void {
    lines = lines.filter((line) => line.id !== id);
    if (!lines.some((line) => !line.modelId && !line.keyId && !line.protocol))
      lines = [...lines, blank()];
  }
  /** 校验完整性、协议、重复和实时兼容关系，再提交显式线路。 */
  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    validation = "";
    const configured = lines.filter(
      (line) => line.modelId || line.keyId || line.protocol,
    );
    if (
      !configured.length ||
      configured.some((line) => !line.modelId || !line.keyId || !line.protocol)
    ) {
      validation =
        "请完成至少一条线路；有内容的行需选完模型、Key 和请求协议，或移除该行。";
      return;
    }
    const identities = configured.map(
      (line) => `${line.modelId}\u0000${line.keyId}`,
    );
    if (
      new Set(identities).size !== identities.length ||
      configured.some(
        (line) =>
          !protocols.some((option) => option.value === line.protocol) ||
          !scope.some(
            (target) =>
              target.modelId === line.modelId &&
              target.keyId === line.keyId &&
              !target.reason,
          ),
      )
    ) {
      validation = "线路重复或 Key 已不兼容，请重新选择。";
      return;
    }
    await onsave({
      providerId,
      enabled,
      hours: Number(hours),
      lines: configured.map(({ modelId, keyId, protocol, effort }) => ({
        modelId,
        keyId,
        protocol,
        effort,
      })),
      nextAt: plan?.nextAt ?? 0,
      lastAt: plan?.lastAt ?? 0,
    });
  }
</script>

<form class="v-editor" onsubmit={submit}>
  <fieldset disabled={busy} class="v-fieldset">
    <section class="v-form-card">
      <h3><span>01</span>运行范围</h3>
      <Field.Group class="v-run-settings">
        <Field.Field
          ><Field.Label>供应商</Field.Label><Choice
            value={providerId}
            options={providers}
            label="计划供应商"
            disabled={!!plan}
            onValueChange={changeProvider}
          /></Field.Field
        >
        <Field.Field
          ><Field.Label>检测周期</Field.Label><Choice
            value={hours}
            onValueChange={(value) => (hours = value)}
            label="检测周期"
            disabled={!enabled}
            options={[1, 6, 12, 24].map((hour) => ({
              value: String(hour),
              label: `每 ${hour} 小时`,
            }))}
          /></Field.Field
        >
        <Field.Field
          ><Field.Label>自动运行</Field.Label>
          <div class="v-mode">
            <span>{enabled ? "按周期执行" : "仅手动检测"}</span><Switch
              bind:checked={enabled}
              aria-label="自动运行"
            />
          </div></Field.Field
        >
      </Field.Group>
      <p class="v-note">
        首次开启不立即执行；睡眠或退出期间不执行，恢复后不补测。
      </p>
    </section>
    <section class="v-form-card">
      <h3><span>02</span>验证线路</h3>
      <div class="v-lines">
        {#each lines as line, index (line.id)}
          {@const choices = keys(line)}
          {@const shared =
            !!line.modelId &&
            lines.filter((other) => other.modelId === line.modelId).length > 1}
          <div class="v-line">
            <span class="v-line-number"
              >{String(index + 1).padStart(2, "0")}</span
            >
            <Field.Field
              ><Field.Label>执行模型</Field.Label><Choice
                value={line.modelId}
                options={modelOptions(line)}
                label={`第 ${index + 1} 行模型`}
                onValueChange={(value) => change(line, "modelId", value)}
              /></Field.Field
            >
            <Field.Field
              ><Field.Label>执行 Key</Field.Label><Choice
                value={line.keyId}
                options={choices}
                disabled={!line.modelId || !choices.length}
                label={`第 ${index + 1} 行 Key`}
                onValueChange={(value) => change(line, "keyId", value)}
              />{#if line.modelId && !choices.length}<small
                  class="text-destructive"
                  >无可用兼容 Key{scope.find(
                    (target) => target.modelId === line.modelId,
                  )?.reason
                    ? `：${scope.find((target) => target.modelId === line.modelId)?.reason}`
                    : ""}</small
                >{/if}</Field.Field
            >
            <Field.Field
              ><Field.Label>请求协议</Field.Label><Choice
                value={line.protocol}
                options={protocols}
                disabled={!line.modelId}
                label={`第 ${index + 1} 行请求协议`}
                onValueChange={(value) => change(line, "protocol", value)}
              /></Field.Field
            >
            <Field.Field
              ><Field.Label>思考强度</Field.Label><Choice
                value={line.effort}
                options={efforts}
                disabled={!line.modelId || shared}
                label={`第 ${index + 1} 行思考强度`}
                onValueChange={(value) => change(line, "effort", value)}
              /></Field.Field
            >
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={!line.modelId && !line.keyId && !line.protocol}
              aria-label={`移除第 ${index + 1} 行`}
              onclick={() => remove(line.id)}>移除</Button
            >
          </div>
        {/each}
      </div>
      <div class="v-math" aria-live="polite">
        {#each [{ name: "执行模型", value: models }, { name: "Key 绑定", value: complete.length }, { name: "固定组合", value: complete.length }, { name: "每轮样本", value: complete.length * 3 }] as item}<div
          >
            <small>{item.name}</small><strong>{item.value}</strong>
          </div>{/each}
      </div>
    </section>
  </fieldset>
  {#if validation || error}<p role="alert" class="v-error">
      {validation || error}
    </p>{/if}
  <footer class="v-editor-footer">
    <p>
      {enabled
        ? `自动运行约 ${Math.round((complete.length * 3 * 24) / Number(hours))} 次请求 / 天`
        : "自动运行关闭，仅手动执行"}<small
        >检测会产生上游消耗，取消不能撤销已发出请求的费用。</small
      >
    </p>
    <div class="v-actions">
      <Button type="button" variant="outline" disabled={busy} onclick={onclose}
        >取消</Button
      ><Button type="submit" disabled={busy}
        >{busy ? "正在保存…" : "保存计划"}</Button
      >
    </div>
  </footer>
</form>
