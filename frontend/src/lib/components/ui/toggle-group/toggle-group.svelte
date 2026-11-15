<script lang="ts" module>
	import { getContext, setContext } from "svelte";
	import { toggleVariants } from "$lib/components/ui/toggle/index.js";
	import type { VariantProps } from "tailwind-variants";

	type ToggleVariants = VariantProps<typeof toggleVariants>;

	interface ToggleGroupContext extends ToggleVariants {
		spacing?: number;
		orientation?: "horizontal" | "vertical";
	}

	export function setToggleGroupCtx(props: ToggleGroupContext) {
		setContext("toggleGroup", props);
	}

	export function getToggleGroupCtx() {
		return getContext<Required<ToggleGroupContext>>("toggleGroup");
	}
</script>

<script lang="ts">
	import { ToggleGroup as ToggleGroupPrimitive } from "bits-ui";
	import { cn } from "$lib/utils.js";

	let {
		ref = $bindable(null),
		value = $bindable(),
		class: className,
		onValueChange,
		allowEmpty = false,
		size = "default",
		spacing = 0,
		orientation = "horizontal",
		variant = "default",
		...restProps
	}: ToggleGroupPrimitive.RootProps &
		ToggleVariants & {
			/** 单选默认保留当前项；仅取消排序等业务允许主动清空。 */
			allowEmpty?: boolean;
			spacing?: number;
			orientation?: "horizontal" | "vertical";
		} = $props();

	/** 在原始控件写入前拒绝取消必选项，不触发空值回调；多选仍按原规则切换。 */
	function setValue(next: never): void {
		if (restProps.type === "single" && next === "" && !allowEmpty) return;
		value = next;
		// 单选/多选由底层按 type 配对；never 仅兼容其联合类型绑定，不转换运行时值。
		onValueChange?.(next);
	}

	setToggleGroupCtx({
		get variant() {
			return variant;
		},
		get size() {
			return size;
		},
		get spacing() {
			return spacing;
		},
		get orientation() {
			return orientation;
		},
	});
</script>

<!-- 单选和多选的联合类型经解构后无法关联，绑定沿用 never 兼容；运行时值仍由 type 决定。 -->
<ToggleGroupPrimitive.Root
	bind:value={() => value as never, setValue}
	bind:ref
	{orientation}
	data-slot="toggle-group"
	data-variant={variant}
	data-size={size}
	data-spacing={spacing}
	style={`--gap: ${spacing}`}
	class={cn(
		"data-[spacing=0]:data-[variant=outline]:rounded-none group/toggle-group flex w-fit flex-row items-center gap-[--spacing(var(--gap))] data-vertical:flex-col data-vertical:items-stretch",
		variant === "card" && "items-stretch",
		orientation === "vertical" && "flex-col items-stretch",
		variant === "segmented" && "rounded-lg border border-border bg-muted p-0.5",
		className
	)}
	{...restProps}
/>
