<script lang="ts">
	import { Tabs as TabsPrimitive } from "bits-ui";
	import { toggleVariants } from "$lib/components/ui/toggle";
	import { cn } from "$lib/utils.js";

	/** 页签复用分段选项的控件样式，保留Tabs自身键盘导航与选中语义。 */
	let {
		ref = $bindable(null),
		class: className,
		...restProps
	}: TabsPrimitive.TriggerProps = $props();
</script>

<TabsPrimitive.Trigger
	bind:ref
	data-slot="tabs-trigger"
	class={cn(
		toggleVariants({ variant: "segmented", size: "xs" }),
		"flex-1 group-data-[orientation=vertical]/tabs:w-full group-data-[orientation=vertical]/tabs:justify-start",
		className
	)}
	{...restProps}
/>
