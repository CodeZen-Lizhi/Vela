<script lang="ts">
	import { Tabs as TabsPrimitive } from "bits-ui";
	import { cn } from "$lib/utils.js";

	let {
		ref = $bindable(null),
		value = $bindable(""),
		class: className,
		...restProps
	}: TabsPrimitive.RootProps = $props();
</script>

<!-- 当前 bits-ui 使用 data-orientation / data-state，样式变体须匹配这些属性。 -->
<TabsPrimitive.Root
	bind:ref
	bind:value
	data-slot="tabs"
	class={cn("gap-2 group/tabs flex data-[orientation=horizontal]:flex-col", className)}
	{...restProps}
/>
