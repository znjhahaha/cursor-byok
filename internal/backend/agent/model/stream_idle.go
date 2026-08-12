package modeladapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	defaultProviderStreamIdleTimeout = 4 * time.Minute
	minProviderStreamIdleTimeout     = 30 * time.Second
)

// ProviderFirstTokenTimeoutError 表示 provider 在首个有效内容前超时。
// 此时尚未向上层发出任何模型事件，调用方可以安全地整体重试。
type ProviderFirstTokenTimeoutError struct {
	Timeout time.Duration
}

func (err *ProviderFirstTokenTimeoutError) Error() string {
	seconds := int(err.Timeout / time.Second)
	if seconds > 0 && err.Timeout == time.Duration(seconds)*time.Second {
		return fmt.Sprintf("provider first token timeout after %ds without any content", seconds)
	}
	return fmt.Sprintf("provider first token timeout after %s without any content", err.Timeout)
}

// IsProviderFirstTokenTimeout 判断错误链中是否包含首 token 超时。
func IsProviderFirstTokenTimeout(err error) bool {
	var target *ProviderFirstTokenTimeoutError
	return errors.As(err, &target)
}

type providerStreamIdleWatchdog struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	// firstTokenTimeout 是首个有效内容前的独立超时；0 表示禁用两段模式。
	firstTokenTimeout time.Duration
	timeout           time.Duration
	timer             *time.Timer

	mu               sync.Mutex
	body             io.Closer
	firstContentSeen bool
	stopped          bool
	timedOut         bool
	err              error
}

func newProviderStreamIdleWatchdog(parent context.Context, firstTokenTimeout time.Duration, timeout time.Duration) (context.Context, *providerStreamIdleWatchdog) {
	if parent == nil {
		parent = context.Background()
	}
	timeout = normalizeProviderStreamIdleTimeoutDuration(timeout)
	if firstTokenTimeout < 0 {
		firstTokenTimeout = 0
	}
	// 首段超时不应长于整体空闲超时，否则退化为单段模式。
	if firstTokenTimeout >= timeout {
		firstTokenTimeout = 0
	}
	ctx, cancel := context.WithCancelCause(parent)
	watchdog := &providerStreamIdleWatchdog{
		ctx:               ctx,
		cancel:            cancel,
		firstTokenTimeout: firstTokenTimeout,
		timeout:           timeout,
	}
	initial := timeout
	if firstTokenTimeout > 0 {
		initial = firstTokenTimeout
	}
	watchdog.timer = time.AfterFunc(initial, watchdog.expire)
	return ctx, watchdog
}

func (watchdog *providerStreamIdleWatchdog) AttachBody(body io.Closer) {
	if watchdog == nil || body == nil {
		return
	}
	watchdog.mu.Lock()
	watchdog.body = body
	shouldClose := watchdog.timedOut || watchdog.stopped
	watchdog.mu.Unlock()
	if shouldClose {
		_ = body.Close()
	}
}

func (watchdog *providerStreamIdleWatchdog) MarkEffectiveContent() {
	if watchdog == nil {
		return
	}
	watchdog.mu.Lock()
	defer watchdog.mu.Unlock()
	watchdog.firstContentSeen = true
	if watchdog.stopped || watchdog.timedOut || watchdog.timer == nil {
		return
	}
	watchdog.timer.Reset(watchdog.timeout)
}

func (watchdog *providerStreamIdleWatchdog) Stop() {
	if watchdog == nil {
		return
	}
	watchdog.mu.Lock()
	if watchdog.stopped {
		watchdog.mu.Unlock()
		return
	}
	watchdog.stopped = true
	watchdog.body = nil
	if watchdog.timer != nil {
		watchdog.timer.Stop()
	}
	watchdog.mu.Unlock()
	watchdog.cancel(nil)
}

func (watchdog *providerStreamIdleWatchdog) Err() error {
	if watchdog == nil {
		return nil
	}
	watchdog.mu.Lock()
	defer watchdog.mu.Unlock()
	if watchdog.timedOut {
		return watchdog.err
	}
	return nil
}

func (watchdog *providerStreamIdleWatchdog) expire() {
	watchdog.mu.Lock()
	if watchdog.stopped || watchdog.timedOut {
		watchdog.mu.Unlock()
		return
	}
	watchdog.timedOut = true
	if !watchdog.firstContentSeen && watchdog.firstTokenTimeout > 0 {
		watchdog.err = &ProviderFirstTokenTimeoutError{Timeout: watchdog.firstTokenTimeout}
	} else {
		watchdog.err = providerStreamIdleTimeoutError(watchdog.timeout)
	}
	body := watchdog.body
	err := watchdog.err
	watchdog.mu.Unlock()

	watchdog.cancel(err)
	if body != nil {
		_ = body.Close()
	}
}

func normalizeProviderStreamIdleTimeoutDuration(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultProviderStreamIdleTimeout
	}
	if timeout < minProviderStreamIdleTimeout {
		return minProviderStreamIdleTimeout
	}
	return timeout
}

func providerStreamIdleTimeoutError(timeout time.Duration) error {
	seconds := int(timeout / time.Second)
	if seconds > 0 && timeout == time.Duration(seconds)*time.Second {
		return fmt.Errorf("provider stream idle timeout after %ds without effective content", seconds)
	}
	return fmt.Errorf("provider stream idle timeout after %s without effective content", timeout)
}
