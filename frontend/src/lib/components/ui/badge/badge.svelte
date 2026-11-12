<script lang="ts" module>
	import { type VariantProps, tv } from "tailwind-variants";

	export const badgeVariants = tv({
		base: "gap-1.5 rounded-none border-0 bg-transparent px-0 py-0 text-[0.625rem] font-semibold uppercase tracking-widest transition-colors has-data-[icon=inline-end]:pr-0 has-data-[icon=inline-start]:pl-0 [&>svg]:size-3! group/badge inline-flex w-fit shrink-0 items-center justify-center overflow-hidden whitespace-nowrap focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 [&>svg]:pointer-events-none",
		variants: {
			variant: {
				/** 紧凑圆角标签按语义着色：info 主信息、positive 计量、neutral 中性、negative 失败。 */
				info: "h-5 rounded-md border border-primary/15 bg-primary/5 px-1.5 py-0 text-[11px] font-medium normal-case tracking-normal text-primary",
				positive: "h-5 rounded-md border border-positive/20 bg-positive/5 px-1.5 py-0 text-[11px] font-medium normal-case tracking-normal text-positive",
				neutral: "h-5 rounded-md border border-border bg-muted/40 px-1.5 py-0 text-[11px] font-medium normal-case tracking-normal text-muted-foreground",
				negative: "h-5 rounded-md border border-destructive/15 bg-destructive/5 px-1.5 py-0 text-[11px] font-medium normal-case tracking-normal text-destructive",
				/** 进行中以呼吸文字与状态点强调，减少动态效果偏好下保留静态蓝色。 */
				processing: "h-5 gap-1 rounded-md border border-primary/30 bg-primary/10 px-1.5 py-0 text-[11px] font-semibold normal-case tracking-normal text-primary before:size-1.5 before:shrink-0 before:rounded-full before:bg-current before:content-[''] motion-safe:animate-[vela-status-breathe_1.8s_ease-in-out_infinite]",
				/** 本地拦截或未发上游使用暖色，与失败及成功分别识别。 */
				warning: "h-5 rounded-md border border-caution/20 bg-caution/5 px-1.5 py-0 text-[11px] font-medium normal-case tracking-normal text-caution",
				/** 传输元信息使用无底色、无描边的次级标签，流式/WS 保留语义色，标准使用中性色。 */
				transport: "h-4 gap-1 rounded-md px-1.5 py-0 text-[11px] font-normal normal-case tracking-normal text-positive",
				metadata: "h-4 gap-1 rounded-md px-1.5 py-0 text-[11px] font-normal normal-case tracking-normal text-muted-foreground",
				/** 模型强度与层级使用更小的空心标签，不转换原始大小写。 */
				parameter: "rounded-full border border-primary/20 px-1.5 py-0 text-[10px] font-medium normal-case tracking-normal text-primary",
				default: "text-foreground [a]:hover:text-foreground/70",
				secondary: "text-muted-foreground [a]:hover:text-foreground",
				destructive: "text-destructive [a]:hover:text-destructive/70 focus-visible:ring-destructive/20 dark:focus-visible:ring-destructive/40",
				outline: "text-foreground [a]:hover:text-foreground/70",
				ghost: "text-muted-foreground hover:text-foreground",
				link: "text-foreground underline-offset-4 hover:underline",
				/** 配置状态只表达启停，不代表连通性或健康度。 */
				enabled: "rounded-full bg-accent px-2.5 py-1 text-xs font-medium normal-case tracking-normal text-primary",
				disabled: "rounded-full bg-muted px-2.5 py-1 text-xs font-medium normal-case tracking-normal text-muted-foreground",
			},
		},
		defaultVariants: {
			variant: "default",
		},
	});

	export type BadgeVariant = VariantProps<typeof badgeVariants>["variant"];
</script>

<script lang="ts">
	import { cn, type WithElementRef } from "$lib/utils.js";
	import type { HTMLAnchorAttributes } from "svelte/elements";

	let {
		ref = $bindable(null),
		href,
		class: className,
		variant = "default",
		children,
		...restProps
	}: WithElementRef<HTMLAnchorAttributes> & {
		variant?: BadgeVariant;
	} = $props();
</script>

<svelte:element
	this={href ? "a" : "span"}
	bind:this={ref}
	data-slot="badge"
	{href}
	class={cn(badgeVariants({ variant }), className)}
	{...restProps}
>
	{@render children?.()}
</svelte:element>
