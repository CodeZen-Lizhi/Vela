<script lang="ts">
	import { cn, type WithElementRef, type WithoutChildren } from "$lib/utils.js";
	import type { HTMLTextareaAttributes } from "svelte/elements";

	let {
		ref = $bindable(null),
		value = $bindable(),
		class: className,
		variant = "default",
		"data-slot": dataSlot = "textarea",
		...restProps
	}: WithoutChildren<WithElementRef<HTMLTextareaAttributes>> & {
		/** 默认沿用底线样式；outline 用于需要明确编辑边界的表单与代码正文。 */
		variant?: "default" | "outline";
	} = $props();
</script>

<textarea
	bind:this={ref}
	data-slot={dataSlot}
	class={cn(
		"border-transparent border-b-input bg-transparent focus-visible:border-b-ring aria-invalid:border-b-destructive dark:aria-invalid:border-b-destructive/50 resize-none rounded-none border px-0 py-3 text-base transition-[color,border-color] md:text-sm flex field-sizing-content min-h-16 w-full outline-none placeholder:text-muted-foreground disabled:cursor-not-allowed disabled:opacity-50",
		variant === "outline" && "field-sizing-fixed rounded-lg border-input bg-background px-3 py-2 text-sm leading-relaxed focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/30 aria-invalid:border-destructive",
		className
	)}
	bind:value
	{...restProps}></textarea>
