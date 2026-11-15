import { pretty } from "./display";

/** 一条真实消息或非文本内容；角色、正文均来自已脱敏快照。 */
export interface MessageRow {
  /** 角色的可读名称；工具、思考与媒体保留自身类型。 */
  role: string;
  /** 正文原有换行不压缩，不从其他请求补齐。 */
  text: string;
}

/** JSON 高亮片段只作为文本渲染，禁止将快照插入 HTML。 */
export interface JsonToken {
  /** 原始字符与语义类别，空类别表示标点或空白。 */
  text: string;
  kind: string;
}

/** 对标准 JSON 缩进；SSE 保留事件顺序，逐帧格式化，不伪造单个响应对象。 */
export function formatSnapshot(text: string): string {
  if (!text.trim()) return "";
  if (!/^data:/m.test(text)) return pretty(text);
  return text.replace(/^data: *(.+)$/gm, (_, value: string) => `data: ${pretty(value)}`);
}

/** 高亮基于已格式化文本切片，字符串内部的数字和转义保持原样。 */
export function jsonTokens(text: string): JsonToken[] {
  const tokens: JsonToken[] = [];
  const pattern = /"(?:[^"\\]|\\.)*"\s*(?=:)|"(?:[^"\\]|\\.)*"|\b(?:true|false|null)\b|-?\b\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b/g;
  let offset = 0;
  for (const match of text.matchAll(pattern)) {
    if (match.index > offset) tokens.push({ text: text.slice(offset, match.index), kind: "" });
    const value = match[0];
    tokens.push({ text: value, kind: value.startsWith('"') ? text[match.index + value.length] === ":" ? "key" : "string" : "literal" });
    offset = match.index + value.length;
  }
  if (offset < text.length) tokens.push({ text: text.slice(offset), kind: "" });
  return tokens;
}

/** 协议对象只在本次快照解析边界收窄。 */
function object(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

/** 标准角色翻译为中文；未知角色原样保留，不擅自标为用户。 */
function roleName(role: string): string {
  return ({ user: "用户", assistant: "助手", model: "助手", system: "系统", developer: "开发者", tool: "工具" } as Record<string, string>)[role] ?? role;
}

/** 提取 JSON 消息、工具和媒体元信息；未知对象保留原始 JSON 作为后备。 */
function readMessages(value: unknown, role: string): MessageRow[] {
  if (typeof value === "string") return value ? [{ role: roleName(role), text: value }] : [];
  if (Array.isArray(value)) return value.flatMap(item => readMessages(item, role));
  const item = object(value);
  const who = typeof item.role === "string" ? item.role : role;
  const rows: MessageRow[] = [];
  if (item.instructions != null) rows.push(...readMessages(item.instructions, "system"));
  if (typeof item.text === "string") rows.push(...readMessages(item.text, who));
  if (typeof item.output_text === "string") rows.push(...readMessages(item.output_text, who));
  for (const key of ["messages", "input", "output", "content", "contents", "parts", "choices", "message", "delta", "candidates", "response"]) {
    if (item[key] != null && typeof item[key] !== "number") rows.push(...readMessages(item[key], who));
  }
  for (const [key, title] of Object.entries({ tool_calls: "工具调用", function_call: "工具调用", functionCall: "工具调用", functionResponse: "工具结果", thinking: "思考", reasoning_content: "思考", error: "错误" })) {
    if (item[key] != null) rows.push({ role: title, text: typeof item[key] === "string" ? item[key] : JSON.stringify(item[key], null, 2) });
  }
  if (["function_call", "function_call_output", "tool_use", "tool_result", "image", "image_url", "input_image", "input_audio", "audio", "video", "file"].includes(String(item.type)) || item.inlineData || item.fileData) {
    return [{ role: String(item.type).includes("tool") || String(item.type).includes("function") ? "工具" : "媒体信息", text: JSON.stringify(item, null, 2) }];
  }
  if (typeof item.role === "string") {
    const messages: MessageRow[] = [];
    for (const row of rows) {
      const previous = messages.at(-1);
      if (previous?.role === row.role) previous.text += `\n\n${row.text}`;
      else messages.push({ ...row });
    }
    return messages;
  }
  return rows;
}

/** 流式帧按协议归并；优先完整终态，未结束时仅合并已收到的增量。 */
function streamMessages(text: string): MessageRow[] {
  const frames: Record<string, unknown>[] = [];
  for (const event of text.split(/\r?\n\r?\n/)) {
    const data = event.split(/\r?\n/).filter(line => line.startsWith("data:")).map(line => line.slice(5).trimStart()).join("\n");
    if (!data || data === "[DONE]") continue;
    try { frames.push(object(JSON.parse(data))); } catch { /* 截断末帧不冒充完整消息，原始视图仍保留。 */ }
  }
  const terminal = [...frames].reverse().find(frame => ["response.completed", "response.incomplete", "response.failed"].includes(String(frame.type)));
  const terminalRows = terminal ? readMessages(terminal, "assistant") : [];
  if (terminalRows.some(row => row.role !== "错误")) return terminalRows;
  const rows = new Map<string, MessageRow>();
  /** 同一流片段按输出身份拼接，换行和空格不丢失。 */
  const append = (id: string, role: string, value: unknown): void => {
    if (typeof value !== "string" || !value) return;
    const row = rows.get(id) ?? { role, text: "" };
    row.text += value;
    rows.set(id, row);
  };
  for (const frame of frames) {
    const type = String(frame.type ?? "");
    if (type === "response.output_text.delta" || type === "response.reasoning_summary_text.delta" || type === "response.function_call_arguments.delta") {
      append(`${type}:${frame.output_index ?? 0}:${frame.content_index ?? 0}`, type.includes("reasoning") ? "思考" : type.includes("arguments") ? "工具调用" : "助手", frame.delta);
    } else if (type === "content_block_start") {
      const block = object(frame.content_block);
      append(`block:${frame.index}`, block.type === "tool_use" ? "工具调用" : block.type === "thinking" ? "思考" : "助手", block.text ?? block.thinking ?? (block.type === "tool_use" ? `${String(block.name)}\n` : ""));
    } else if (type === "content_block_delta") {
      const delta = object(frame.delta);
      append(`block:${frame.index}`, delta.type === "thinking_delta" ? "思考" : delta.type === "input_json_delta" ? "工具调用" : "助手", delta.text ?? delta.thinking ?? delta.partial_json);
    } else if (Array.isArray(frame.choices)) {
      for (const choice of frame.choices) {
        const item = object(choice), delta = object(item.delta);
        append(`chat:${item.index ?? 0}`, "助手", delta.content ?? item.text);
        append(`reasoning:${item.index ?? 0}`, "思考", delta.reasoning_content);
        if (Array.isArray(delta.tool_calls)) for (const call of delta.tool_calls) {
          const tool = object(call), fn = object(tool.function);
          append(`tool:${item.index ?? 0}:${tool.index ?? 0}`, "工具调用", `${typeof fn.name === "string" ? fn.name + "\n" : ""}${typeof fn.arguments === "string" ? fn.arguments : ""}`);
        }
      }
    } else if (Array.isArray(frame.candidates)) {
      for (const candidate of frame.candidates) {
        const item = object(candidate);
        for (const [index, row] of readMessages(item.content, "assistant").entries()) append(`gemini:${item.index ?? 0}:${index}`, row.role, row.text);
      }
    } else if (frame.error && frame !== terminal) {
      for (const row of readMessages(frame, "assistant")) append(`error:${rows.size}`, row.role, row.text);
    }
  }
  return [...rows.values(), ...terminalRows];
}

/** 仅解析本次输入/输出，不补造会话历史；SSE 与普通 JSON 共用显示结构。 */
export function conversationMessages(text: string, direction: "request" | "response"): MessageRow[] {
  try { return readMessages(JSON.parse(text), direction === "request" ? "user" : "assistant"); }
  catch { return streamMessages(text); }
}

/** 复制可读会话时保留角色、消息顺序、换行及代码围栏，不混入页面元信息。 */
export function conversationCopy(rows: MessageRow[]): string {
  return rows.map(row => `${row.role}：\n${row.text}`).join("\n\n");
}

/** 正文仅拆分围栏代码与普通文本，避免把外部内容当作 HTML 执行。 */
export function messageBlocks(text: string): { code: boolean; text: string }[] {
  return text.split(/```[^\n]*\n([\s\S]*?)(?:```|$)/g).map((part, index) => ({ code: index % 2 === 1, text: index % 2 === 1 ? part : part.trim() })).filter(part => part.text.trim());
}
