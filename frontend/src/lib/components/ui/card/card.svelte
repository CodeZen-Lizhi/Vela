<script lang="ts">
	import { cn, type WithElementRef } from "$lib/utils.js";
	import type { HTMLAttributes } from "svelte/elements";

	/** 官方卡片组成：转发语义属性、节点引用及内容插槽，外观由共享主题维护。 */
	let {
		ref = $bindable(null),
		class: className,
		children,
		size = "default",
		variant = "default",
		...restProps
	}: WithElementRef<HTMLAttributes<HTMLDivElement>> & {
		/** size 控制现有指标卡密度。 */ size?: "default" | "sm";
		/** path 为请求入口/结果的紧凑虚线卡，其外观统一在共享组件维护。 */ variant?: "default" | "path";
	} = $props();
</script>

<div
	bind:this={ref}
	data-slot="card"
	data-size={size}
	class={cn("bg-card text-card-foreground border border-border rounded-2xl gap-4 overflow-hidden py-5 text-sm shadow-sm [--card-spacing:--spacing(5)] has-[>img:first-child]:pt-0 data-[size=sm]:[--card-spacing:--spacing(5)] *:[img:first-child]:rounded-none *:[img:last-child]:rounded-none group/card flex flex-col", variant === "path" && "min-h-[72px] justify-center gap-[5px] rounded-[13px] border-dashed py-0 shadow-none [--card-spacing:14px]", className)}
	{...restProps}
>
	{@render children?.()}
</div>
