import { defineConfig } from "mygo-cli";
import pkg from "./package.json" with { type: "json" };

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
  macos: {
    minimumSystemVersion: "14.0",
  },
});
