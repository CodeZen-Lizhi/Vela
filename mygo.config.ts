import { existsSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { defineConfig } from "mygo-cli";
import pkg from "./package.json" with { type: "json" };

// 更新私钥只在本机构建或 CI 中读取，公钥随源码和应用固定。
const signingKey = process.env.VELA_UPDATE_PRIVATE_KEY_FILE ?? join(homedir(), "Library/Application Support/Vela/update-signing/mygo-update.key");

export default defineConfig({
  name: "Vela",
  slug: "vela",
  identifier: "com.codezenlizhi.vela",
  version: pkg.version,
  license: pkg.license,
  copyright: "© 2026 CodeZen",
  main: "./cmd/vela",
  devUrl: "http://localhost:5173",
  devCommand: "npm run dev:web",
  buildCommand: "npm run build:web",
  frontendDist: "frontend/dist",
  bindings: "frontend/src/lib/generated/mygo.ts",
  out: "build",
  // 发布元数据在构建前生成，随许可一起进入已签名 App 和 DMG。
  resources: ["LICENSE", "THIRD_PARTY_NOTICES.txt", ...(process.env.VELA_RELEASE_METADATA ? [process.env.VELA_RELEASE_METADATA] : [])],
  updates: {
    publicKey: readFileSync(new URL("./config/update-public-key.txt", import.meta.url), "utf8").trim(),
    github: "CodeZen-Lizhi/Vela",
    privateKey: existsSync(signingKey) ? signingKey : undefined,
    deltas: 0,
  },
  macos: {
    minimumSystemVersion: "14.0",
  },
});
