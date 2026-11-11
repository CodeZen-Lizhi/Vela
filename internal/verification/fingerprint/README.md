# 内置 lm-detector 核心

来源：https://github.com/Ikaleio/lm-detector/tree/a7e9d3056e019827904dd50722c20b4a0c896738/sdk/go

固定提交：`a7e9d3056e019827904dd50722c20b4a0c896738`。MIT 许可全文见 `LICENSE`，版权归上游作者 `xqy2006`。保留第三方源码注释；本地新增与改写说明使用中文。

## 本地差异

- 包名由 `lmfpd` 改为 `fingerprint`；核心评分、特征、二进制解码、题目、请求和响应解析沿用上游。
- 删除上游 `Load / LoadOptions / DefaultDataURLs`、网络下载、后台更新、磁盘缓存与 zstd 依赖。只通过 `LoadBundled()` 读取原样 embed 的 bank/bin；未引入 SDK 或任何新 Go 依赖。
- `DetectConfig.HTTPClient *http.Client` 通过 options 传入 sampler。nil 使用上游禁止跟随重定向的默认 client；非 nil 完整沿用调用方客户端，代理、重定向策略与连接生命周期由调用方管理。所有请求继续传递传入 context 的取消。
- 未加入 Vela 业务、固定目标重试或 Gemini；这些由调用方组织。

## API

```go
func LoadBundled() (*Detector, error)
func (d *Detector) BuiltAt() string
func (d *Detector) Models() []ReferenceModel
func (d *Detector) ReferenceModels() []ReferenceModel // Models 的别名
func GenerateChallenges(count int) ([]Challenge, error)
func ParseNumbers(text string) []int
func BuildRequest(config DetectConfig, prompt string) (*Request, error)
func RequestSample(ctx context.Context, config DetectConfig, challenge Challenge) Sample
func (d *Detector) Analyze(outputs []Output, strict bool) (*Analysis, error)
func (d *Detector) Detect(ctx context.Context, config DetectConfig, options ...DetectOption) (*DetectResult, error)
```

`ReferenceModel` 提供上游真实的 `ID / DisplayName / Family / FamilyName / ResponseCount`。`Models` 返回独立副本，不加载新数据；本快照有 69 个模型。没有全局模型目录或全局解码缓存。

包初始化不解码离线 bank/bin。每次显式 `LoadBundled` 创建一个评分器；任务开始后加载一次，采样及 Analyze 期间复用，任务结束释放评分器引用即可由 GC 回收解码状态。原始 embed 字节随程序存在。上游少量题目模板的包初始化行为保留。

`Analyze` 接受 1～3 个 `Output{Text, ExpectedCount}`；strict 要求 3 个有效样本；非 strict 的 1～2 个有效样本仅输出 partial 排名，无校准置信度。保留 `Analysis / Sample / Challenge / Output / DetectConfig` 等上游原类型与 JSON 字段。`Timeout=0` 默认 120 秒，负值禁用计时器；计时覆盖首 SSE 字节或整个 JSON 响应。采样失败/取消在 `Sample.State / Error` 返回。

## 数据追溯

两个文件均来自上述固定提交的 `data/`，未压缩、裁剪或改写。bank 与 detector 构建时间均为 `2026-11-11T01:24:58.268488+00:00`；加载时还核对模型顺序、ID 和响应数量。

| 文件 | SHA-256 |
| --- | --- |
| data/unified_bank.json | `a6e8d6291b50e7cf25744b5fbad46460ed4cc0b94e156bb2719a9d714684b635` |
| data/shared_detector.bin | `8c7d48ea242026d123baa7cfbbe377701ac8d6899e6796f319ce555d3ae06b6c` |
| testdata/conformance/analyze.json（上游原始旧版） | `7bdd3ab697de6b1a2600d7dc19103380c10139abfd6f0db8e92a03141ad0ce73` |
| testdata/analyze-snapshot.json（当前数据的独立期望） | `46246a8a3854594c527c4a3532c0577ce12422d652e3bc01e3b96b7cb832b4d8` |

## 验证与 fixture 版本差异

`go test -v ./internal/verification/fingerprint` 运行真实核心：11 组解析、49 组请求、13 组本地 HTTP 流/JSON 样本、12 组评分，以及真实注入 TLS client、context 取消、模型元数据隔离和题目约束测试。数值比较沿用上游相对容差 `1e-9`，逐项比较完整 Analysis（包括 69 个候选、分数、概率和诊断），不复制 Go 业务算法到测试。

注意：指定提交中原始 `sdk/conformance/analyze.json` 对应 `2026-10-25T10:04:10.682171+00:00` 数据，与该提交 bank/bin 不匹配。`testdata/conformance/` 完整保留原始 fixture；解析、请求、流和模板直接与之比较。评分使用原始 12 组固定输入，以同提交上游 Python SDK `_codec / _analysis / _ranker / _features` 及当前 bank/bin 独立重算，保存为 `testdata/analyze-snapshot.json`；没有使用本 Go 实现产生测试期望。

重算仅需 Python 与 NumPy（本次环境 NumPy 2.2.6），不载入上游网络/缓存模块：

```sh
python3 internal/verification/fingerprint/testdata/generate_snapshot.py /path/to/lm-detector-at-fixed-commit
```

第三方 conformance runner 改为 Go 测试入口及本地 LoadBundled，保留原比较逻辑。全链路 Vela 业务、真实外部模型与 Gemini 不在此包验收范围。
