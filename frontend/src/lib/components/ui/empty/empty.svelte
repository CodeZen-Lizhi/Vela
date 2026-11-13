<script lang="ts">
	import { cn, type WithElementRef } from "$lib/utils.js";
	import type { HTMLAttributes } from "svelte/elements";

	let {
		ref = $bindable(null),
		class: className,
        size = "default",
		children,
		...restProps
	}: WithElementRef<HTMLAttributes<HTMLDivElement>> & {
        /** compact用于弹窗内的局部空态，default用于列表空态。 */
        size?: "default" | "compact";
    } = $props();
</script>

<div
	bind:this={ref}
	data-slot="empty"
	class={cn(
		"gap-4 border-dashed p-12 flex w-full min-w-0 flex-1 flex-col items-center justify-center text-center text-balance text-muted-foreground",
		size === "compact" && "px-4 py-5",
		className
	)}
	{...restProps}
>
	{@render children?.()}
</div>
