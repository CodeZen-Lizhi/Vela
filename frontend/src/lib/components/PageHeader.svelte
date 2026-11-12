<script lang="ts">
  import type { Snippet } from "svelte";

  /** 统一模块页头；操作区随窗口宽度换行，不参与列表滚动。 */
  let { title, description, actions }: {
    /** 当前模块名称。 */
    title: string;
    /** 标题下的一行用途说明。 */
    description: string;
    /** 当前模块已有的主要操作。 */
    actions?: Snippet;
  } = $props();
</script>

<header class="page-heading">
  <div class="page-heading-copy">
    <h1>{title}</h1>
    <p>{description}</p>
  </div>
  {#if actions}<div class="page-heading-actions">{@render actions()}</div>{/if}
</header>
