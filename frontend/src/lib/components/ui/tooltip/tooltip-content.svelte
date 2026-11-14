<script lang="ts">
  import { Tooltip as TooltipPrimitive } from "bits-ui";
  import { cn, type WithoutChildrenOrChild } from "$lib/utils.js";
  import TooltipPortal from "./tooltip-portal.svelte";
  import type { ComponentProps } from "svelte";

  /** 文字提示使用主题浅色浮层、细边框与轻阴影；详情变体增加内边距，均不截住鼠标操作。 */
  let {
    ref = $bindable(null),
    class: className,
    sideOffset = 4,
    side = "top",
    children,
    portalProps,
    variant = "default",
    ...restProps
  }: TooltipPrimitive.ContentProps & {
    /** 短文字与多行查询详情共用配色，详情增加留白。 */
    variant?: "default" | "detail";
    /** 沿用Tooltip的Portal配置，弹窗内提示仍由组件管理层叠。 */
    portalProps?: WithoutChildrenOrChild<ComponentProps<typeof TooltipPortal>>;
  } = $props();
</script>

<TooltipPortal {...portalProps}>
  <TooltipPrimitive.Content
    bind:ref
    data-slot="tooltip-content"
    {sideOffset}
    {side}
    class={cn("z-50 w-fit max-w-[min(20rem,calc(100vw-1rem))] rounded-md border border-border bg-popover px-2.5 py-1.5 text-xs leading-5 text-popover-foreground shadow-md outline-none break-words whitespace-normal pointer-events-none", variant === "detail" && "rounded-lg px-4 py-3 shadow-lg", className)}
    {...restProps}
  >
    {@render children?.()}
  </TooltipPrimitive.Content>
</TooltipPortal>
