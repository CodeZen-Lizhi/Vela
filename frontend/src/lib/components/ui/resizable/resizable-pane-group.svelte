<script lang="ts">
	import * as ResizablePrimitive from "paneforge";
	import { cn } from "$lib/utils.js";

	/** 官方 Resizable 分组封装，传递布局约束和布局变化回调。 */
	let {
		ref = $bindable(null),
		class: className,
		...restProps
	}: ResizablePrimitive.PaneGroupProps = $props();
</script>

<ResizablePrimitive.PaneGroup
	bind:ref
	data-slot="resizable-pane-group"
	class={cn(
		// PaneForge 使用 data-direction 表达分栏方向。
		"cn-resizable-panel-group flex h-full w-full data-[direction=vertical]:flex-col",
		className
	)}
	{...restProps}
/>
