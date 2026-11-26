# Vela

**本地优先的 AI 网关桌面客户端。**

> Many models. One course.

在一个本机入口管理 AI 供应商、API Key、模型映射、调度和请求记录。桌面使用 MyGo 和系统 WebView，业务核心为 Go，界面为 Svelte。

首个公开测试版为 **0.1.0-beta.1**，目标为 **macOS 14+、Apple Silicon（arm64）**。本地候选包已生成，验收覆盖和剩余边界见[交付记录](docs/public-beta-delivery.md)。测试版不代表稳定版或全部性能目标已通过。

## 下载与安装

[源码与开发入口](https://github.com/CodeZen-Lizhi/Vela) · [Releases 下载页](https://github.com/CodeZen-Lizhi/Vela/releases) · [0.1.0-beta.1 说明](docs/releases/0.1.0-beta.1.md)

请从 Releases 选择对应版本的 arm64 DMG，打开后双击其中的 `Vela.app`，选择“安装并打开”：应用会先复制并校验新版，再正常退出“应用程序”中的旧版，等待网关和数据收尾后替换并启动新版。30 秒内无法退出则停止，不强杀、不覆盖仍在运行的旧版；现有配置和记录保留。取消安装不会打开用户数据。目标目录权限不足时，由系统请求管理员授权。

也可先自行退出旧版，再将 App 拖入“应用程序”。本次采用 ad-hoc 签名，未经过 Apple 公证，首次打开可能被 macOS 拦截。核对来源后按“系统设置 → 隐私与安全性”的提示选择“仍要打开”，不需要关闭系统安全保护。详细升级及异常恢复步骤见[版本说明](docs/releases/0.1.0-beta.1.md#升级与应急恢复)。

## 后续更新

在“设置 → 常规 → 应用更新”点击“检查更新”，查看新版本与说明，下载校验完成后选择“重启并更新”；选择“稍后”继续使用当前进程。重启会短暂停止网关并正常收尾，数据目录保留。下载失败或签名不匹配不会替换当前应用，可重试；已暂存的更新也会在下次正常退出后完成替换。测试版检查后续测试版和正式版，正式版只检查正式发布。

当前已完成本地更新产物与链路验证，官方仓库 `CodeZen-Lizhi/Vela` 已公开并可匿名读取 Releases，更新文件随 `v0.1.0-beta.1` 测试版 Release 分发；已安装相同版本不会重复更新，后续发布更高版本后可在应用内升级。开发版、只读位置或安装目录不可写时不启用应用内更新，可用 DMG 重新安装。

## 快速上手：完成第一个 Chat 请求

1. 打开 Vela，在“模型”页在线添加或手动创建一个对话模型，记下本地模型 ID。
2. 在“供应商”页添加支持 Chat Completions 的上游地址和 API Key，启用供应商与 Key，关联刚创建的模型并允许该 Key 使用；上游模型名不同时配置请求名映射。
3. 打开“设置 → 网关与网络”的“网关接入”。首次“访问密钥”显示“尚未设置”，点击“设置密钥”填写并“保存密钥”（已有密钥时按钮为“修改密钥”）；应用不会自动生成密钥。默认地址为 `http://127.0.0.1:18080`，若保存后提示“密钥已保存，但网关未生效”且原因为端口占用，修改“监听地址”中的端口并点击“保存端口”重试。生效后复制“接入地址”和“访问密钥”。
4. 在终端运行以下示例，将 `你的本地模型ID` 换成第 1 步的值；`read` 后输入第 3 步的网关密钥（输入不回显）。这里使用网关密钥，上游 API Key 只填在供应商配置中。

```bash
read -r -s VELA_GATEWAY_KEY
curl --fail-with-body --show-error http://127.0.0.1:18080/v1/chat/completions \
  -H "Authorization: Bearer ${VELA_GATEWAY_KEY}" \
  -H 'Content-Type: application/json' \
  --data '{"model":"你的本地模型ID","messages":[{"role":"user","content":"请只回复：Hello Vela"}],"stream":false}'
unset VELA_GATEWAY_KEY
```

成功应得到 HTTP 200 和包含 `choices[0].message` 回答的 JSON，并在“使用记录”看到对应请求及最终结果。健康检查或模型列表成功只说明本机入口可用，不能代替真实上游回答。401 检查网关密钥；模型不存在或无候选时检查本地模型 ID、关联、Key 权限及启用状态；上游拒绝与余额问题在请求详情中排查。候选包已通过本机可控上游的真实 Chat 调用；各远端供应商的兼容性仍取决于其实际实现。

## 协议与能力

网关只监听 `127.0.0.1`，四种对话协议同时提供，按上游原格式发送，不做跨协议转换。客户端的 Base URL 规则不同，请避免重复拼接 `/v1` 或 `/v1beta`。

| 协议 | 本机请求路径 | 鉴权 |
| --- | --- | --- |
| OpenAI Chat Completions | `POST /v1/chat/completions` | `Authorization: Bearer <网关密钥>` |
| OpenAI Responses | `POST /v1/responses` | 同上 |
| Anthropic Messages | `POST /v1/messages` | Bearer 或 `x-api-key` |
| Gemini | `POST /v1beta/models/{model}:generateContent`、`:streamGenerateContent`；也接受 `/v1/models/` 路径 | Bearer 或 `x-goog-api-key` |

上述路径支持各自的普通响应与 SSE。OpenAI SDK 通常将 Base URL 配为 `http://127.0.0.1:18080/v1`；自动追加版本路径的客户端应使用根地址。`GET /v1/health`、`GET /v1/models` 同样需要网关密钥，模型列表包含所有已添加类型，各调用入口分别核对模型类型及权限。

- 模型目录、完整价格编辑、同步及本地修改保护；供应商、多 Key、模型权限、映射与账户查询。
- 固定顺序、会话亲和、成本优先三种调度策略，以及重试、故障转移、健康状态与冷却。
- 总览、请求详情、用量与估算费用、长期统计、手动清理，以及模型指纹采样和计划。
- 源码已接入 Responses Compact / WebSocket、Token 计数、向量、排序、图片、视频、Search、Gemini Interactions / Files 及标准 OpenAI Realtime；具体路径和限制见[版本说明](docs/releases/0.1.0-beta.1.md#协议入口与兼容边界)，这些入口不代表每种远端供应商及候选包组合都已验收。

关闭窗口可按偏好继续在后台运行；菜单栏可打开主窗口、暂停或恢复网关。真正退出应用会停止网关，它随桌面应用运行，不是独立系统服务。

## 本地数据与隐私

默认数据目录为 `~/Library/Application Support/Vela/`，包含 `vela.sqlite` 与 `credentials/`。网关密钥、上游 API Key、账户认证及代理凭据加密保存在本机，不上传给开发者；实际请求仍会将内容和必要认证发送给你配置的上游。在线模型目录和主动/定时账户查询也会访问相应外部服务。

请求明细、诊断和累计统计保存在 SQLite。请求与响应的脱敏文本正文保存在数据目录的 `details/` 中，每次请求一个 `.json.zst` 文件。明细默认保留 **30 天**，正文默认保留 **7 天**，从请求结束起算。可在数据管理分别设置保留天数和预览清理，正文设为 0 天停止保存新正文。累计统计独立保留。媒体记录只保留文本与元信息，不保留媒体二进制或完整内嵌 base64，不承诺完整回放。

`credentials/master.key` 是解密必需的本机主密钥，`credentials/secrets.enc` 保存密文；凭据目录权限为 0700，文件为 0600。不要删除或单独搬运它们，也不要公开数据目录、日志或请求正文。同时取得主密钥和密文仍可解密，此机制不隔离同一 macOS 用户下的恶意程序；不承诺安全擦除旧数据库页或历史副本。旧网关密钥首次从钥匙串迁移可能需要一次系统授权。

## 测试版限制

- 设置 → 数据管理支持密码加密的配置导入导出，搬运模型、供应商、上游 Key、余额与 Key 查询认证、映射/权限及调度。导入可选“覆盖相同配置”或“保留原有、仅新增”，保留本机使用记录、总览统计与偏好。请保管导出密码；文件包含实际认证信息，密码遗失无法恢复。应用不提供整库备份/恢复功能。
- Responses HTTP 的 `background:true` 尚未支持，会在出站前返回 400。
- Responses 续接、视频、Gemini 文件/上传和 Interactions 的资源绑定只存在于当前进程。重启、过期、淘汰或原凭据/地址/映射失效后可能返回 409；不能跨进程续接或自动改选其他 Key。
- 费用根据上游可用用量和本地价格估算，不是供应商账单；缺失用量/价格保持未知。指纹结果仅表示参考库内统计相似性，不能认证模型身份或单凭结果认定渠道造假，检测会产生真实上游费用。
- 已接入手动检查、下载与确认重启更新，GitHub 更新发布尚未上线；Intel、Windows、Linux 不属于本次包的支持范围。20 并发及 100 万条历史规模尚未验收。

## 开发

使用 Go 1.27.3+、Node.js 22.12+。技术栈为 MyGo 0.8.3、Svelte 5、TypeScript、Vite 8、Tailwind CSS 4、shadcn-svelte 和 SQLite（modernc.org/sqlite）。

```bash
npm install
npm run dev
```

`npm run dev:web` 仅预览界面，不含桌面服务。使用 `VELA_DATA_DIR=/tmp/vela-dev npm run dev` 可隔离开发数据；隔离目录使用独立凭据，不读取日常钥匙串。版本修改后运行 `npm run version:sync`，更新依赖后运行 `npm run licenses:generate`。检查入口为 `go test ./...`、`npm run check`、`npm run test:release`；`npm run build` 构建 App 和支持双击自安装的 DMG 到 `build/`；`npm run test:installer` 隔离验证复制校验、退出、超时与替换。本地候选发布命令为 `npm run release:prepare -- --notes docs/releases/0.1.0-beta.1.md`；未提交改动用于本地验收时可显式加 `--allow-dirty`，源码状态会记入包内元数据。构建成功不能替代安装包验收；本轮状态见[公开测试版交付记录](docs/public-beta-delivery.md)。

## 许可

本项目采用 MIT 许可，具体条款见 [LICENSE](LICENSE)。第三方依赖与模型指纹内置核心/数据的许可说明见 [THIRD_PARTY_NOTICES.txt](THIRD_PARTY_NOTICES.txt)。


应用更新发布复用 MyGo：`mygo.config.ts` 固定 GitHub 仓库及 `config/update-public-key.txt` 公钥。私钥默认读取本机用户配置目录下 `Vela/update-signing/mygo-update.key`（macOS 为 `~/Library/Application Support/Vela/update-signing/mygo-update.key`），权限0600，不进入源码；CI 可通过 `MYGO_UPDATER_PRIVATE_KEY` 或 `VELA_UPDATE_PRIVATE_KEY_FILE` 注入。请妥善保管并备份私钥，已安装应用只接受相应公钥验证通过的更新，丢失私钥会影响后续更新。

构建会额外输出 `update-darwin-arm64.json` 和带签名的 `.tar.gz` 更新包；`CHANGELOG.md` 须包含根版本对应章节。候选发布入口验证大小和签名，将更新文件纳入清单。取得发布授权后，发布到对应 `v版本` 的 GitHub Release：上传 DMG、更新包、更新清单和校验文件，beta版本标为 prerelease，不用 draft 作为更新源。只有上传 DMG 无法供应用内更新；本地构建不自动发布；对外发布需明确授权。
