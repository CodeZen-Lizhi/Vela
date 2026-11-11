# 调度语言迁移

日期：2026-11-11。分支：`codex/scheduling-migration`。

## 依据与范围

以旧 Aether 真实调用链补全功能，以已确认的 Vela 调度需求覆盖明确差异。需求正文位于 `/Users/zhenglizhi/otherProjects/Aether/docs/tasks/11-07-desktop-rewrite/scheduling.md`。

当前 Worktree 带入主目录必要的未提交模型、供应商、设置、桌面壳和策略页作为基线；主目录保留原样。本次新增行为主要在 `internal/scheduling`、`internal/app/scheduling*`、`internal/providers/routing.go` 和调度页面。开发时未提交、push 或合并；本次按用户要求将调度改动提交并合入本地主分支，不push。

## 已实现

- 三策略、完整供应商顺序、即时保存、修订隔离、真实创建追加/删除移除、停用保留、失败回退、整行拖动和键盘排序。
- 调度Go入口 `app.NewSchedulingExecution` → `Executor.Execute` → 真实供应商模型/Key权限/凭据复核 → `Runtime.Acquire` → HTTP/SSE执行 → 整Key健康结算及SQLite持久化。
- 固定顺序、5分钟成功续期亲和、成本倍率（零最前、未知最后），没有第二次随机/压力选路。
- 真实RPM、并发、同Key尝试与各供应商转移预算；轻重/中性分类、连续故障加重、熔断、冷却、单恢复试探与凭据/熔断代次保护。
- HTTP聊天流式总期限默认120秒、首次响应默认30秒、熔断等待上限1～32分钟/默认10分钟；全局、供应商和Key代理选择沿用既有能力。
- 四种聊天协议的HTTP/SSE适配，输出后失败不重放；HTTP连续会话无法验证时409边界。非聊天协议可使用独立AttemptRunner，不强套聊天健康分类。
- 配置搬运可读取 `Store.Policy` 并通过 `Store.ImportPolicy` 原子应用策略与完整顺序，不信任外部修订号。

## 旧版调用链补全事实

- 亲和读取不续期；完整成功重新写入后续300秒，进程重启不恢复亲和。只恢复Key健康/熔断/冷却。
- 旧模型映射逐候选只选一条请求名，同候选重试不换下一映射。失败可以转其他已枚举Key/供应商，不能把映射数组当轮询或回退队列。
- 转移预算按供应商隔离，从该供应商首次真实尝试开始；同供应商换Key计转移，同Key重试不计。某供应商耗尽后可继续其他供应商。
- 实际主执行排序上下文关闭正健康分比较；同优先级固定顺序按身份，亲和/成本策略保留种子散列同级比较。
- HTTP `previous_response_id` 非null（包括空字符串）不能作为普通请求自动重开；Responses item完成不等于response完成；Anthropic须message_stop；Chat的wire finish_reason或DONE才是终态。Gemini沿旧解析器的可见输出/EOF边界，不声称额外增加严格wire终态要求。

## 验证记录

- 受影响包测试和race、`go vet`：真实HTTP K1→K1→K2、逐次扣分、备用亲和、固定顺序回主、输出后断流不重放、Key权限、健康恢复、供应商顺序生命周期和非法导入原子拒绝。
- 真实MyGo WebView + 生成绑定 + 隔离SQLite：策略切换、键盘调整顺序、切页重读及最新二进制重启后读回一致。证据：`/tmp/vela-scheduling-desktop-evidence.json`、`/tmp/vela-scheduling-desktop.png`。
- `go test ./...`、前端检查和MyGo `.app`/DMG构建通过。前端真实组件交互检查覆盖pointer拖动、键盘、焦点、失败回退、窄窗与减少动画；该部分使用受控保存边界，不代替以上真实IPC验证。

## 集成边界

此仓库还没有正式网关监听和协议路由；供应商管理页面已由主分支提供。本次提供已接入真实数据的调度Go执行入口，并通过真实本地上游验证；没有把内部执行器测试称为已完成正式网关接入。

供应商模型关系/权限与有序映射直接复用主分支已有界面和Go操作。余额查询与余额低提醒没有真实数据来源，未伪造展示。整份跨机配置文件/凭据搬运、Responses WebSocket所有权、图片/视频等独立执行器由对应模块接入，不在调度页面新增配置或模拟成功。


审查与修复：独立审查发现并修复了SSE短行无空行时事件累计无界、Gemini空content误算成功两项P2；均增加真实HTTP回归。进一步自检区分了HTTP流式共享总期限和非流式每尝试超时，增加真实1秒总期限下换Key不会重置、超时不扣备用健康的组合测试。

会话迁移复核另修正Search强制Codex family和SDK空白Anthropic头检测两处旧版一致性偏差，均补充对应回归。


## 合入主分支适配

主分支已具备完整供应商模型关联、Key权限、操作范围映射和本地凭据加密。合入时删除调度分支临时提供的重复模型表和写操作，直接复用主分支的 `provider_models`、`provider_key_models`、`provider_model_mappings` 与 `ResolveModelRequestName`；配置通过主分支 `decodeDocument` 读取，代理认证仅在后端解密。组合HTTP回归改为使用真实整文档加密存储，以覆盖主分支新增的持久化约束。
