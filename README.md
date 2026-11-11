# Vela

**本地优先的 AI 网关桌面客户端。**

> Many models. One course.

Vela 是 Aether 重构后的新项目。它把多家 AI 供应商、不同协议、模型映射、调度、额度和请求诊断收束到一个本机入口，桌面宿主使用 MyGo，业务核心使用 Go，界面使用 Svelte。

当前已提供桌面壳、类型化前后端绑定和模型管理第一阶段：真实目录添加、完整价格配置、本地修改保护、同步与恢复来源值。供应商、调度、网关执行、用量与诊断仍待后续实现。

## 技术栈

- Go 1.27.3
- MyGo 0.8.3，系统 WebView 模式
- Svelte 5
- TypeScript
- Vite 8
- Tailwind CSS 4
- shadcn-svelte
- SQLite，使用 modernc.org/sqlite 纯 Go 驱动

## 目录

```text
.
├── cmd/vela/                 MyGo 桌面应用入口
├── internal/app/             最小类型化应用服务
├── internal/models/          模型目录、价格规则与 SQLite 持久化
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

## 模型管理

打开“模型”页，通过“在线添加”从 models.dev 目录选择模型并确认创建，或通过“手动创建”填写目录外的模型；在线目录的厂商条只列热门官方厂商，其他来源通过搜索选取。列表提供搜索、类型筛选、详情、编辑和确认删除。价格编辑按基础、上下文阶梯、Fast、峰谷、图片和视频分组；同步保留本地修改，恢复来源值随表单保存生效，取消不写入。来源下架保留配置，单模型同步失败不阻止其他模型更新。

“设置”提供唯一全局代理开关和地址，目录下载跟随此设置；关闭时直连。默认数据保存在 macOS `~/Library/Application Support/Vela/vela.sqlite`。本地验收可使用 `VELA_DATA_DIR=/tmp/vela-acceptance npm run dev` 隔离数据。

模块通过 Go 的 `internal/models` 操作接口维护完整价格，SQLite 在每次模型写入中保存完整文档，来源身份和统一模型名有唯一性约束。前端只使用生成的 MyGo 绑定；浏览器模式明确标为预览，不模拟业务成功。

## 当前边界

本阶段维护价格配置，尚未接入网关公开模型、供应商映射、真实请求测试或费用计算。分时例外日期可手动维护，自动法定节假日日历未接入；目录未提供的媒体矩阵、缓存或价格字段保持未知，可本地补充。详情与编辑参考原型，使用白底蓝色和 shadcn-svelte 控件。

## 本地凭据与隐私

网关访问密钥与供应商 API Key 只在本机加密保存，不上传给开发者。`~/Library/Application Support/Vela/credentials/` 中的 `master.key` 是随机 256 位主密钥，`secrets.enc` 保存 AES-256-GCM 密文；上游 API Key 的密文位于 SQLite，全局及供应商/Key 代理配置文档也加密保存，避免代理账号密码明文落库。已有代理 JSON 自动迁移；不承诺安全擦除旧数据库页或历史副本。凭据目录为 0700，凭据文件为 0600；普通配置、日志和默认导出不得包含凭据及主密钥。隔离目录同样使用独立凭据文件，不读取日常钥匙串。

旧网关访问密钥首次读取时迁移，可能要求一次系统授权；成功后不再访问旧钥匙串，旧项保留。拒绝授权可重试，主密钥丢失或密文损坏时拒绝覆盖数据。不要删除或单独搬运主密钥；普通配置导出不承担凭据恢复。

此保护防止直接查看明文与数据库单独泄露，不隔离同一 macOS 用户下的恶意程序；同时取得主密钥与密文仍可解密。未公证 App 的首次打开提示属于 macOS 的独立检查，不保证所有安装和更新路径都免系统授权。正式自动更新、跨机加密导出与后续账户令牌仍按各模块需求实施。
