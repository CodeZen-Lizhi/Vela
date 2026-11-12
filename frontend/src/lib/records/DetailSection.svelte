<script lang="ts">
  import type { Snippet } from "svelte";
  import * as Accordion from "$lib/components/ui/accordion";

  /** 旧版详情中的次级折叠区：默认收起，标题与说明保持同一入口。 */
  let { title, description = "", children }: {
    /** 区块标题及其内容范围说明。 */ title: string;
    description?: string;
    /** 折叠区域内的真实详情。 */ children: Snippet;
  } = $props();
</script>

<Accordion.Root type="multiple" class="record-detail-section">
  <Accordion.Item value="content">
    <Accordion.Trigger variant="section"><span class="record-section-heading">{title}{#if description}<small>{description}</small>{/if}</span></Accordion.Trigger>
    <Accordion.Content><div class="record-section-body">{@render children()}</div></Accordion.Content>
  </Accordion.Item>
</Accordion.Root>
