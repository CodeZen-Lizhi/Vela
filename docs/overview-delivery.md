# 总览接入记录

2026-11-13，在 `preview-page` worktree 基于同步后的代码实施。需求依据为 [UI 原型要求](/Users/zhenglizhi/otherProjects/Aether/docs/tasks/11-07-desktop-rewrite/ui-prototype.md) 的总览相关正文及“Vela 总览正式接入”节，统计身份、保留及固定时区沿用 plan.md R13、R19～R21，关注沿用 R44。

## 实现

总览成为默认首页，复用 shadcn-svelte 的卡片、分段选项、按钮、日期输入、表格、对话框及 Tooltip。保留三张统计卡、时间范围、柱线趋势、共享费用口径、三维度分布、九列排序明细、全年热力图选日、首次接入引导和四类关注。关注快捷设置复用现有编辑器；倍率基线和已读状态在本机 WebView 持久保存，切页仍观察。

后端通过 `VelaService.Overview` 返回同一事务内的统计快照。小时聚合与请求结算同事务保存，长期保留；进行中请求仅在读取时合并，不重复写入长期统计。模型/供应商统计逻辑请求，Key 统计真实尝试。标准费用使用开始时价格快照、倍率一逐尝试计算，实际费用使用当时倍率；零倍率有效。

首次原子回填仍保留的请求，减去已回填部分后保留旧日累计。无法恢复的历史状态、用量拆分、标准费用和小时归属明确未知；有已知部分显示“≥”。本轮没有修改上游会话留下的记录页 820px 表头溢出问题。

## 当前验证

- `npm run generate` 成功；`npm --workspace vela-frontend run build` 通过，Svelte 0 错误/0 警告；保留既有主 chunk 超过 500kB 的提示。
- `go test ./internal/records ./internal/app`：180 项通过。覆盖真实 HTTP 结算、重试和零倍率、进行中计数、清理保留、改名、历史迁移和重启幂等、无效数据、结算/迁移回滚及超限明确报错。
- `node --test frontend/src/lib/overview/data.test.mjs`：2 项通过，覆盖逻辑/Key 口径隔离、零倍率、部分已知金额、统计时区、小时桶、旧日累计与热力日历。
- Ego 浏览器加载真实 Svelte App、生成 RPC 客户端、本地 Go 服务及隔离 SQLite。6 次合成调用经真实 HTTP 记录结算：全部 6 请求、720 Token、标准费用 $0.0027、实际 $0.0015；今日 4 请求、3 成功、1 失败、480 Token、标准 $0.0018、实际 $0.00075，与预期一致。
- 实际操作时间范围、自定义无效日期、柱线切换、费用口径联动、Key 分布、明细排序和热力选日；九列桌面明细无下钻，Token 胶囊实际 72×8px、分段高度 8px。820px 明细转为卡片，弹窗 754/754 无横向溢出。
- 实测 1440px 和 820px 页面、内容区均无横向溢出；1440px 三卡等宽且高166px，820px 请求/Token 两列、费用单行。全年热力图在自身区域滚动。浅深色及关注弹窗已观察。
- 实际保存四项提醒关闭后模块隐藏，重载仍隐藏；倍率 0.5→0.8 显示升高提醒，已读后重载仍移除。真实读取失败保留最后成功数据；空库显示零汇总和首次配置，点击进入供应商。浏览器未处理错误为0。
- code-review 发现的两项 P2（未知用量分项显示零、部分已知标准金额隐藏）均修复并定向复核关闭。`git diff --check` 通过。

证据在 `.cache/overview-e2e/evidence/`，包括 `browser-checks.json`、`overview-1440.png`、`overview-820-top.png`、`overview-dark-820.png`、`details-820.png` 和 `attention-settings-820.png`。业务桥及逐笔样例核算在 `.cache/overview-e2e/`；余额、健康及验证差异为隔离合成状态，请求结算和设置持久化使用真实实现。

## 边界

本轮验证内容层与真实本地服务，未安装或替换日常桌面 APP，未操作用户数据库或访问外部供应商。一次快照超过十万统计维度时明确报错，不静默截断。已清理历史无法恢复的字段继续保持未知。倍率观察只记录应用运行期间读到的相邻已知值，不声称捕获应用退出期间的每次变化。

## 2026-11-13 加密 Key 配置读取修复

桌面供应商模块会将 Key 配置文档加密保存，原总览 SQL 对 `provider_keys.document` 直接执行 `json_extract`，在真实配置下触发 `malformed JSON`。统计查询改为只判断 Key 是否存在，应用层复用 `providers.KeyNames` 解密解析当前名称，每家供应商只读取一次。未关闭配置加密，也未丢弃异常行。

新增 `TestOverviewEncryptedKeyNames` 使用已启用真实文档加密的调度 fixture：修复前稳定复现相同错误，修复后读取成功并验证改名后的当前名称。`go test ./internal/records ./internal/app` 共181项通过。真实本机数据库仅做只读检查，提取66行概览和必要配置到受限副本；副本的真实应用服务读取通过，浏览器显示三张统计卡、趋势和热力图，不再出现 malformed JSON。没有修改原数据库或复制请求正文。证据位于 `.cache/overview-json-fix/verification.json` 和 `restored-page.png`。

`npm run build` 已重新生成 macOS arm64 应用和 DMG，类型检查0错误/0警告。需求同步：无需更新，本次修复落实既有真实统计及加密配置边界，不改变需求行为。

## 2026-11-13 总览时间范围单入口

用户要求把顶部多个时间范围按钮和独立双日期面板收成一个时间框。总览现在只渲染一个时间范围触发器，复用 shadcn-svelte Popover 与 RangeCalendar：快捷范围在弹出层内选择，自定义范围按第一天、最后一天两个端点完成；完整区间选定后立即应用并关闭弹层。今天、近 7 天、近 30 天、全部及热力图选日继续共用原有统计口径。

范围日历限制在统计起点至今天，未来日期不可选；入口展示当前口径或简洁日期区间，并显示统计时区。820×600 下弹层底部位于 `594.56px`，未超出 `600px` 视口；820px 与 390px 的内容页均满足 `scrollWidth === clientWidth`。浅深主题均读取现有语义色，没有另做控件色板。

验证使用 `Vela Dev` 与真实本机统计：打开单入口后切到近 7 天，页头变为 `2026-11-07 — 2026-11-13`，汇总同步更新；重新打开后依次选择 `2026-11-12` 和 `2026-11-13`，范围应用后弹层关闭；点击热力图 `2026-11-12` 后入口更新为该日。浏览器同构页面覆盖快捷范围、端点选择、Esc 关闭、浅深主题和 390px 布局，Console 未发现错误。最终 `svelte-check` 为 0 错误、0 警告，`npm run build` 完成前端、darwin/arm64 应用及 DMG 构建。

证据位于 `.cache/business-e2e-overview/date-range-real-820.png`、`date-range-popover-1180.png`、`date-range-applied-1180.png`、`date-range-820-final.png`、`date-range-390-final.png` 和 `date-range-dark-1180.png`。本轮验证开发桌面应用及真实本地统计，未替换日常安装包，也未访问外部供应商。

需求同步：已更新 [UI 原型要求](/Users/zhenglizhi/otherProjects/Aether/docs/tasks/11-07-desktop-rewrite/ui-prototype.md) 的“Vela 总览时间范围单入口”条款。

## 2026-11-13 时间范围弹层尺寸收紧

用户反馈原弹层接近整窗大小。选择器宽度由 `320px` 收紧为 `288px`，日期格由 `32px` 收紧为 `28px`，并压缩标题、快捷范围、周间距和页脚密度。总览仍只有一个时间入口，快捷范围、自定义两端点、统计时区和热力图选日规则未变。

浏览器实际渲染与交互检查：820×600 下弹层为 `288×416.56px`，底部位于 `502.56px`，高度占视口 `69.4%`；390×600 下弹层完整位于视口内，页面 `scrollWidth === clientWidth`。1180×760 深色外观下弹层保持 `288×416.56px`，使用主题浮层、文字和边框语义色。快捷范围切换后单入口更新为“近 7 天”，按 Escape 可关闭。真实 `Vela Dev` 同时打开同一选择器，本机统计、三张汇总卡和趋势继续正常渲染。

证据位于 `.cache/business-e2e-overview/date-range-compact-820.png`、`date-range-compact-390.png`、`date-range-compact-dark-1180.png` 和 `date-range-compact-native-1180.png`。

需求同步：已同步 [UI 原型要求](/Users/zhenglizhi/otherProjects/Aether/docs/tasks/11-07-desktop-rewrite/ui-prototype.md) 的“Vela 总览时间范围单入口（2026-11-13，当前有效）”条款，新增紧凑浮层、28px 日期点击区域及 820×600 下不超过视口 75% 的验收。
