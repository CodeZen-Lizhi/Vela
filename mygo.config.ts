import { defineConfig } from "mygo-cli";

export default defineConfig({
  name: "Vela",
  slug: "vela",
  identifier: "com.codezenlizhi.vela",
  version: "0.1.0",
  copyright: "© 2026 CodeZen",
  main: "./cmd/vela",
  devUrl: "http://localhost:5173",
  devCommand: "npm run dev:web",
  buildCommand: "npm run build:web",
  frontendDist: "frontend/dist",
  bindings: "frontend/src/lib/generated/mygo.ts",
  out: "build",
  macos: {
    minimumSystemVersion: "14.0",
  },
});
