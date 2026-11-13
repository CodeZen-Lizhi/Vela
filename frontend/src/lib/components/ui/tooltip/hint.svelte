<script lang="ts">
  import type { Snippet } from "svelte";
  import Root from "./tooltip.svelte";
  import Trigger from "./tooltip-trigger.svelte";
  import Content from "./tooltip-content.svelte";

  /** 将纯文字提示附着到原控件，不新增DOM容器；空提示直接渲染原内容。 */
  let { text, children }: {
    /** 提示全文；空值表示当前元素不需要悬停提示。 */
    text: string | null | undefined;
    /** 原元素接收Tooltip触发属性，保留布局、事件及可访问名称。 */
    children: Snippet<[Record<string, unknown>]>;
  } = $props();
  /** 仅传递提示的关联和事件，避免按钮默认type及状态覆盖输入框、选择器的原有契约。 */
  function triggerProps(props: Record<string, unknown>): Record<string, unknown> {
    const { "data-slot": slot, type, disabled, "data-state": state, "data-disabled": disabledState, ...attributes } = props;
    return attributes;
  }
</script>

{#if text}
  <Root>
    <Trigger>
      {#snippet child({ props })}{@render children(triggerProps(props))}{/snippet}
    </Trigger>
    <Content>{text}</Content>
  </Root>
{:else}
  {@render children({})}
{/if}
