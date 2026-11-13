# 首个公开测试版交付记录

日期：2026-11-13。版本：`0.1.0-beta.1`。平台：macOS 14+、Apple Silicon（arm64）。

已完成发布准备的代码、文档及本地候选包，完成下述本机验证；尚未提交、push、创建 tag 或对外发布。未覆盖的系统和供应商组合仍待验证，不能宣称稳定版或全部首装场景通过。

## 实施范围

- 数据目录在打开凭据与数据库前取得系统文件锁，覆盖同目录及目录别名；普通重复启动复用 MyGo 聚焦能力。旧版不遵守新锁时仍须先退出，不能同时运行。
- HTTP/SSE 停止接入后先限时自然等待，超时取消连接，再等待已进入业务的 handler 收尾；Responses WebSocket / Realtime 等待取消后的业务收尾。账户刷新、验证及历史清理任务在共享数据库关闭前退出。
- 初始化失败先清理已打开资源，再显示包含原因及相关路径的原生弹窗；`vela.log` / `vela.previous.log` 保留本次、上次启动日志，文件权限 0600。
- 根 `package.json` 是唯一版本来源；生成 Go 版本、同步 workspace/锁文件，MyGo 与 Vite 读取同一版本。MIT 及 67 份第三方许可/声明全文随 App、DMG 和 ZIP 交付。
- `npm run release:prepare` 生成本地候选、源码状态指纹、构建信息、发布说明、manifest 和 SHA256SUMS。当前包记录未提交改动，不冒充已发布 tag 对应的干净提交。
- [README](../README.md) 和[版本说明](releases/0.1.0-beta.1.md) 覆盖安装、首次 Chat、协议路径、升级、应急文件、隐私和已知限制。既有需求增量已同步至桌面重构总方案 R55 / A37。

正文 7 天、明细/诊断 30 天及长期累计继续沿用原要求；配置导入导出、自动更新没有实现，完整备份/恢复和正文采集开关没有重新加入。

## 当前验证

运行环境为本机 macOS 27.2、arm64。测试上游为受控本机独立 HTTP/WS 服务；没有用付费远端服务替代或扩大本次验收。

| 范围 | 实际结果 | 证据与边界 |
| --- | --- | --- |
| Go 门禁 | 全量 651 项通过，`go vet ./...` 通过；后续 WS 收尾修正后网关定向普通/race 各 34 项、既有 WS/Realtime 定向 race 9 项通过 | 全量结果在 WS 最后修正前完成；修正后按影响范围补验，无新增依赖 |
| 前端与发布脚本 | Svelte 0 错误/0 警告；前端 Node 测试 10 项、发布脚本 5 项通过 | 包含真实 workspace dev 指定端口启动、版本注入、源码指纹和校验值；生产构建仍有既有大 chunk 提示 |
| 原生调试版 + IPC | 空库启动，通过真实 IPC 创建模型、供应商、Key 及关联，Chat/SSE 成功、401 拒绝、端口冲突保留原监听 | `.cache/public-beta/native-results.json` / `native-records.png`；初始端口用隔离 SQLite 预置，避免占用日常端口 |
| 同目录并发与 HTTP 退出 | 活跃长流期间第二个新实例不改原记录；SIGTERM 约 2.063 秒后退出，三条记录全部终态 | 同一调试版场景实测，非退出性能承诺；独立进程锁测试另覆盖目录别名、不同目录及进程退出后重新加锁 |
| WS 慢收尾 | Responses / Realtime × Close / Pause 四种组合，取消后收尾阻塞超过 2.2 秒仍等待；放行后真实 SQLite 记录终态且可恢复监听 | 原实现复现提前返回及 database is closed，修正后普通/race 通过；不把连接关闭当作落库完成 |
| 分发下载及包内容 | Ego 浏览器经本机 HTTP 下载 DMG，带 `com.apple.quarantine`，SHA256 与原包一致；只读挂载后复制到专用验收目录 | `.cache/public-beta/downloaded-vela.dmg`；App 内许可及元数据与外侧副本一致，签名完整性校验通过，未替换日常 `/Applications/Vela.app` |
| 候选包实际业务 | 从安装介质复制的生产 App 读取原配置，真实 Chat 返回 BETA_OK；记录落库、重启再调用通过 | `.cache/public-beta/candidate-results.json`；为真实生产二进制和本机上游，不是浏览器预览或 Mock RPC |
| 升级与原文件恢复 | 原 `0.1.0` App 使用隔离样例重启并调用；保全原文件后，候选包保留模型/供应商/Key/四条原记录及凭据文件哈希；恢复原文件副本后调用正常 | `upgrade-baseline.json` / `candidate-results.json`；只证明本次样例和版本组合，不承诺任意历史版本、跨机或整库兼容 |
| 最终原生 UI | 生产 App 显示 0.1.0-beta.1 和首次配置空态；通过实际控件手动创建模型并重读落库；Cmd+Q 正常退出 | `ui-results.json` / `candidate-empty.png` / `candidate-model-created.png` |
| 启动失败与隐私 | 隔离损坏 SQLite 显示原生原因、数据目录和日志路径；点击退出返回 1，损坏文件哈希不变；样例凭据未进入日志、日志权限 0600 | `ui-results.json` / `candidate-startup-error.png`；不宣称安全擦除历史副本 |
| 审查 | 启动/数据独占、HTTP/WS 生命周期和发布脚本已审查；发现的 WS P2 已修复并复核关闭，无遗留代码发现 | 复用本轮实际测试；审查不替代跨机 Gatekeeper 和真实远端兼容结果 |

验收脚本、截图和测试数据位于被忽略的 `.cache/public-beta/`，只留本机，不随 Release 上传。部分脚本用于一次性隔离演练，不能直接用于日常数据。

## 候选产物

发布命令：

```bash
npm run release:prepare -- --notes docs/releases/0.1.0-beta.1.md --allow-dirty
```

产物目录为 `build/release/0.1.0-beta.1/darwin-arm64/`，包含 App、DMG、App ZIP、LICENSE、THIRD_PARTY_NOTICES.txt、RELEASE_NOTES.md、release-metadata.json、release-manifest.json 和 SHA256SUMS。具体摘要在最终包确认后记录于同批 SHA256SUMS；当前没有对外下载资产直链。

实际签名为 ad-hoc，TeamIdentifier 未设置，未公证。`codesign --verify --deep --strict` 通过；`spctl --assess` 拒绝符合未公证状态，不能宣传 Apple 已验证开发者身份。

## 仍未覆盖

- 未曾信任 Vela 的另一台 macOS 14+ 电脑从公网下载、通过 Gatekeeper 提示完成首次打开。当前只有本机带下载隔离标记的介质及生产 App 直接启动证据，不能保证所有首次打开路径免确认。
- 最终生产包从空库经所有 UI 控件完成整套供应商/Key/权限配置；本轮新建模型为生产控件，完整配置用调试版真实 IPC，生产包读取并调用已验证。
- 普通重复启动聚焦的最终原生行为：MyGo 已接入，目录独占已实测；未为此再启动日常默认数据实例。
- 各远端供应商、所有媒体/文件/Realtime 组合、20 并发及 100 万历史规模，以及未定义的启动/内存/延迟性能门槛。

原生验收须由显式设置隔离目录的进程启动，绑定 UI 前核对实际 PID 与环境；UI 工具可能通过 LaunchServices 自动重开应用，不能假设它继承终端环境。涉及旧版升级时须先退出所有共享数据的旧进程。
