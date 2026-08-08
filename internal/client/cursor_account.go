package client

import (
	"context"
	"fmt"
	"time"

	"cursor/internal/cursoraccount"
)

// CursorAccountStatus 是暴露给前端的脱敏账号状态，不含任何令牌。
type CursorAccountStatus = cursoraccount.Status

// GetCursorAccountStatus 返回当前控制面账号状态，并顺带补全缺失的邮箱。
func (s *ProxyService) GetCursorAccountStatus() CursorAccountStatus {
	if s == nil || s.cursorAccount == nil {
		return CursorAccountStatus{State: cursoraccount.StateSignedOut}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.cursorAccount.EnsureEmail(ctx)
	return s.cursorAccount.Status()
}

// StartCursorAccountLogin 打开官方登录页并开始轮询授权结果。
func (s *ProxyService) StartCursorAccountLogin() (CursorAccountStatus, error) {
	if s == nil || s.cursorAccount == nil {
		return CursorAccountStatus{State: cursoraccount.StateError}, fmt.Errorf("Cursor 账号服务未初始化")
	}
	return s.cursorAccount.StartLogin()
}

// DisconnectCursorAccount 清除本地凭据，控制面路由随之回落到未登录行为。
func (s *ProxyService) DisconnectCursorAccount() (CursorAccountStatus, error) {
	if s == nil || s.cursorAccount == nil {
		return CursorAccountStatus{State: cursoraccount.StateSignedOut}, nil
	}
	return s.cursorAccount.Disconnect()
}