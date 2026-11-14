<script lang="ts">
  import type { ComponentProps, Snippet } from "svelte";
  import { mergeProps } from "bits-ui";
  import Root from "./tooltip.svelte";
  import Trigger from "./tooltip-trigger.svelte";
  import Content from "./tooltip-content.svelte";

  /** 仅为实际省略的文字补充全文；折叠侧栏菜单可显式保留图标说明。 */
  let { text, overflowOnly = true, side = "top", children }: {
    /** 提示全文；空值表示当前元素不需要悬停提示。 */
    text: string | null | undefined;
    /** 默认仅在文字以省略号截断时显示；只有折叠侧栏菜单可设为false。 */
    overflowOnly?: boolean;
    /** 菜单提示放在侧栏右侧，省略文字贴近原元素显示。 */
    side?: ComponentProps<typeof Content>["side"];
    /** 原元素接收Tooltip触发属性，保留布局、事件及可访问名称。 */
    children: Snippet<[Record<string, unknown>]>;
  } = $props();
  /** 按本次悬停或聚焦时的实际尺寸判断，窗口变宽后不沿用旧结果。 */
  let overflowing = $state(false);
  /** 未截断的名称拒绝打开提示。 */
  let open = $state(false);
  /** 只判断当前文字是否省略，浮层直接锚定原元素，避免按整行定位产生过远间距。 */
  function prepareHint(event: Event): void {
    const element = event.currentTarget as HTMLElement;
    overflowing = getComputedStyle(element).textOverflow === "ellipsis" && element.clientWidth > 0 && element.scrollWidth > element.clientWidth;
  }
  /** 仅传递提示的关联和事件，避免按钮默认type及状态覆盖输入框、选择器的原有契约。 */
  function triggerProps(props: Record<string, unknown>): Record<string, unknown> {
    const { "data-slot": slot, type, disabled, "data-state": state, "data-disabled": disabledState, ...attributes } = props;
    return overflowOnly ? mergeProps({ onpointerenter: prepareHint, onfocus: prepareHint }, attributes) : attributes;
  }
</script>

{#if text}
  <Root delayDuration={300} disableHoverableContent bind:open={() => open, (next) => open = next && (!overflowOnly || overflowing)}>
    <Trigger>
      {#snippet child({ props })}{@render children(triggerProps(props))}{/snippet}
    </Trigger>
    <Content side={overflowOnly ? "bottom" : side} align={overflowOnly ? "start" : "center"}>{text}</Content>
  </Root>
{:else}
  {@render children({})}
{/if}
