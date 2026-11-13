<script lang="ts">
	import { cn, type WithElementRef } from "$lib/utils.js";
	import type { HTMLAttributes } from "svelte/elements";

	let {
		ref = $bindable(null),
		class: className,
		children,
		...restProps
	}: WithElementRef<HTMLAttributes<HTMLDivElement>> = $props();
</script>

<!-- 标题和说明预留关闭按钮的位置，长名称换行后也不会被图标遮住。 -->
<div
	bind:this={ref}
	data-slot="dialog-header"
	class={cn("min-w-0 gap-2 flex flex-col pr-10 [overflow-wrap:anywhere]", className)}
	{...restProps}
>
	{@render children?.()}
</div>
