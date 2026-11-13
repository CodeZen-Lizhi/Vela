<script lang="ts">
	import { Tooltip as TooltipPrimitive } from "bits-ui";

	/** 首次300ms；关闭后500ms内进入另一提示时跳过等待。 */
	let { delayDuration = 300, skipDelayDuration = 500, ...restProps }: TooltipPrimitive.ProviderProps = $props();
</script>

<TooltipPrimitive.Provider {delayDuration} {skipDelayDuration} {...restProps} />
