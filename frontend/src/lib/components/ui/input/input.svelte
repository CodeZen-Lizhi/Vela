<script lang="ts">
	import { cn, type WithElementRef } from "$lib/utils.js";
	import type { HTMLInputAttributes, HTMLInputTypeAttribute } from "svelte/elements";

	type InputType = Exclude<HTMLInputTypeAttribute, "file">;

	/** normal为常规表单，compact为表格行内数字编辑。 */
	type Props = { density?: "normal" | "compact" } & WithElementRef<
		Omit<HTMLInputAttributes, "type"> &
			({ type: "file"; files?: FileList } | { type?: InputType; files?: undefined })
	>;

	let {
		ref = $bindable(null),
		value = $bindable(),
		type,
		files = $bindable(),
		class: className,
		"data-slot": dataSlot = "input",
		density = "normal",
		...restProps
	}: Props = $props();
</script>

{#if type === "file"}
	<input
		bind:this={ref}
		data-slot={dataSlot}
		class={cn(
			"rounded-lg border-input bg-background focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/15 aria-invalid:border-b-destructive dark:aria-invalid:border-b-destructive/50 h-9 border px-3 py-1 text-base transition-[color,border-color] file:h-7 file:text-sm file:font-medium md:text-sm w-full min-w-0 outline-none file:inline-flex file:border-0 file:bg-transparent file:text-foreground placeholder:text-muted-foreground disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50",
			density === "compact" && "h-7 px-2 text-xs md:text-xs",
			className
		)}
		type="file"
		bind:files
		bind:value
		{...restProps}
	/>
{:else}
	<input
		bind:this={ref}
		data-slot={dataSlot}
		class={cn(
			"rounded-lg border-input bg-background focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/15 aria-invalid:border-b-destructive dark:aria-invalid:border-b-destructive/50 h-9 border px-3 py-1 text-base transition-[color,border-color] file:h-7 file:text-sm file:font-medium md:text-sm w-full min-w-0 outline-none file:inline-flex file:border-0 file:bg-transparent file:text-foreground placeholder:text-muted-foreground disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50",
			density === "compact" && "h-7 px-2 text-xs md:text-xs",
			className
		)}
		{type}
		bind:value
		{...restProps}
	/>
{/if}

<style>
  /* 保留数字输入与原生校验，仅统一隐藏不适合紧凑表单的步进箭头。 */
  input[type="number"] { appearance:textfield; }
  input[type="number"]::-webkit-inner-spin-button, input[type="number"]::-webkit-outer-spin-button { -webkit-appearance:none; margin:0; }
</style>
