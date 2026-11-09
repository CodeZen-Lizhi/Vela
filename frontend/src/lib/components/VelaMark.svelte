<script lang="ts">
  /** Vela 品牌 V 标识；颜色继承外层文本色，尺寸由 class 控制。 */
  let { class: className = "" }: { class?: string } = $props();
</script>

<svg viewBox="0 0 1024 1024" class={className} aria-hidden="true">
  <path
    d="M143 270H309C383 278 441 344 505 470C569 596 605 707 626 742C620 751 588 777 555 793C522 809 505 799 505 799C474 782 420 726 389 732C359 651 243 403 143 270Z"
    fill="currentColor"
  />
  <path
    d="M880 269H718C686 272 657 288 627 352C595 420 548 529 524 580C539 616 571 679 624 741C640 730 660 706 675 678C675 678 696 623 717 568C738 513 781 384 880 269Z"
    fill="currentColor"
  />
</svg>
