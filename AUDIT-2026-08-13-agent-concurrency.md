# Agent 本地模块并发与安全审计报告

- 日期：2026-08-13
- 方式：4 个并行子代理分别审计 forwarder 并发核心、agent 执行桥/模型流、forwarder 存储层、server/bridge/skills 周边；逐文件通读 + 跨文件追踪共享状态，`go vet` 全部通过。
- 结论：**3 个 P1 + 8 个 P2** 确认可触发的真实问题；1 个设计级安全权衡需单独决策。核心并发设计（actor 模型、终态收敛、文件锁协议、SSE 流生命周期、zip slip 防护、凭证处理）经核查是稳固的。
- 状态：**均未修复**，仅记录。

---

## P1（高危，建议优先修复）

### P1-1 conversationID 路径穿越：`DeleteConversation("..")` 可删除整个应用数据目录

- 位置：`internal/backend/forwarder/file_store.go:868`（`validateConversationID`）、`internal/backend/forwarder/conversation_search.go:168-181`（`DeleteConversation`）
- 问题：`validateConversationID` 只拦截 `/` 和 `\`，`"."` 与 `".."` 直接放行。`DeleteConversation` 对不存在 `state.json` 的目录直接 `os.RemoveAll(conversationDir)`：
  - `id="."` → 目录归一为历史根 → 根下无 `state.json` → **删光全部会话**；
  - `id=".."` → 历史根父目录 → **删掉整个应用数据目录**。
- 触发场景：前端 