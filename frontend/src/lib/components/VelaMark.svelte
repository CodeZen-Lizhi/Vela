<script lang="ts">
  /** Vela 品牌 V 标识；颜色继承外层文本色，尺寸由 class 控制。 */
  let { class: className = "" }: { class?: string } = $props();
</script>

<svg viewBox="0 0 1024 1024" class={className} aria-hidden="true">
  <path
    d="M512 510 C473 423 420 270 330 270 H160 C244 382 320 553 384 683 C423 762 465 798 512 798 C559 798 601 762 640 683 C704 553 780 382 864 270 H694 C604 270 551 423 512 510 Z"
    fill="currentColor"
  />
</svg>
