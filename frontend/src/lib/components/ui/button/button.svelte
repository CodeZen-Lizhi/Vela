<script lang="ts" module>
	import { type VariantProps, tv } from "tailwind-variants";
	import { cn, type WithElementRef } from "$lib/utils.js";
	import type { HTMLAnchorAttributes, HTMLButtonAttributes } from "svelte/elements";

	export const buttonVariants = tv({
		base: "focus-visible:border-ring focus-visible:ring-ring/30 aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive dark:aria-invalid:border-destructive/50 rounded-lg border border-transparent bg-clip-padding text-sm font-medium focus-visible:ring-2 aria-invalid:ring-2 active:not-aria-[haspopup]:translate-y-px [&_svg:not([class*='size-'])]:size-3.5 group/button inline-flex shrink-0 items-center justify-center whitespace-nowrap transition-all outline-none select-none disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0",
		variants: {
			variant: {
				default: "bg-primary text-primary-foreground hover:bg-primary/80",
				outline: "border-input bg-background hover:bg-accent hover:text-foreground dark:hover:bg-input/30 aria-expanded:bg-muted aria-expanded:text-foreground",
				secondary: "bg-secondary text-secondary-foreground hover:bg-[color-mix(in_oklch,var(--secondary),var(--foreground)_5%)] aria-expanded:bg-secondary aria-expanded:text-secondary-foreground",
				/** 导航及选项以共享状态属性统一颜色，保留原型紧凑密度。 */
				navigation: "justify-start text-muted-foreground hover:bg-secondary hover:text-foreground aria-[current=page]:bg-accent aria-[current=page]:text-primary",
				/** 设置子分类保留轻量选中状态。 */
				subnavigation: "justify-start text-muted-foreground hover:text-foreground aria-[current=page]:bg-secondary aria-[current=page]:text-foreground aria-[current=page]:font-semibold",
				/** 可拖动供应商整行入口使用统一布局和拖动状态。 */
				row: "justify-start rounded-none border-0 bg-card text-left hover:bg-muted focus-visible:bg-muted active:translate-y-0 data-[dragging=true]:rounded-lg data-[dragging=true]:bg-accent data-[dragging=true]:shadow-lg data-[inactive=true]:opacity-60 aria-disabled:cursor-wait",
				/** 实时开关以主题色与呼吸灯强调运行状态。 */
				live: "border-primary/30 bg-background text-primary hover:bg-accent",
				ghost: "hover:bg-muted hover:text-foreground dark:hover:bg-muted/50 aria-expanded:bg-muted aria-expanded:text-foreground",
				destructive: "bg-destructive/10 hover:bg-destructive/20 focus-visible:ring-destructive/20 dark:focus-visible:ring-destructive/40 dark:bg-destructive/20 text-destructive focus-visible:border-destructive/40 dark:hover:bg-destructive/30",
				link: "text-primary underline-offset-4 hover:underline underline",
				/** 无底色的次要文本操作，悬停时强调可点击性。 */
				text: "text-muted-foreground hover:text-primary underline-offset-4 hover:underline",
			},
			size: {
				/** 侧栏与小型文字入口的尺寸统一维护。 */
				navigation: "h-auto min-h-10 gap-3 px-[var(--sidebar-nav-inset,10px)] py-[11px] text-[13px] font-medium",
				/** 设置子分类保留轻量选中状态。 */
				subnavigation: "h-auto min-h-[31px] justify-start px-2.5 py-[5px] text-xs font-normal",
				/** 在线目录卡片允许多行信息，保留统一按钮反馈。 */
				catalog: "h-auto min-w-0 flex-col items-stretch justify-start gap-2.5 whitespace-normal p-3 text-left",
				/** 可拖动供应商整行入口使用统一布局和拖动状态。 */
				row: "h-auto min-h-[58px] w-full min-w-0 justify-start gap-2.5 whitespace-normal px-4 py-3",
				/** 折叠标题使用整行点击范围。 */
				disclosure: "h-11 w-full gap-2 px-4 text-xs justify-between",
				/** 时间线等信息入口允许多行内容，保留统一按钮主题和焦点样式。 */
				content: "h-auto gap-2 px-3 py-2 whitespace-normal",
				// table-inline 用于表格文字入口，不占额外列宽，保留主题与焦点反馈。
				"table-inline": "h-auto gap-1 p-0 text-xs",
				default: "h-9 gap-2 px-3.5 has-data-[icon=inline-end]:pr-4 has-data-[icon=inline-start]:pl-4",
				xs: "h-7 gap-1 px-3 has-data-[icon=inline-end]:pr-2 has-data-[icon=inline-start]:pl-2 [&_svg:not([class*='size-'])]:size-3",
				sm: "h-9 gap-2 px-3.5 has-data-[icon=inline-end]:pr-3 has-data-[icon=inline-start]:pl-3",
				lg: "h-11 gap-1.5 px-8 has-data-[icon=inline-end]:pr-5 has-data-[icon=inline-start]:pl-5",
				icon: "size-10",
				"icon-xs": "size-7 [&_svg:not([class*='size-'])]:size-3",
				"icon-sm": "size-9",
				"icon-lg": "size-11",
				/** 随正文排布，不引入按钮内边距或固定高度。 */
				inline: "h-auto gap-1 rounded-sm p-0 font-normal",
			},
		},
		defaultVariants: {
			variant: "default",
			size: "default",
		},
	});

	export type ButtonVariant = VariantProps<typeof buttonVariants>["variant"];
	export type ButtonSize = VariantProps<typeof buttonVariants>["size"];

	export type ButtonProps = WithElementRef<HTMLButtonAttributes> &
		WithElementRef<HTMLAnchorAttributes> & {
			variant?: ButtonVariant;
			size?: ButtonSize;
		};
</script>

<script lang="ts">
	let {
		class: className,
		variant = "default",
		size = "default",
		ref = $bindable(null),
		href = undefined,
		type = "button",
		disabled,
		children,
		...restProps
	}: ButtonProps = $props();
</script>

{#if href}
	<a
		bind:this={ref}
		data-slot="button"
		class={cn(buttonVariants({ variant, size }), className)}
		href={disabled ? undefined : href}
		aria-disabled={disabled}
		role={disabled ? "link" : undefined}
		tabindex={disabled ? -1 : undefined}
		{...restProps}
	>
		{@render children?.()}
	</a>
{:else}
	<button
		bind:this={ref}
		data-slot="button"
		class={cn(buttonVariants({ variant, size }), className)}
		{type}
		{disabled}
		{...restProps}
	>
		{@render children?.()}
	</button>
{/if}
