<script lang="ts">
	import { Tabs as TabsPrimitive } from "bits-ui";
	import { cn } from "$lib/utils.js";

	/** 页签容器固定使用页面分区结构，并保留横纵向及属性转发。 */
	let {
		ref = $bindable(null),
		class: className,
		...restProps
	}: TabsPrimitive.ListProps = $props();
</script>

<TabsPrimitive.List
	bind:ref
	data-slot="tabs-list"
	class={cn(
		"group/tabs-list inline-flex h-auto w-fit items-center justify-center gap-6 rounded-none border-b border-border bg-transparent p-0 text-muted-foreground group-data-[orientation=vertical]/tabs:flex-col",
		className
	)}
	{...restProps}
/>
