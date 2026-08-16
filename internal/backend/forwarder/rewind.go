package forwarder

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

type runRewindDecision struct {
	Evaluated          bool
	Apply              bool
	Reason             string
	SkipReason         string
	IncomingMessageID  string
	HasClientTurnCount bool
	ClientTurnCount    int
	ServerTailTurnSeq  int64
	ServerNextTurnSeq  int64
	TargetTurnSeq      int64
	TargetEntrySeq     int64
	TargetRequestID    string
	MatchCount         int
	DroppedEntryCount  int
	DroppedTurnCount   int
	DroppedSeqStart    int64
	DroppedSeqEnd      int64
	PrefixEntries      []HistoryEntry
}

type runRewindMatch struct {
	Entry HistoryEntry
}

func (service *Service) decideRunRewind(intent InboundIntent, conversation *ConversationFile) runRewindDecision {
	decision := runRewindDecision{ClientTurnCount: -1}
	if !shouldEvaluateRunRewind(intent) {
		return decision
	}
	decision.Evaluated = true
	decision.IncomingMessageID = strings.TrimSpace(intent.UserMessage.GetMessageId())
	decision.HasClientTurnCount = intent.ConversationState != nil
	if decision.HasClientTurnCount {
		decision.ClientTurnCount = len(intent.ConversationState.GetTurns())
	}
	if conversation != nil {
		decision.ServerTailTurnSeq = maxHistoryTurnSeq(conversation.Entries)
		if decision.ServerTailTurnSeq == 0 && len(conversation.Entries) > 0 {
			// 导入历史的 entry 可能全部没有 TurnSeq，此时用条目数当伪尾部，
			// 让按位置截断的撤回也能通过尾部判断。
			decision.ServerTailTurnSeq = int64(len(conversation.Entries))
		}
		decision.ServerNextTurnSeq = conversation.NextTurnSeq
	}
	if decision.IncomingMessageID == "" {
		decision.SkipReason = "missing_message_id"
		return decision
	}
	if conversation == nil || len(conversation.Entries) == 0 {
		decision.SkipReason = "message_id_not_found"
		return decision
	}

	positional := false
	matches := findUserMessageEntriesByMessageID(conversation.Entries, decision.IncomingMessageID)
	decision.MatchCount = len(matches)
	if len(matches) > 0 {
		selected, selectReason := selectRunRewindMatch(matches, decision.ClientTurnCount, decision.HasClientTurnCount)
		decision.TargetTurnSeq = selected.Entry.TurnSeq
		decision.TargetEntrySeq = selected.Entry.Seq
		decision.TargetRequestID = strings.TrimSpace(selected.Entry.RequestID)
		decision.Reason = selectReason
	} else if targetTurnSeq, targetRequestID, ok := alignRunRewindByConversationState(conversation.Entries, intent.ConversationState); ok {
		// 客户端撤回后重发的是新 message_id（编辑重发场景），messageId 匹配不到：
		// 用客户端 ConversationState 的回合结构与服务端 user_message entry 的
		// request_id 对齐推断撤回点，否则整段被撤回的上下文会继续喂给模型。
		decision.TargetTurnSeq = targetTurnSeq
		decision.TargetRequestID = targetRequestID
		decision.Reason = "conversation_state_aligned"
	} else {
		decision.SkipReason = "message_id_not_found"
		return decision
	}

	if decision.TargetTurnSeq <= 0 {
		// 命中的 entry 没有有效 TurnSeq（例如从客户端 ConversationState 导入的历史）：
		// 退回按 Seq 位置截断，而不是放弃撤回让旧上下文全部残留。
		prefix, targetTurnSeq := prefixEntriesBeforeEntrySeq(conversation.Entries, decision.TargetEntrySeq)
		if targetTurnSeq > 0 {
			decision.TargetTurnSeq = targetTurnSeq
			positional = true
			decision.Reason = decision.Reason + "+positional"
			decision.PrefixEntries = prefix
		} else {
			decision.SkipReason = "target_turn_seq_missing"
			return decision
		}
	}

	serverTailBeyondTarget := decision.ServerTailTurnSeq > decision.TargetTurnSeq
	clientBehindServerTail := decision.HasClientTurnCount && int64(decision.ClientTurnCount) < decision.ServerTailTurnSeq
	if !serverTailBeyondTarget && !clientBehindServerTail {
		decision.SkipReason = "message_id_at_active_tail"
		return decision
	}

	decision.Apply = true
	if !positional {
		decision.PrefixEntries = prefixEntriesBeforeTurn(conversation.Entries, decision.TargetTurnSeq)
	}
	decision.DroppedEntryCount, decision.DroppedTurnCount, decision.DroppedSeqStart, decision.DroppedSeqEnd = droppedEntryStats(conversation.Entries, decision.TargetTurnSeq)
	return decision
}

func shouldEvaluateRunRewind(intent InboundIntent) bool {
	if strings.TrimSpace(intent.Kind) != "run" || intent.Prewarm || intent.UserMessage == nil {
		return false
	}
	return conversationActionCase(intent.ClientMessage) == "user_message_action"
}

func findUserMessageEntriesByMessageID(entries []HistoryEntry, messageID string) []runRewindMatch {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" || len(entries) == 0 {
		return nil
	}
	matches := make([]runRewindMatch, 0, 1)
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) != "user_message" || len(entry.Payload) == 0 {
			continue
		}
		userMessage := &agentv1.UserMessage{}
		if err := protojson.Unmarshal(entry.Payload, userMessage); err != nil {
			continue
		}
		if strings.TrimSpace(userMessage.GetMessageId()) != messageID {
			continue
		}
		matches = append(matches, runRewindMatch{Entry: entry})
	}
	return matches
}

// selectRunRewindMatch 统一选最早匹配：messageId 出现多份只可能来自重发/regenerate，
// 最早一份是原始位置。宁可撤回到更早的位置，也不能让对齐位置之前的旧回合
// 继续留在模型上下文里。
func selectRunRewindMatch(matches []runRewindMatch, clientTurnCount int, hasClientTurnCount bool) (runRewindMatch, string) {
	if len(matches) == 0 {
		return runRewindMatch{}, "no_match"
	}
	earliest := earliestRunRewindMatch(matches)
	if !hasClientTurnCount || clientTurnCount < 0 {
		return earliest, "earliest_message_id_match"
	}
	targetTurnSeq := int64(clientTurnCount) + 1
	switch {
	case earliest.Entry.TurnSeq == targetTurnSeq:
		return earliest, "client_turn_count_aligned"
	case earliest.Entry.TurnSeq > targetTurnSeq:
		return earliest, "first_match_after_client_turn_count"
	default:
		return earliest, "earliest_duplicate_before_client_turn_count"
	}
}

func earliestRunRewindMatch(matches []runRewindMatch) runRewindMatch {
	earliest := matches[0]
	for _, match := range matches[1:] {
		if earlierHistoryEntry(match.Entry, earliest.Entry) {
			earliest = match
		}
	}
	return earliest
}

// alignRunRewindByConversationState 在 messageId 匹配失败时，用客户端回合结构与服务端
// user_message entry 的 request_id 做对齐：客户端把对话撤回到第 k 回合后重发新消息，
// 它携带的 turns 只剩 k 项，最后一项的 request_id 仍指向服务端第 k 回合的 user_message。
// 仅当服务端尾部严格超出该对齐位置时才认定为撤回；正常新回合（尾部恰好对齐）不触发。
func alignRunRewindByConversationState(entries []HistoryEntry, state *agentv1.ConversationStateStructure) (int64, string, bool) {
	if state == nil {
		return 0, "", false
	}
	turns := state.GetTurns()
	if len(turns) == 0 {
		return 0, "", false
	}
	clientTurnCount := int64(len(turns))
	if maxHistoryTurnSeq(entries) <= clientTurnCount {
		return 0, "", false
	}
	turn := &agentv1.ConversationTurnStructure{}
	if err := proto.Unmarshal(turns[len(turns)-1], turn); err != nil {
		return 0, "", false
	}
	agentTurn := turn.GetAgentConversationTurn()
	if agentTurn == nil {
		return 0, "", false
	}
	lastTurnRequestID := strings.TrimSpace(agentTurn.GetRequestId())
	if lastTurnRequestID == "" {
		return 0, "", false
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) != "user_message" || entry.TurnSeq != clientTurnCount {
			continue
		}
		if strings.TrimSpace(entry.RequestID) == lastTurnRequestID {
			return clientTurnCount + 1, lastTurnRequestID, true
		}
	}
	return 0, "", false
}

func earlierHistoryEntry(left HistoryEntry, right HistoryEntry) bool {
	if left.TurnSeq != right.TurnSeq {
		return left.TurnSeq < right.TurnSeq
	}
	if left.Seq != right.Seq {
		return left.Seq < right.Seq
	}
	return left.CreatedAt.Before(right.CreatedAt)
}

func maxHistoryTurnSeq(entries []HistoryEntry) int64 {
	var maxTurnSeq int64
	for _, entry := range entries {
		if entry.TurnSeq > maxTurnSeq {
			maxTurnSeq = entry.TurnSeq
		}
	}
	return maxTurnSeq
}

func prefixEntriesBeforeTurn(entries []HistoryEntry, targetTurnSeq int64) []HistoryEntry {
	if len(entries) == 0 {
		return nil
	}
	prefix := make([]HistoryEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.TurnSeq < targetTurnSeq {
			prefix = append(prefix, entry)
		}
	}
	return prefix
}

// prefixEntriesBeforeEntrySeq 返回 Seq 小于 targetEntrySeq 的前缀，以及截断后
// 新回合应使用的 TurnSeq（前缀中的最大 TurnSeq + 1）。用于导入历史这类
// entry 没有有效 TurnSeq、只能按位置截断的场景。
func prefixEntriesBeforeEntrySeq(entries []HistoryEntry, targetEntrySeq int64) ([]HistoryEntry, int64) {
	if len(entries) == 0 || targetEntrySeq <= 0 {
		return nil, 0
	}
	prefix := make([]HistoryEntry, 0, len(entries))
	var maxTurnSeq int64
	for _, entry := range entries {
		if entry.Seq >= targetEntrySeq {
			continue
		}
		prefix = append(prefix, entry)
		if entry.TurnSeq > maxTurnSeq {
			maxTurnSeq = entry.TurnSeq
		}
	}
	if len(prefix) == len(entries) {
		return nil, 0
	}
	return prefix, maxTurnSeq + 1
}

func droppedEntryStats(entries []HistoryEntry, targetTurnSeq int64) (int, int, int64, int64) {
	if len(entries) == 0 {
		return 0, 0, 0, 0
	}
	droppedTurns := make(map[int64]struct{})
	var droppedEntries int
	var seqStart int64
	var seqEnd int64
	for _, entry := range entries {
		if entry.TurnSeq < targetTurnSeq {
			continue
		}
		droppedEntries++
		if entry.TurnSeq > 0 {
			droppedTurns[entry.TurnSeq] = struct{}{}
		}
		if entry.Seq > 0 && (seqStart == 0 || entry.Seq < seqStart) {
			seqStart = entry.Seq
		}
		if entry.Seq > seqEnd {
			seqEnd = entry.Seq
		}
	}
	return droppedEntries, len(droppedTurns), seqStart, seqEnd
}

func appendReplacementRunEntries(prefix []HistoryEntry, entries []HistoryEntry) []HistoryEntry {
	replacement := make([]HistoryEntry, 0, len(prefix)+len(entries))
	replacement = append(replacement, prefix...)
	replacement = append(replacement, entries...)
	return replacement
}

func (service *Service) applyRunRewindToConversation(conversation *ConversationFile, decision runRewindDecision, entries []HistoryEntry, intent InboundIntent, turnSeq int64) {
	if conversation == nil || !decision.Apply {
		return
	}
	conversation.Entries = nil
	conversation.NextEntrySeq = 1
	conversation.NextTurnSeq = 1
	appendEntriesInPlace(conversation, appendReplacementRunEntries(decision.PrefixEntries, entries))
	applyRunRewindConversationState(conversation, intent, turnSeq)
	deriveConversationLoopState(conversation)
}

func applyRunRewindConversationState(conversation *ConversationFile, intent InboundIntent, turnSeq int64) {
	if conversation == nil {
		return
	}
	conversation.TokenDetailsUsedTokens = 0
	if conversation.TokenDetailsMaxTokens == 0 {
		conversation.TokenDetailsMaxTokens = projectedConversationMaxTokens
	}
	clearConversationAutoCompactionState(conversation)
	conversation.LatestRequestPrefix = nil
	conversation.LastProviderCall = nil
	conversation.CurrentLoopID = fmt.Sprintf("%d:%s", turnSeq, strings.TrimSpace(intent.RequestID))
	conversation.CurrentLoopStatus = "running"
	conversation.CurrentRequestID = strings.TrimSpace(intent.RequestID)
	conversation.CurrentTurnSeq = turnSeq
}

func applyRunRewindMetadata(conversation *ConversationFile, source *ConversationFile, intent InboundIntent, turnSeq int64) {
	if conversation == nil {
		return
	}
	if source != nil {
		if strings.TrimSpace(source.ConversationID) != "" {
			conversation.ConversationID = strings.TrimSpace(source.ConversationID)
		}
		if strings.TrimSpace(source.RootConversationID) != "" {
			conversation.RootConversationID = strings.TrimSpace(source.RootConversationID)
		}
		conversation.ParentConversationID = strings.TrimSpace(source.ParentConversationID)
		conversation.ParentToolCallID = strings.TrimSpace(source.ParentToolCallID)
		conversation.SubagentTypeName = strings.TrimSpace(source.SubagentTypeName)
		if strings.TrimSpace(source.Mode) != "" {
			conversation.Mode = strings.TrimSpace(source.Mode)
		}
		if source.TokenDetailsMaxTokens > 0 {
			conversation.TokenDetailsMaxTokens = source.TokenDetailsMaxTokens
		}
	}
	applyRunRewindConversationState(conversation, intent, turnSeq)
}

func (service *Service) logRunRewindDecision(requestID string, conversationID string, eventName string, decision runRewindDecision) {
	if service == nil || !decision.Evaluated {
		return
	}
	fields := map[string]any{
		"message_id":           decision.IncomingMessageID,
		"apply":                decision.Apply,
		"reason":               decision.Reason,
		"skip_reason":          decision.SkipReason,
		"target_turn_seq":      decision.TargetTurnSeq,
		"target_entry_seq":     decision.TargetEntrySeq,
		"target_request_id":    decision.TargetRequestID,
		"server_tail_turn_seq": decision.ServerTailTurnSeq,
		"server_next_turn_seq": decision.ServerNextTurnSeq,
		"match_count":          decision.MatchCount,
		"dropped_entry_count":  decision.DroppedEntryCount,
		"dropped_turn_count":   decision.DroppedTurnCount,
		"dropped_seq_start":    decision.DroppedSeqStart,
		"dropped_seq_end":      decision.DroppedSeqEnd,
	}
	if decision.HasClientTurnCount {
		fields["client_turn_count"] = decision.ClientTurnCount
	} else {
		fields["client_turn_count"] = nil
	}
	service.debug.LogRuntime(context.Background(), requestID, conversationID, eventName, fields)
}
