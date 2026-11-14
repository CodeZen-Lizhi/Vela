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


## DMG 双击自安装（2026-11-14）

用户选择“从 DMG 双击 App 自行安装”，替代本轮先前新增的 PKG 方案。已移除 PKG 构建脚本及发布入口，沿用 MyGo 的 App/DMG 构建。只读 `/Volumes/` 或 Gatekeeper AppTranslocation 中的正式版在初始化用户数据和单实例检查前进入原生“安装并打开”确认；取消不访问业务数据，正常安装版和开发版不拦截。

安装先在目标父目录暂存新版并检查签名及应用身份，再仅向目标旧版可执行路径发送一次 SIGTERM，等待原生正常退出与业务收尾。最多30秒，拒绝退出时不替换、不强杀并显示错误。以同卷换名替换整包，失败时恢复旧包，无法恢复时保留备份位置；权限不足或旧包归其他用户所有时通过系统申请管理员授权。成功后由原桌面用户通过 `open -n` 打开已安装副本，避免激活还在DMG中的进程；显式隔离数据目录透传给新进程，安装不操作业务数据库或凭据。

原生验收使用最终DMG的真实Vela.app作为来源，共享实际安装确认/安装/启动入口，将目标固定在 `.cache/dmg-self-install/Applications/`。原生确认后，旧生产App进程5660正常返回0，新进程5834从安装目标启动；旧包独有文件被移除，新包签名通过，隔离凭据主密钥SHA256不变，SQLite integrity_check为ok，新进程确认使用同一隔离数据目录。未替换日常 `/Applications/Vela.app`。证据为 `.cache/dmg-self-install/checks.json`、`state.json`、`native.log`。CUA读取验收宿主时因其已退出而超时，未取得弹窗截图；安装成功依据独立进程、文件和重读结果，不宣称已取得截图。

Go验收覆盖真实签名App的复制与整包替换、其他目录同名进程保留、损坏安装源在退出旧版前失败、拒绝退出30秒后旧包保留和暂存清理，以及真实只读DMG来源识别；构建、测试与自检最终结果见本节后续记录。管理员授权分支及Gatekeeper首次来源提示未实测，不据此保证所有机器免系统确认。需求正文同步Aether总方案R55与A37。

最终检查：`go test ./cmd/vela ./internal/desktopinstall -count=1 -v`通过（含4项安装器测试，无跳过），`go vet`、Shell/Go静态自检及Diff空白检查通过；`npm run build`完成App签名及DMG生成，Svelte0错误/0警告，保留既有大chunk提示。当前DMG SHA256：`5ca9c2014b3991816390d3b4b3cc173007788d111b09cac34c575d2f5c0b018d`。验收进程已正常退出，日常应用保持运行。应用内下载更新仅答复可行性，未接入或发布更新源。


## 应用内更新接入（2026-11-14）

用户已授权“DMG首次安装＋应用内更新”。常规设置增加检查更新、版本及纯文本发行说明、共享Progress下载进度、下载完成后的二次重启确认。更新状态由宿主Manager持有，不因离开设置取消下载；检查与下载串行，重复点击不并发，失败保留原因和重试入口，只有签名验证完成才可重启。复用MyGo Updater正常退出后的替换和重启，无新增更新服务或静默下载。beta构建检查后续测试版及正式版，正式构建仅正式发布；不可写安装位置及开发版明确提示不可更新。

独立Ed25519签名公钥位于 `config/update-public-key.txt`；正式私钥保存在用户配置目录的 `Vela/update-signing/mygo-update.key`，权限0600，未加入源码、日志或包。MyGo构建额外输出签名tar.gz及平台清单，CHANGELOG提供对应版本说明；release:prepare校验清单存在、实际包大小及Ed25519签名，更新文件纳入候选SHA256SUMS和manifest。此签名用于更新包验证，与Apple Developer ID签名/公证不同。

实际验收：原生隔离App使用当前Manager/MyGo后端、真实本机HTTP更新清单和包，先拒绝错误签名，再下载有效签名包；ready时未提前重启，确认后旧进程返回0，1.1.0新版从原目标路径启动，替换后App签名通过。证据 `.cache/application-update/native/checks.json`、`native-http.log`；测试密钥与正式密钥隔离，原生验收不访问日常应用或用户数据库。前端用实际ApplicationUpdate及共享控件验证检查→下载25%→ready→稍后不重启→确认后仅一次重启，控制台错误0；820px页面clientWidth/scrollWidth均820，截图为 `.cache/application-update/downloading.png`、`ready-820.png`。该界面验收使用合成服务响应，不能当作原生IPC或远端发布通过。

检查：受影响app/updates/cmd三包测试通过，updates race通过；6项发布测试通过，含错误签名和包大小拒绝；go vet、Diff检查及最终npm run build通过，Svelte0错误/0警告，保留既有大chunk提示。最终签名产物另以源码公钥验证通过。

上线边界：只读查询确认 `CodeZen-Lizhi/Vela` 为PRIVATE，匿名GitHub Releases API返回404。已向用户说明需使用公开发布仓库或HTTPS更新地址；当前正式公开更新源尚待决定，不能宣传已上线。没有对外创建/上传Release、push/tag，未替换日常应用；新增能力须先通过DMG安装带内置公钥的版本，之前不含更新器的版本不能凭本次源码自动获得更新入口。权威需求同步总方案R55与A37。


### 正式更新仓库公开（2026-11-14）

用户明确选择将现有 `CodeZen-Lizhi/Vela` 仓库改为公开，已执行可见性修改；无需另建发布仓库，内置更新源保持原地址。修改前镜像读取远端唯一main分支历史，51个可达提交；Gitleaks实际扫描48个非合并提交、约18.23MB。两条命中均为Key生命周期测试的同一固定假密钥，已核对连续数字字母尾部和memoryKeys替代，不是真实凭据；未发现密钥、数据库或.env类文件进入历史。扫描是已执行的凭据检查，不保证穷尽所有敏感信息。

修改后以不带认证的HTTP请求验证：仓库API返回visibility=public、private=false；Releases API返回200，列表为空。证据位于 `.cache/visibility-audit/assessment.json`、`public-repo.json`、`public-releases.json`；原始扫描结果已脱敏。当前仅仓库及已有历史公开，未push本地改动、创建tag或发布Release，在线更新资产仍需后续明确授权发布。总方案R55同步确认使用现有公开仓库。


## 首个测试版发布授权（2026-11-14）

用户明确要求创建Release并上传DMG、签名更新包及更新清单。本次将已验收的请求记录布局、DMG自安装和应用内更新改动纳入可追溯源码提交，从干净源码构建 `v0.1.0-beta.1`，通过草稿Release上传并校验后公开为prerelease。构建在本机macOS执行，不将正式更新私钥传给GitHub；源码、标签、更新清单及下载资产保持同版本。发布后的URL、摘要与匿名下载验证以实际结果补记；本段仅记录授权与发布流程，不提前宣称上传完成。
