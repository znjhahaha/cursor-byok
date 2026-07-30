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
