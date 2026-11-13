<script lang="ts" module>
	import { type VariantProps, tv } from "tailwind-variants";

	export const toggleVariants = tv({
		base: "hover:text-foreground aria-pressed:bg-muted focus-visible:border-ring focus-visible:ring-ring/30 aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive gap-1.5 rounded-none text-xs font-semibold normal-case tracking-normal transition-colors [&_svg:not([class*='size-'])]:size-3.5 group/toggle inline-flex items-center justify-center whitespace-nowrap outline-none hover:bg-muted focus-visible:ring-[3px] disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0",
		variants: {
			variant: {
				default: "bg-transparent",
                /** 分段切换用于紧凑列表，卡片用于带说明的单选项。 */
                segmented: "rounded-md text-muted-foreground normal-case tracking-normal data-[state=on]:bg-background data-[state=on]:text-primary data-[state=on]:shadow-sm",
                card: "rounded-lg border border-border bg-background text-left normal-case tracking-normal hover:border-primary/35 data-[state=on]:border-primary/60 data-[state=on]:bg-accent data-[state=on]:text-foreground",
				outline: "border-input hover:bg-muted border bg-transparent",
			},
			size: {
				default: "h-10 min-w-10 px-6 has-data-[icon=inline-end]:pr-4 has-data-[icon=inline-start]:pl-4",
				/** xs适用于行内来源切换，choice用于包含说明的选项。 */
                xs: "h-6 min-w-9 px-2 text-[11px]",
                choice: "h-auto min-h-[62px] min-w-0 items-start justify-start gap-3 whitespace-normal px-3.5 py-4",
                sm: "h-9 min-w-9 px-4 has-data-[icon=inline-end]:pr-3 has-data-[icon=inline-start]:pl-3",
				lg: "h-11 min-w-11 px-8 has-data-[icon=inline-end]:pr-5 has-data-[icon=inline-start]:pl-5",
			},
		},
		defaultVariants: {
			variant: "default",
			size: "default",
		},
	});

	export type ToggleVariant = VariantProps<typeof toggleVariants>["variant"];
	export type ToggleSize = VariantProps<typeof toggleVariants>["size"];
	export type ToggleVariants = VariantProps<typeof toggleVariants>;
</script>

<script lang="ts">
	import { Toggle as TogglePrimitive } from "bits-ui";
	import { cn } from "$lib/utils.js";

	let {
		ref = $bindable(null),
		pressed = $bindable(false),
		class: className,
		size = "default",
		variant = "default",
		...restProps
	}: TogglePrimitive.RootProps & {
		variant?: ToggleVariant;
		size?: ToggleSize;
	} = $props();
</script>

<TogglePrimitive.Root
	bind:ref
	bind:pressed
	data-slot="toggle"
	class={cn(toggleVariants({ variant, size }), className)}
	{...restProps}
/>
