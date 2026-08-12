package forwarder

import (
	"context"
	"errors"
	"strings"
)

var errProviderLoopInterrupted = errors.New("provider loop interrupted")

// streamInterrupt 是一次回合的中断闩。
//
// 与 ActiveStream.ProviderCancel 的差别在生命周期：后者只覆盖单个 provider pass，
// 两个 pass 之间必然存在空窗 —— driveProvider 正是在这个空窗里做快照、编译与落盘，
// 代价随历史长度增长。取消请求落在窗内时，它既取消不了已结束的上一个 pass，
// 也拦不住即将启动的下一个 pass。
//
// 中断闩按回合存在，每个 pass 的 context 从它派生，于是：
//   - 空窗期内触发中断，随后派生出的 pass context 一诞生就是已取消状态；
//   - 重复触发天然幂等；
//   - 触发路径只碰内存，不必进 actor 队列，因此不会被 actor 正在执行的长任务挡住。
type streamInterrupt struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// armStreamInterrupt 为新回合准备中断闩。
// 已触发的闩不能复用，否则同一个 requestID 的下一回合刚启动就会被判为已取消。
func armStreamInterrupt(stream *ActiveStream) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.Interrupt != nil && stream.Interrupt.ctx.Err() == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	stream.Interrupt = &streamInterrupt{ctx: ctx, cancel: cancel}
}

// interruptStream 立即掐断本回合仍在运行的 provider pass 与压缩任务。
// 这是带外路径：只做内存操作，不写历史、不发事件，善后仍由 actor 串行完成。
func interruptStream(stream *ActiveStream) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	interrupt := stream.Interrupt
	passCancel := stream.ProviderCancel
	stream.ProviderCancel = nil
	stream.mu.Unlock()
	if interrupt != nil {
		interrupt.cancel()
	}
	if passCancel != nil {
		passCancel()
	}
}

// releaseStreamInterrupt 在 actor 退出（即回合已收口）时释放闩持有的 context 资源。
func releaseStreamInterrupt(stream *ActiveStream) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	interrupt := stream.Interrupt
	stream.mu.Unlock()
	if interrupt != nil {
		interrupt.cancel()
	}
}

// streamInterruptContext 返回派生 pass context 用的父 context。
// 闩缺失时退回 Background：宁可失去这一层保护，也不要让回合无法启动。
func streamInterruptContext(stream *ActiveStream) context.Context {
	if stream == nil {
		return context.Background()
	}
	stream.mu.Lock()
	interrupt := stream.Interrupt
	stream.mu.Unlock()
	if interrupt == nil {
		return context.Background()
	}
	return interrupt.ctx
}

// streamInterrupted 供 driveProvider 在每个耗时阶段之间做快速检查，
// 避免中断信号到达后仍把请求发给模型。
func streamInterrupted(stream *ActiveStream) bool {
	if stream == nil {
		return false
	}
	stream.mu.Lock()
	interrupt := stream.Interrupt
	stream.mu.Unlock()
	return interrupt != nil && interrupt.ctx.Err() != nil
}

func isTerminalStreamStatus(status StreamStatus) bool {
	switch status {
	case StreamStatusCanceled, StreamStatusCompleted, StreamStatusFailed:
		return true
	default:
		return false
	}
}

// streamReachedTerminalLocked 判断回合是否已经收口。调用方必须持有 stream.mu。
// Status 与 Phase 分别记录流层与回合层的收口结果，任一为终态都不应再推进。
func streamReachedTerminalLocked(stream *ActiveStream) bool {
	if stream == nil {
		return true
	}
	if isTerminalStreamStatus(stream.Status) {
		return true
	}
	switch stream.Phase {
	case TurnPhaseCompleted, TurnPhaseFailed, TurnPhaseCanceled:
		return true
	default:
		return false
	}
}

func streamReachedTerminal(stream *ActiveStream) bool {
	if stream == nil {
		return true
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return streamReachedTerminalLocked(stream)
}

func providerLoopInterruptErr(ctx context.Context, stream *ActiveStream, modelCallID string) error {
	if ctx != nil && ctx.Err() != nil {
		return errProviderLoopInterrupted
	}
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if isTerminalStreamStatus(stream.Status) {
		return errProviderLoopInterrupted
	}
	switch stream.Phase {
	case TurnPhaseCanceled, TurnPhaseCompleted, TurnPhaseFailed:
		return errProviderLoopInterrupted
	}
	expectedModelCallID := strings.TrimSpace(modelCallID)
	currentModelCallID := strings.TrimSpace(stream.CurrentModelCallID)
	if expectedModelCallID != "" && currentModelCallID != "" && currentModelCallID != expectedModelCallID {
		return errProviderLoopInterrupted
	}
	return nil
}
