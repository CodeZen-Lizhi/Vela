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
                /** 请求尝试卡统一维护状态色、分区表头和选中反馈；完整执行错误优先于 HTTP 状态。 */
                attempt: "overflow-hidden rounded-[13px] border-border bg-card p-0 hover:bg-muted data-[result=success]:border-positive/40 data-[result=failed]:border-destructive/40 data-[result=local]:border-caution/40 data-[result=pending]:border-primary/40 data-[result=success]:[&_.record-attempt-head]:bg-positive/5 data-[result=success]:[&_.record-attempt-head]:text-positive data-[result=failed]:[&_.record-attempt-head]:bg-destructive/5 data-[result=failed]:[&_.record-attempt-head]:text-destructive data-[result=local]:[&_.record-attempt-head]:bg-caution/5 data-[result=local]:[&_.record-attempt-head]:text-caution data-[result=pending]:[&_.record-attempt-head]:bg-primary/5 data-[result=pending]:[&_.record-attempt-head]:text-primary data-[selected=true]:ring-2 data-[selected=true]:ring-primary/30",
				/** 实时开关以主题色文字和边框强调启用状态。 */
				live: "border-primary/30 bg-background text-primary hover:bg-accent",
				/** 数值详情入口保持正文色，悬停与展开时使用主题强调色。 */
                metric: "text-foreground hover:text-primary aria-expanded:text-primary active:translate-y-0",
                /** 趋势图的透明命中区保留鼠标反馈与键盘焦点。 */
                plot: "rounded-sm border-0 bg-transparent hover:bg-primary/10 focus-visible:bg-primary/10 active:translate-y-0",
                /** 年度热力图颜色仅表达真实数据强度，未记录日期降低对比。 */
                heatmap: "rounded-[3px] border-0 bg-[color-mix(in_srgb,var(--primary)_var(--heat-strength,0%),var(--muted))] hover:ring-1 hover:ring-primary active:translate-y-0 disabled:opacity-45",
                ghost: "hover:bg-muted hover:text-foreground dark:hover:bg-muted/50 aria-expanded:bg-muted aria-expanded:text-foreground",
				destructive: "bg-destructive/10 hover:bg-destructive/20 focus-visible:ring-destructive/20 dark:focus-visible:ring-destructive/40 dark:bg-destructive/20 text-destructive focus-visible:border-destructive/40 dark:hover:bg-destructive/30",
				link: "text-primary underline-offset-4 hover:underline underline",
				/** 无底色的次要文本操作，悬停时强调可点击性。 */
				text: "text-muted-foreground hover:text-primary underline-offset-4 hover:underline",
			},
			size: {
				/** 执行路径卡片由内容分区控制间距，表头与语义边框贴合。 */
				attempt: "h-auto min-h-[88px] gap-0 p-0 whitespace-normal",
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
                /** 图表命中区跟随绘图区布局，不引入控件固定尺寸。 */
                plot: "h-full min-w-0 flex-1 p-0",
                /** 年历以13px方格保持与认可原型一致的信息密度。 */
                heatmap: "h-[13px] min-w-3 p-0",
                default: "h-9 gap-2 px-3.5 has-data-[icon=inline-end]:pr-4 has-data-[icon=inline-start]:pl-4",
				xs: "h-7 gap-1 px-3 has-data-[icon=inline-end]:pr-2 has-data-[icon=inline-start]:pl-2 [&_svg:not([class*='size-'])]:size-3",
				sm: "h-9 gap-2 px-3.5 has-data-[icon=inline-end]:pr-3 has-data-[icon=inline-start]:pl-3",
				lg: "h-11 gap-1.5 px-8 has-data-[icon=inline-end]:pr-5 has-data-[icon=inline-start]:pl-5",
				icon: "size-10",
				"icon-xs": "size-7 [&_svg:not([class*='size-'])]:size-3",
				"icon-sm": "size-9",
				"icon-lg": "size-11",
				/** 表格行操作按列宽设置 30～36px 点击区，避免统一尺寸挤压不同列。 */
				"row-action": "size-[var(--row-action-size,2rem)] [&_svg:not([class*='size-'])]:size-3.5",
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
