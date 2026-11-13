<script lang="ts" module>
	import { tv, type VariantProps } from "tailwind-variants";

	/** 所有页签共用灰底描边与紧凑间距，与ToggleGroup分段容器一致。 */
	export const tabsListVariants = tv({
		base: "rounded-lg border border-border p-0.5 gap-1 group/tabs-list inline-flex w-fit items-center justify-center text-muted-foreground group-data-[orientation=vertical]/tabs:flex-col",
		variants: {
			variant: {
				default: "bg-muted",
			},
		},
		defaultVariants: {
			variant: "default",
		},
	});

	export type TabsListVariant = VariantProps<typeof tabsListVariants>["variant"];
</script>

<script lang="ts">
	import { Tabs as TabsPrimitive } from "bits-ui";
	import { cn } from "$lib/utils.js";

	/** 页签容器统一分段外观，同时保留现有横纵向与属性转发。 */
	let {
		ref = $bindable(null),
		variant = "default",
		class: className,
		...restProps
	}: TabsPrimitive.ListProps & {
		variant?: TabsListVariant;
	} = $props();
</script>

<TabsPrimitive.List
	bind:ref
	data-slot="tabs-list"
	data-variant={variant}
	class={cn(tabsListVariants({ variant }), className)}
	{...restProps}
/>
