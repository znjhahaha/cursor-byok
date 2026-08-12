// conversation_metadata.go 处理客户端提交的会话元数据（AgentService/UpdateConversationMetadata）。
package forwarder

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"cursor/gen/agentv1"
)

// UpdateConversationMetadata 幂等地把会话标题写入 state.json。
// 允许 metadata 早于首次 RunSSE 到达：会话骨架按需创建，后续真实轮次直接续写。
func (service *Service) UpdateConversationMetadata(_ context.Context, req *connect.Request[agentv1.UpdateConversationMetadataRequest]) (*connect.Response[agentv1.UpdateConversationMetadataResponse], error) {
	if service == nil || service.store == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("forwarder store is not initialized"))
	}
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("update conversation metadata request is required"))
	}
	conversationID := strings.TrimSpace(req.Msg.GetConversationId())
	if conversationID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("conversation id is required"))
	}
	// name 未设置时保持现状，重复提交同名是幂等覆盖。
	if req.Msg.Name == nil {
		return connect.NewResponse(&agentv1.UpdateConversationMetadataResponse{}), nil
	}
	name := strings.TrimSpace(req.Msg.GetName())
	if _, err := service.store.UpdateConversationMeta(conversationID, func(conversation *ConversationFile) error {
		conversation.Name = name
		return nil
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentv1.UpdateConversationMetadataResponse{}), nil
}
