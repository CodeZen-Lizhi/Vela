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
    image: { label: "视觉", icon: Eye, tone: "vision" },
    tools: { label: "工具", icon: Wrench, tone: "tools" },
    reasoning: { label: "推理", icon: BrainCircuit, tone: "reasoning" },
    pdf: { label: "文档", icon: FileText, tone: "neutral" },
    audio: { label: "音频", icon: AudioLines, tone: "vision" },
    video: { label: "视频", icon: Video, tone: "reasoning" },
    structured: { label: "结构化", icon: Braces, tone: "structured" },
  };
</script>

<div class="model-capabilities">
  {#each [...new Set(values)] as value}
    {@const item = presentation[value as keyof typeof presentation]}
    {#if item}<Badge variant="outline" class={`capability-pill ${item.tone}`}
        ><item.icon class="size-3" aria-hidden="true" />{item.label}</Badge
      >{/if}
  {/each}
  {#if !values.length}<span class="text-xs text-muted-foreground">能力未知</span
    >{/if}
</div>
