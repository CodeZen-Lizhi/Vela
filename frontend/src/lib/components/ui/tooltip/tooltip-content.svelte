<script lang="ts">
  import { Tooltip as TooltipPrimitive } from "bits-ui";
  import { cn, type WithoutChildrenOrChild } from "$lib/utils.js";
  import TooltipPortal from "./tooltip-portal.svelte";
  import type { ComponentProps } from "svelte";

  /** 全局纯文字提示：无底色、边框、阴影或箭头，颜色跟随主题。 */
  let {
    ref = $bindable(null),
    class: className,
    sideOffset = 6,
    side = "bottom",
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
    class={cn("z-50 w-fit max-w-xs break-words whitespace-normal bg-transparent p-0 text-xs leading-relaxed text-foreground border-0 shadow-none outline-none", className)}
    {...restProps}
  >
    {@render children?.()}
  </TooltipPrimitive.Content>
</TooltipPortal>
