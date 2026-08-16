package forwarder

import (
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

const checkpointBlobWriteTimeout = 5 * time.Second

type pendingCheckpointBlobWrite struct {
	requestID uint32
	blob      CheckpointBlob
}

func clonePendingTurnCompletion(completion *pendingTurnCompletion) *pendingTurnCompletion {
	if completion == nil {
		return nil
	}
	cloned := *completion
	return &cloned
}

// markConversationBlobAcked 登记客户端已确认写入 KV 的 blob。
// 注册表按会话、跨流存活：内容寻址的 blob 确认一次即永久有效。
func (service *Service) markConversationBlobAcked(conversationID string, key string) {
	if service == nil || strings.TrimSpace(conversationID) == "" || strings.TrimSpace(key) == "" {
		return
	}
	service.ackedBlobsMu.Lock()
	defer service.ackedBlobsMu.Unlock()
	if service.ackedConversationBlobs == nil {
		service.ackedConversationBlobs = make(map[string]map[string]struct{})
	}
	keys := service.ackedConversationBlobs[conversationID]
	if keys == nil {
		keys = make(map[string]struct{})
		service.ackedConversationBlobs[conversationID] = keys
	}
	keys[key] = struct{}{}
}

func (service *Service) conversationBlobAcked(conversationID string, key string) bool {
	if service == nil || strings.TrimSpace(conversationID) == "" || key == "" {
		return false
	}
	service.ackedBlobsMu.Lock()
	defer service.ackedBlobsMu.Unlock()
	_, ok := service.ackedConversationBlobs[conversationID][key]
	return ok
}

func (service *Service) queueCheckpointProjection(stream *ActiveStream, projection *CheckpointProjection, completion *pendingTurnCompletion) error {
	if service == nil || stream == nil || projection == nil || projection.State == nil {
		return nil
	}
	state, ok := proto.Clone(projection.State).(*agentv1.ConversationStateStructure)
	if !ok || state == nil {
		return fmt.Errorf("clone checkpoint state")
	}

	stream.mu.Lock()
	if stream.PendingCheckpointBlobWrites == nil {
		stream.PendingCheckpointBlobWrites = make(map[uint32]string)
	}
	if stream.ConfirmedCheckpointBlobs == nil {
		stream.ConfirmedCheckpointBlobs = make(map[string]struct{})
	}
	if completion == nil && stream.PendingCheckpoint != nil {
		completion = stream.PendingCheckpoint.Completion
	}
	required := make(map[string]struct{}, len(projection.Blobs))
	pendingKeys := make(map[string]struct{}, len(stream.PendingCheckpointBlobWrites))
	for _, key := range stream.PendingCheckpointBlobWrites {
		pendingKeys[key] = struct{}{}
	}
	toWrite := make([]pendingCheckpointBlobWrite, 0, len(projection.Blobs))
	conversationID := strings.TrimSpace(stream.ConversationID)
	for _, blob := range projection.Blobs {
		key := string(blob.ID)
		if key == "" {
			continue
		}
		if service.conversationBlobAcked(conversationID, key) {
			// 客户端在之前的回合已确认过这个 blob（KV 内容寻址且持久），无需重推：
			// 长对话每个新流重推全量 blob 图会把 thinking/exec delta 压在 FIFO
			// backlog 后面，还会因 5s 应答超时引发下一轮全量重发。
			continue
		}
		if _, confirmed := stream.ConfirmedCheckpointBlobs[key]; confirmed {
			continue
		}
		if _, pending := pendingKeys[key]; pending {
			// 本流已发送过（ack 未回或曾在超时窗口外迟到）：不重发，也不计入
			// Required——否则终态 checkpoint 会被已超时的 blob 卡满 5s 才落盘。
			continue
		}
		required[key] = struct{}{}
		stream.NextCheckpointBlobRequestID++
		if stream.NextCheckpointBlobRequestID == 0 {
			stream.NextCheckpointBlobRequestID++
		}
		requestID := stream.NextCheckpointBlobRequestID
		stream.PendingCheckpointBlobWrites[requestID] = key
		pendingKeys[key] = struct{}{}
		toWrite = append(toWrite, pendingCheckpointBlobWrite{requestID: requestID, blob: blob})
	}
	stream.PendingCheckpoint = &pendingCheckpointPublish{
		State:      state,
		Required:   required,
		Completion: clonePendingTurnCompletion(completion),
	}
	if completion != nil {
		stream.Phase = TurnPhaseCheckpointing
	}
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()

	for _, write := range toWrite {
		if err := service.broker.Publish(stream.RequestID, StreamEvent{
			Message: buildSetCheckpointBlobMessage(write.requestID, write.blob),
		}); err != nil {
			return service.finishAfterCheckpointSyncFailure(stream, fmt.Errorf("publish checkpoint blob: %w", err))
		}
	}
	if service.checkpointProjectionReady(stream) {
		return service.publishReadyCheckpoint(stream)
	}
	// Keep the latest live UI state ahead of an immediate client abort. Blob writes are
	// ordered before this snapshot; acknowledgements still gate terminal completion.
	if completion == nil {
		if err := service.publishPendingCheckpoint(stream); err != nil {
			return service.finishAfterCheckpointSyncFailure(stream, fmt.Errorf("publish pending checkpoint: %w", err))
		}
	}
	service.scheduleStreamTimer(
		stream,
		providerTimerKey(streamTimerCheckpointBlobs, ""),
		checkpointBlobWriteTimeout,
		streamTimerCheckpointBlobs,
		"",
		0,
		"checkpoint blob write timeout",
	)
	return nil
}

func (service *Service) publishPendingCheckpoint(stream *ActiveStream) error {
	if service == nil || stream == nil {
		return nil
	}
	stream.mu.Lock()
	pending := stream.PendingCheckpoint
	if pending == nil || pending.Published {
		stream.mu.Unlock()
		return nil
	}
	pending.Published = true
	state := pending.State
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	if err := service.broker.Publish(stream.RequestID, StreamEvent{Message: buildCheckpointMessage(state)}); err != nil {
		stream.mu.Lock()
		if stream.PendingCheckpoint == pending {
			pending.Published = false
		}
		stream.mu.Unlock()
		return err
	}
	return nil
}

func (service *Service) checkpointProjectionReady(stream *ActiveStream) bool {
	if stream == nil {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.PendingCheckpoint == nil {
		return false
	}
	for key := range stream.PendingCheckpoint.Required {
		if _, confirmed := stream.ConfirmedCheckpointBlobs[key]; !confirmed {
			return false
		}
	}
	return true
}

func (service *Service) handleCheckpointBlobResult(stream *ActiveStream, message *agentv1.KvClientMessage) error {
	if service == nil || stream == nil || message == nil || message.GetSetBlobResult() == nil {
		return nil
	}
	stream.mu.Lock()
	key, ok := stream.PendingCheckpointBlobWrites[message.GetId()]
	if ok {
		delete(stream.PendingCheckpointBlobWrites, message.GetId())
	}
	required := false
	if ok && stream.PendingCheckpoint != nil {
		_, required = stream.PendingCheckpoint.Required[key]
	}
	conversationID := strings.TrimSpace(stream.ConversationID)
	if ok && message.GetSetBlobResult().GetError() == nil {
		stream.ConfirmedCheckpointBlobs[key] = struct{}{}
	}
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	if !ok {
		return nil
	}
	if blobErr := message.GetSetBlobResult().GetError(); blobErr != nil && required {
		return service.finishAfterCheckpointSyncFailure(stream, fmt.Errorf(
			"client rejected checkpoint blob %s: %s",
			hex.EncodeToString([]byte(key)),
			firstNonEmpty(strings.TrimSpace(blobErr.GetMessage()), "unknown error"),
		))
	}
	if message.GetSetBlobResult().GetError() == nil {
		// 跨流登记：同会话后续回合的新流不再重推这个 blob。
		service.markConversationBlobAcked(conversationID, key)
	}
	if service.checkpointProjectionReady(stream) {
		return service.publishReadyCheckpoint(stream)
	}
	return nil
}

func (service *Service) publishReadyCheckpoint(stream *ActiveStream) error {
	if service == nil || stream == nil {
		return nil
	}
	stream.mu.Lock()
	pending := stream.PendingCheckpoint
	if pending == nil {
		stream.mu.Unlock()
		return nil
	}
	for key := range pending.Required {
		if _, confirmed := stream.ConfirmedCheckpointBlobs[key]; !confirmed {
			stream.mu.Unlock()
			return nil
		}
	}
	stream.PendingCheckpoint = nil
	state := pending.State
	completion := clonePendingTurnCompletion(pending.Completion)
	published := pending.Published
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	clearStreamTimer(stream, providerTimerKey(streamTimerCheckpointBlobs, ""))
	if !published {
		if err := service.broker.Publish(stream.RequestID, StreamEvent{Message: buildCheckpointMessage(state)}); err != nil {
			if completion != nil {
				log.Printf("forwarder checkpoint publish skipped before successful terminal request_id=%s err=%v", stream.RequestID, err)
				return service.finishSuccessfulTurnAfterCheckpoint(stream, *completion)
			}
			return err
		}
	}
	if completion != nil {
		return service.finishSuccessfulTurnAfterCheckpoint(stream, *completion)
	}
	return nil
}

func (service *Service) handleCheckpointBlobTimeout(stream *ActiveStream) error {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	pendingCount := len(stream.PendingCheckpointBlobWrites)
	stream.mu.Unlock()
	return service.finishAfterCheckpointSyncFailure(stream, fmt.Errorf("%d checkpoint blob writes timed out", pendingCount))
}

func (service *Service) finishAfterCheckpointSyncFailure(stream *ActiveStream, cause error) error {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	pending := stream.PendingCheckpoint
	// 保留 PendingCheckpointBlobWrites 的 requestID→key 映射：迟到的成功 ack 仍要
	// 登记进会话级注册表，且同流后续 checkpoint 借此跳过已发 blob——清空映射会让
	// 注册表永远填不上，下个 checkpoint 全量重推，形成 30MB 级风暴死循环。
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	clearStreamTimer(stream, providerTimerKey(streamTimerCheckpointBlobs, ""))
	if cause != nil {
		log.Printf("forwarder checkpoint blob sync skipped request_id=%s conversation_id=%s err=%v", stream.RequestID, stream.ConversationID, cause)
	}
	if pending == nil {
		return nil
	}
	// 应答超时只说明 blob 没有全部确认，不说明会话状态不能发布：
	// 直接丢掉最终 checkpoint 会让客户端错过整个回合（新气泡不渲染、
	// 思考/文本停在半路）。这里尽力发布，缺的 blob 由客户端已有内容兜底。
	if err := service.publishPendingCheckpoint(stream); err != nil {
		log.Printf("forwarder checkpoint best-effort publish failed request_id=%s err=%v", stream.RequestID, err)
	}
	stream.mu.Lock()
	stream.PendingCheckpoint = nil
	stream.mu.Unlock()
	if pending.Completion != nil {
		return service.finishSuccessfulTurnAfterCheckpoint(stream, *pending.Completion)
	}
	return nil
}

func (service *Service) discardPendingCheckpoint(stream *ActiveStream, reason string) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	stream.PendingCheckpoint = nil
	stream.PendingCheckpointBlobWrites = make(map[uint32]string)
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	clearStreamTimer(stream, providerTimerKey(streamTimerCheckpointBlobs, ""))
	if strings.TrimSpace(reason) != "" {
		log.Printf("forwarder pending checkpoint discarded request_id=%s conversation_id=%s reason=%s", stream.RequestID, stream.ConversationID, strings.TrimSpace(reason))
	}
}
