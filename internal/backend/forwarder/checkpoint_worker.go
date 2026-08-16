// checkpoint_worker.go 把 checkpoint 投影挪出 actor 协程。
//
// 长会话下一次投影是 O(历史长度) 的 CPU（结构化状态解码全部 tool_result、
// 全量回合 blob 重新编码、整个 replay 的 JSON 重编码），原先串在 actor 上，
// 每个工具往返 3-5 次，直接把 provider 的 thinking/text/exec delta 压住几秒
// ——UI 表现为思考指示长时间不动然后整块弹出、shell 输出迟到。
//
// 规则：
//   - 单 worker，同一 requestID 的发布严格按提交顺序执行（旧状态不会覆盖新状态）；
//   - 中间 checkpoint（completion == nil）只影响客户端展示，出队时被更新的
//     请求吸收（快照在处理时才取，被吸收的旧请求自然作废），失败只记日志；
//   - 终态 checkpoint（completion != nil）永不丢弃，调用方在 done 上阻塞等待，
//     保证 turn_ended 之前终态 checkpoint 与错误语义与同步实现完全一致。
package forwarder

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

type checkpointWorkRequest struct {
	completion *pendingTurnCompletion
	done       chan error
}

type checkpointStreamQueue struct {
	items []checkpointWorkRequest
}

type checkpointWorker struct {
	mu      sync.Mutex
	cond    *sync.Cond
	queues  map[string]*checkpointStreamQueue
	pending int
	started bool
}

func (service *Service) enqueueCheckpointWork(requestID string, request checkpointWorkRequest) {
	if service == nil {
		return
	}
	worker := &service.checkpointWorker
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.cond == nil {
		worker.cond = sync.NewCond(&worker.mu)
	}
	if worker.queues == nil {
		worker.queues = make(map[string]*checkpointStreamQueue)
	}
	queue := worker.queues[requestID]
	if queue == nil {
		queue = &checkpointStreamQueue{}
		worker.queues[requestID] = queue
	}
	queue.items = append(queue.items, request)
	worker.pending++
	if !worker.started {
		worker.started = true
		go service.checkpointWorkerLoop()
	}
	worker.cond.Broadcast()
}

// checkpointWorkerNext 取下一个待处理请求。被更新请求吸收的中间请求在出队时
// 丢弃；丢弃与取出都要递减 pending 并唤醒 flushCheckpointWork 的等待者。
func (service *Service) checkpointWorkerNext() (string, checkpointWorkRequest, bool) {
	worker := &service.checkpointWorker
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.cond == nil {
		worker.cond = sync.NewCond(&worker.mu)
	}
	for len(worker.queues) == 0 {
		worker.cond.Wait()
	}
	for requestID, queue := range worker.queues {
		for len(queue.items) > 1 && queue.items[0].completion == nil {
			queue.items = queue.items[1:]
			worker.pending--
			worker.cond.Broadcast()
		}
		item := queue.items[0]
		queue.items = queue.items[1:]
		if len(queue.items) == 0 {
			delete(worker.queues, requestID)
		}
		return requestID, item, true
	}
	return "", checkpointWorkRequest{}, false
}

// finishCheckpointWork 在一次请求处理完毕（或吸收丢弃）后递减 pending，
// flushCheckpointWork 的等待者据此判断队列真正排空——递减不能发生在出队时，
// 否则调用方会在事件尚未发布时就醒来。
func (service *Service) finishCheckpointWork() {
	worker := &service.checkpointWorker
	worker.mu.Lock()
	worker.pending--
	worker.cond.Broadcast()
	worker.mu.Unlock()
}

func (service *Service) checkpointWorkerLoop() {
	for {
		requestID, request, ok := service.checkpointWorkerNext()
		if !ok {
			return
		}
		err := service.processCheckpointRequest(requestID, request)
		service.finishCheckpointWork()
		if request.done != nil {
			request.done <- err
		} else if err != nil {
			log.Printf("forwarder checkpoint publish failed request_id=%s err=%v", strings.TrimSpace(requestID), err)
		}
	}
}

func (service *Service) processCheckpointRequest(requestID string, request checkpointWorkRequest) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("checkpoint worker panic: %v", recovered)
			log.Printf("forwarder checkpoint worker panic request_id=%s err=%v", strings.TrimSpace(requestID), err)
		}
	}()
	return service.projectAndQueueCheckpoint(requestID, request.completion)
}

// flushCheckpointWork 等待已入队的全部 checkpoint 请求处理完毕（含被吸收的）。
// 供测试在触发异步中间 checkpoint 后做确定性断言。
func (service *Service) flushCheckpointWork() {
	if service == nil {
		return
	}
	worker := &service.checkpointWorker
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.cond == nil {
		return
	}
	for worker.pending > 0 {
		worker.cond.Wait()
	}
}

// projectAndQueueCheckpoint 是 checkpoint 发布的同步实现，由 worker 调用：
// 取会话快照（entries 借用共享，读路径不可变）→ 投影 → 叠加运行态 → blob 门控发布。
func (service *Service) projectAndQueueCheckpoint(requestID string, completion *pendingTurnCompletion) error {
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return fmt.Errorf("request is not active: %s", requestID)
	}
	conversation, pendingExecs, pendingInteractions, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		return err
	}
	projection, err := service.projector.ProjectCheckpointProjection(conversation)
	if err != nil {
		return err
	}
	if projection == nil || projection.State == nil {
		return fmt.Errorf("checkpoint projection is empty")
	}
	// 投影按指纹缓存跨调用共享：State 必须先克隆再叠加运行态字段
	// （PendingToolCalls、展示用 token 明细），否则会污染缓存。
	state, ok := proto.Clone(projection.State).(*agentv1.ConversationStateStructure)
	if !ok || state == nil {
		return fmt.Errorf("clone checkpoint state")
	}
	state.PendingToolCalls = buildPendingToolCalls(pendingExecs, pendingInteractions)
	service.rewriteCheckpointTokenDetailsForClient(stream, conversation, state)
	return service.queueCheckpointProjection(stream, &CheckpointProjection{State: state, Blobs: projection.Blobs}, completion)
}
