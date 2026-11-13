<script lang="ts">
	import { cn, type WithElementRef } from "$lib/utils.js";
	import type { HTMLAttributes } from "svelte/elements";

	/** 官方卡片组成：转发语义属性、节点引用及内容插槽，外观由共享主题维护。 */
	let {
		ref = $bindable(null),
		class: className,
		children,
		...restProps
	}: WithElementRef<HTMLAttributes<HTMLDivElement>> = $props();
</script>

<div
	bind:this={ref}
	data-slot="card-title"
	class={cn("text-sm font-semibold tracking-normal", className)}
	{...restProps}
>
	{@render children?.()}
</div>
