import assert from "node:assert/strict";
import { test } from "node:test";
import { emptyQuery, matches, transports } from "./display.ts";

/** WebSocket 使用独立客户端传输筛选；旧记录继续按原 stream 标记兼容。 */
test("WebSocket 不冒充 HTTP 流式，旧记录传输仍兼容", () => {
  const query = emptyQuery();
  query.transports = ["websocket"];
  assert.equal(matches({ stream: true, transport: "websocket" }, query), true);
  assert.equal(matches({ stream: true }, query), false);
  query.transports = ["stream"];
  assert.equal(matches({ stream: true, transport: "websocket" }, query), false);
  assert.equal(matches({ stream: true }, query), true);
  assert.ok(transports.some((value) => value.value === "websocket" && value.label === "WebSocket"));
});
