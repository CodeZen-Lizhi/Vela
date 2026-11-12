/** 代理类型与显示名称，沿用后端支持的协议；socks5h 保留远程 DNS 配置。 */
export const proxyProtocols: Record<string, string> = {
  http: "HTTP",
  https: "HTTPS",
  socks5: "SOCKS5",
  socks5h: "SOCKS5 · 远程 DNS",
};

/** 将代理 URL 展开为表单字段；密码仅用于当前编辑及提交，不写入页面摘要。 */
export interface ProxyAddress {
  /** 协议标识，与后端 URL scheme 一致。 */
  protocol: string;
  /** 域名、IPv4 或带方括号的 IPv6 主机。 */
  host: string;
  /** TCP 端口的输入草稿，空字符串表示尚未填写。 */
  port: string;
  /** 可选认证用户名，保存时按 URL userinfo 编码。 */
  username: string;
  /** 可选认证密码，保存时按 URL userinfo 编码。 */
  password: string;
}

/** 读取已有地址并解码认证信息；空地址给出常用的本机代理起始值。 */
export function parseProxyAddress(raw: string): ProxyAddress {
  if (!raw) return { protocol: "http", host: "127.0.0.1", port: "7890", username: "", password: "" };
  const url = new URL(raw);
  const protocol = url.protocol.slice(0, -1);
  if (!Object.hasOwn(proxyProtocols, protocol)) throw new Error("不支持的代理类型");
  return {
    protocol,
    host: url.hostname,
    port: url.port || (protocol === "http" ? "80" : protocol === "https" ? "443" : "1080"),
    username: decodeURIComponent(url.username),
    password: decodeURIComponent(url.password),
  };
}

/** 校验拆分字段并组装 URL；保留旧地址的附加路径，避免界面调整丢弃已保存内容。 */
export function formatProxyAddress(value: ProxyAddress, original = ""): string {
  const host = value.host.trim();
  if (!host || /[\s/@?#]/.test(host)) throw new Error("请输入主机名或 IP 地址，不需要填写协议和端口");
  const port = value.port.trim();
  if (!/^\d+$/.test(port) || Number(port) < 1 || Number(port) > 65535) throw new Error("请输入 1–65535 之间的整数端口");
  if (!Object.hasOwn(proxyProtocols, value.protocol)) throw new Error("请选择支持的代理类型");
  if (value.password && !value.username) throw new Error("填写代理密码时也需要填写用户名");
  const authority = host.includes(":") && !host.startsWith("[") ? `[${host}]` : host;
  let url: URL;
  try {
    url = new URL(`${value.protocol}://${authority}:${port}`);
  } catch {
    throw new Error("请输入有效的主机名或 IP 地址");
  }
  if (original) {
    const previous = new URL(original);
    url.pathname = previous.pathname;
    url.search = previous.search;
    url.hash = previous.hash;
  }
  url.username = encodeURIComponent(value.username);
  url.password = encodeURIComponent(value.password);
  return url.toString();
}
