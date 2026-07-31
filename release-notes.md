-------0.0.46------
- 修复 AnyRouter 模型测试长时间等待：兼容仅发送 `data:` 的 Anthropic SSE，使用小型流式请求并在获得足够文本后提前完成
- 将“模型可用性”与“完整测速”分离：已收到有效文本但流未正常收尾时显示可用及警告，不再误报测试超时
- 修正 Token 统计模型归属：按稳定渠道 ID 记录，支持独立查看 `baibei-claude-opus-4-8` 等共享上游模型的渠道
- 新统计按北京时间（Asia/Shanghai）聚合日期和小时，并兼容标记升级前的 UTC 历史数据
- 优化调用统计页的筛选、摘要卡、小时图、模型详情和窄窗口布局，明确标示估算 Token 与未报告 usage
- 新增日志与诊断页：支持热加载详细日志、级别/请求 ID/模型筛选、脱敏查看、打开日志目录和导出诊断包
- 补充流式测试、统计迁移、北京时间跨日、模型详情、日志脱敏及前端状态回归测试

-------0.0.45------
- 更新 AnyRouter Claude Code 兼容指纹：UA 升级至 Claude Code 2.1.220、Stainless SDK 0.94.0，并补充稳定会话标识
- Claude Code 模式默认仅使用 Bearer 鉴权，自定义 Header 仍拥有最高优先级
- 修正 1M 上下文 wire 语义：出站模型移除 `[1m]` 后缀，按需发送 `context-1m-2025-08-07` beta
- 按同机 Claude Code 2.1.220 流式抓包对齐 beta 顺序、runtime 与 sdk-cli UA，移除 redact-thinking beta 和旧 billing system 注入
- 修正 AnyRouter 新版验证格式：`metadata.user_id` 使用 JSON 字符串并与 v4 会话 ID 对齐，清理无有效签名的历史 thinking block
- 对 429、502、503、504 及可恢复的 TLS/连接错误执行有限退避重试，并在最终错误中附带脱敏状态序列
- 补充请求指纹、鉴权、1M、RequestBodyOverride、脱敏诊断及模型级模式回归测试；真实流式验收中 AnyRouter 返回预期 429、AgentRouter 完整成功
- 模型编辑页支持 generic、Claude Code、Codex 三种客户端模式及 Anthropic 1M 开关，中转站编辑体验同步优化

-------0.0.44------
- 新增中转站客户端模式：支持 generic、Claude Code 与 Codex 请求指纹，模型请求、模型列表探测和重连路径保持一致
- 修复 AgentRouter 的 unauthorized_client_error：补齐 Claude Code beta、x-app、direct-browser 与 Stainless 请求头
- 新增 Anthropic 1M 上下文显式开关：本地上下文提升至 1,000,000，仅出站派生 [1m] 并按需发送 context-1m beta
- AnyRouter、AgentRouter 默认使用 Claude Code 模式，OpenAI 兼容内置站点默认使用 Codex 模式
- 修复模型绑定的中转站已不存在：支持按协议和地址安全重绑，或保留完整连接信息并降级为独立模型
- 新增请求指纹、鉴权、自定义请求头优先级、配置迁移和 1M 上下文回归测试
- 更新中英文、日文与俄文客户端模式及 1M 上下文配置文案

-------0.0.43------
- 中转站配置保存加固：落盘后回读校验密钥与模型归属，写入异常改为显式报错并记录日志
- 调用统计细化：新增中转站/模型多选筛选、每日明细表（可展开模型细分）、缓存命中率趋势与按小时分布
- 页脚署名补充二次开发信息
- 新增 GitHub Actions 自动构建：推送标签自动发布 Windows / Linux / macOS 安装包

注：按小时分布自本版本起累积，历史日期无小时数据。

-------0.0.42------
- 修复grep或者read长时间阻塞问题 @liorxuan
-------0.0.41------
- 修复内存泄漏问题，该可能导致内存异常占用
- 支持俄语增加翻译范围
- 修复qwen-3.8-max中断问题(mimo也应该属于同一类问题)
- 修复claude模型可能无法识别图片问题 @GGHansome
- 支持自定义Openai端点 @Sxuan-Coder
- WebSearch 接入百度搜索，DuckDuckGo 作为兜底 @杨超
- 修复一些兼容性问题 @kael-odin 
- 修复window上一些表现问题 @philau2512

🔔 如何让AI自动拉模型配置? (以下为提示词，把地址和密钥换为你的)
我的模型配置在 ～/.cursor-local-assistant-v2/config.yaml，我的API地址是：https://xxx 密钥是xxx，帮我拉所有模型配置进去，不要影响已有模型，根据models标准接口拉取。
