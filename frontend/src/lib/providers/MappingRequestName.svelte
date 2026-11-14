<script lang="ts">
  import { tick } from "svelte";
  import { Command as CommandPrimitive } from "bits-ui";
  import { Box, ChevronDown, CircleAlert, LoaderCircle, Plus } from "@lucide/svelte";
  import * as Command from "$lib/components/ui/command";
  import * as InputGroup from "$lib/components/ui/input-group";
  import * as Popover from "$lib/components/ui/popover";
  import * as Alert from "$lib/components/ui/alert";
  import { Button } from "$lib/components/ui/button";

  /** 映射请求名选择器：默认搜索真实上游候选，自定义名称通过同一浮层显式确认。 */
  let { value = "", names, excluded, disabled = false, loading = false, queryError = "", warning = "", label, onselect, onretry }: {
    /** 当前请求名；空值表示添加入口。 */
    value?: string;
    /** 当前供应商查询得到的上游名称，刷新失败时由父级保留上次成功快照。 */
    names: string[];
    /** 同一模型已有名称；编辑行允许保持自身当前名称。 */
    excluded: string[];
    /** 禁止操作、上游读取状态及脱敏提示，不影响已有映射。 */
    disabled?: boolean; loading?: boolean; queryError?: string; warning?: string;
    /** 当前统一模型或映射行的可访问名称。 */
    label: string;
    /** 父级更新草稿，失败时抛错并保留输入供修正。 */
    onselect: (name: string) => void | Promise<void>;
    /** 重试上游查询，不保存草稿。 */
    onretry: () => void;
  } = $props();
  /** 浮层、输入与局部错误属于本控件，不持久化。 */
  let open = $state(false), query = $state(""), failure = $state(""), saving = $state(false);
  /** 浮层锚点和输入焦点，保证弹窗滚动时定位一致。 */
  let anchor = $state<HTMLDivElement | null>(null), input = $state<HTMLInputElement | null>(null), command = $state<HTMLDivElement | null>(null);
  const text = $derived(query.trim());
  const options = $derived(names.filter((name) => !excluded.includes(name) && name.toLowerCase().includes(text.toLowerCase())).slice(0, 50));
  const canCreate = $derived(text.length > 0 && text !== value && !excluded.includes(text) && !names.includes(text));

  $effect(() => { if (!open) { query = value; failure = ""; } });

  /** 选择或手动添加均只更新父级草稿；保留重复名称和提交失败的就近提示。 */
  async function select(name: string): Promise<void> {
    if (disabled || saving) return;
    name = name.trim();
    if (!name) { failure = "上游请求名不能为空"; return; }
    if (excluded.includes(name)) { failure = "此请求名已添加"; return; }
    saving = true; failure = "";
    try {
      await onselect(name);
      open = false;
      await tick();
      input?.focus({ preventScroll: true });
      open = false;
    } catch (cause) { failure = cause instanceof Error ? cause.message : String(cause); }
    finally { saving = false; }
  }
</script>

<div class="min-w-0 flex-1">
  <Command.Root bind:ref={command} shouldFilter={false} label={label} class="h-auto overflow-visible">
    <Popover.Root bind:open>
      <InputGroup.Root bind:ref={anchor}>
        <CommandPrimitive.Input bind:value={query}>
          {#snippet child({ props })}
            <InputGroup.Input {...props} bind:ref={input} bind:value={query} aria-label={label} aria-expanded={open}
              aria-invalid={!!failure} disabled={disabled || saving} placeholder="添加映射：搜索上游模型，或输入请求名"
              onfocus={() => open = true} oninput={() => { open = true; failure = ""; }}
              onkeydown={(event) => {
                if (event.key === "Escape" && open) { event.preventDefault(); event.stopPropagation(); open = false; }
                if (event.key === "Enter" && text && !options.length && !canCreate) { event.preventDefault(); void select(text); }
              }} />
          {/snippet}
        </CommandPrimitive.Input>
        {#if !value}<InputGroup.Addon><Plus /></InputGroup.Addon>{/if}
        <InputGroup.Addon align="inline-end">
          <Popover.Trigger>
            {#snippet child({ props })}<InputGroup.Button {...props} size="icon-xs" disabled={disabled || saving} aria-label={`选择 ${label}`}><ChevronDown /></InputGroup.Button>{/snippet}
          </Popover.Trigger>
        </InputGroup.Addon>
      </InputGroup.Root>
      <!-- 保留在 Command 的 DOM 内以接收方向键，并以弹窗为碰撞边界避免滚动区裁切候选。 -->
      <Popover.Content align="start" strategy="fixed" customAnchor={anchor} class="w-[min(410px,var(--bits-popover-anchor-width))] max-w-[calc(100vw-48px)] gap-1 p-1"
        collisionBoundary={anchor?.closest<HTMLElement>('[role="dialog"]') ?? undefined} collisionPadding={16}
        portalProps={{ to: command ?? undefined }}
        trapFocus={false} onOpenAutoFocus={(event) => event.preventDefault()} onCloseAutoFocus={(event) => event.preventDefault()}
        onInteractOutside={(event) => { if (event.target instanceof Node && anchor?.contains(event.target)) event.preventDefault(); }}>
        <Command.List>
          <Command.Group heading="上游模型">
            {#each options as name (name)}
              <Command.Item value={name} onSelect={() => void select(name)} disabled={disabled || saving}><Box /><code class="min-w-0 break-all text-xs">{name}</code></Command.Item>
            {/each}
          </Command.Group>
          {#if loading}<p class="flex items-center gap-2 px-3 py-2 text-xs text-muted-foreground"><LoaderCircle class="size-3 animate-spin" />正在获取上游模型…</p>
          {:else if !options.length}<p class="px-3 py-2 text-xs text-muted-foreground">{text ? "没有匹配的上游模型" : "暂无上游候选，可输入请求名手动添加"}</p>{/if}
          {#if queryError || warning}
            <div class="flex min-w-0 items-start gap-2 px-3 py-2">
              <Alert.Root variant="inline-error"><CircleAlert /><Alert.Description>{queryError || warning}</Alert.Description></Alert.Root>
              <Button variant="text" size="inline" onclick={onretry} disabled={loading}>重试</Button>
            </div>
          {/if}
          <Command.Separator />
          <Command.Group>
            {#if canCreate}<Command.Item value={`custom:${text}`} onSelect={() => void select(text)} disabled={disabled || saving}><Plus /><span class="text-xs">手动添加</span><code class="min-w-0 break-all text-xs">{text}</code></Command.Item>
            {:else}<p class="px-3 py-2 text-xs text-muted-foreground">也可输入自定义请求名后直接添加</p>{/if}
          </Command.Group>
        </Command.List>
      </Popover.Content>
    </Popover.Root>
  </Command.Root>
  {#if failure}<Alert.Root variant="inline-error" class="mt-1"><Alert.Description>{failure}</Alert.Description></Alert.Root>{/if}
</div>
