<script lang="ts">
  import { Tooltip as TooltipPrimitive } from "bits-ui";
  import { cn, type WithoutChildrenOrChild } from "$lib/utils.js";
  import TooltipPortal from "./tooltip-portal.svelte";
  import type { ComponentProps } from "svelte";

  /** 紧凑文字提示沿用主题反色，无边框、阴影或箭头，不截住鼠标操作。 */
  let {
    ref = $bindable(null),
    class: className,
    sideOffset = 4,
    side = "top",
    children,
    portalProps,
    ...restProps
  }: TooltipPrimitive.ContentProps & {
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
    class={cn("z-50 w-fit max-w-[min(26rem,calc(100vw-1rem))] rounded-xs border-0 bg-foreground px-1.5 py-0.5 text-[11px] leading-4 text-background shadow-none outline-none break-words whitespace-normal pointer-events-none", className)}
    {...restProps}
  >
    {@render children?.()}
  </TooltipPrimitive.Content>
</TooltipPortal>
