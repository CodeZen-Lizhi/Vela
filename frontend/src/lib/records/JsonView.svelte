<script lang="ts">
  import { jsonTokens } from "./exchange";
  /** 只读 JSON/事件快照：缩进由调用方提供，词法高亮不插入 HTML。 */
  let { text }: {
    /** 当前侧完整可见的已脱敏数据，也作为复制源。 */ text: string;
  } = $props();
  const tokens = $derived(jsonTokens(text));
</script>

<pre class="record-exchange-json"><code>{#each tokens as token}<span class:json-key={token.kind === "key"} class:json-string={token.kind === "string"} class:json-literal={token.kind === "literal"}>{token.text}</span>{/each}</code></pre>

<style>
  .json-key { color:var(--parameter); }
  .json-string { color:var(--positive); }
  .json-literal { color:var(--primary); }
</style>
