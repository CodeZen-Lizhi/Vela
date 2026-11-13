<script lang="ts">
  import {
    Type,
    Eye,
    Wrench,
    BrainCircuit,
    FileText,
    AudioLines,
    Video,
    Braces,
  } from "@lucide/svelte";
  import { Badge } from "$lib/components/ui/badge";
  /** 只展示来源明确记录的能力，颜色与原型中的语义对应。 */
  let { values }: { values: string[] } = $props();
  /** 同一能力在列表与详情中共用图标、名称和强调色。 */
  const presentation = {
    text: { label: "文本", icon: Type, tone: "neutral" },
    image: { label: "视觉", icon: Eye, tone: "info" },
    tools: { label: "工具", icon: Wrench, tone: "warning" },
    reasoning: { label: "推理", icon: BrainCircuit, tone: "parameter" },
    pdf: { label: "文档", icon: FileText, tone: "neutral" },
    audio: { label: "音频", icon: AudioLines, tone: "info" },
    video: { label: "视频", icon: Video, tone: "parameter" },
    structured: { label: "结构化", icon: Braces, tone: "positive" },
  };
</script>

<div class="model-capabilities">
  {#each [...new Set(values)] as value}
    {@const item = presentation[value as keyof typeof presentation]}
    {#if item}<Badge variant={item.tone as "neutral" | "info" | "warning" | "parameter" | "positive"}
        ><item.icon class="size-3" aria-hidden="true" />{item.label}</Badge
      >{/if}
  {/each}
</div>
