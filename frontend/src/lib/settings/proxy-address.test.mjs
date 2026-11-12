import assert from "node:assert/strict";
import { test } from "node:test";
import { parseProxyAddress, formatProxyAddress } from "./proxy-address.ts";

/** 真实 URL 编解码保留各协议、IPv6 与认证中的特殊字符。 */
test("代理字段可保存并重读协议、IPv6 和特殊认证字符", () => {
  for (const protocol of ["http", "https", "socks5", "socks5h"]) {
    const draft = { protocol, host: "[::1]", port: "7890", username: "user@%:/中文", password: "p%@:?#/ word" };
    const result = formatProxyAddress(draft);
    assert.deepEqual(parseProxyAddress(result), draft);
    assert.equal(new URL(result).hostname, "[::1]");
  }
  assert.equal(parseProxyAddress("https://proxy.example.com").port, "443");
  assert.equal(parseProxyAddress("http://localhost").port, "80");
  assert.equal(parseProxyAddress("socks5://localhost").port, "1080");
});

/** 非法主机、端口或不完整认证不会形成可提交的地址。 */
test("代理字段拒绝错误输入并保留旧地址附加内容", () => {
  const draft = parseProxyAddress("");
  for (const change of [{ host: "" }, { host: "http://localhost" }, { host: "localhost/path" }, { port: "0" }, { port: "65536" }, { port: "1.5" }, { protocol: "ftp" }, { password: "secret" }]) {
    assert.throws(() => formatProxyAddress({ ...draft, ...change }));
  }
  const original = "http://localhost:8080/path?option=1#keep";
  const url = new URL(formatProxyAddress({ ...parseProxyAddress(original), port: "7890" }, original));
  assert.equal(url.port, "7890");
  assert.equal(url.pathname + url.search + url.hash, "/path?option=1#keep");
});
