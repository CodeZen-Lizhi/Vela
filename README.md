# Vela

**本地优先的 AI 网关桌面客户端。**

> Many models. One course.

Vela 是 Aether 重构后的新项目。它把多家 AI 供应商、不同协议、模型映射、调度、额度和请求诊断收束到一个本机入口，桌面宿主使用 MyGo，业务核心使用 Go，界面使用 Svelte。

当前仓库是重构起点，只包含可运行的桌面壳、类型化前后端绑定和基础界面。供应商、模型、调度、网关业务、用量与诊断能力尚未实现。

## 技术栈

- Go 1.27.3
- MyGo 0.8.3，系统 WebView 模式
- Svelte 5
- TypeScript
- Vite 8
- Tailwind CSS 4
- shadcn-svelte
- SQLite 将在后续业务模块中接入

## 目录

```text
.
├── cmd/vela/                 MyGo 桌面应用入口
├── internal/app/             最小类型化应用服务
├── internal/buildinfo/       构建信息
├── frontend/                 Svelte 前端
├── resources/                桌面应用资源
├── docs/adr/                 架构决策记录
└── mygo.config.ts            MyGo 开发与打包配置
```

## 开发

需要 Go 1.27.3+ 和 Node.js 22.12+。

```bash
npm install
npm run check
npm run dev
```

只在浏览器预览界面时运行：

```bash
npm run dev:web
```

浏览器模式不包含 MyGo 运行时，页面会明确显示为浏览器预览。

## 验证

```bash
go test ./...
npm run check
npm run build
```

桌面应用构建产物位于 `build/`。

## 当前边界

本次只完成框架初始化，不提供 AI 网关业务接口、数据库、供应商管理、模型路由或访问凭据。上述模块应在后续任务中分别实现。
