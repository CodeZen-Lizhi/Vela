<script lang="ts">
	import { cn, type WithElementRef } from "$lib/utils.js";
	import type { HTMLAttributes } from "svelte/elements";

	let {
		ref = $bindable(null),
		class: className,
		children,
		variant = "default",
		...restProps
	}: WithElementRef<HTMLAttributes<HTMLTableSectionElement>> & {
		/** 列表表头沿用模型页的浅底与紧凑密度，默认变体保留其他表格外观。 */
		variant?: "default" | "list";
	} = $props();
</script>

<thead
	bind:this={ref}
	data-slot="table-header"
	class={cn("[&_tr]:border-b", variant === "list" && "[&_th]:h-10 [&_th]:bg-[color-mix(in_srgb,var(--muted)_40%,var(--card))] [&_th]:px-[var(--table-head-inset,1rem)] [&_th]:py-2 [&_th]:text-xs [&_th]:font-medium [&_th]:normal-case [&_th]:tracking-normal [&_tr:hover]:bg-transparent", className)}
	{...restProps}
>
	{@render children?.()}
</thead>
