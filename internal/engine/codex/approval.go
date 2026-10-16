package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// request 发送请求，并在引擎要求审批时征询 approver 后重试。
//
// 引擎的审批模型：调用返回 ok=false 且带 approvalRequest 时，调用方需要
// 决定是否放行；放行则在同一请求的 meta 里带上 x-oai-cua-approved-app
// 重发。这是一次请求内的握手，不产生额外的方法调用。
func (c *Client) request(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	resp, err := c.dispatch(ctx, method, params, nil, timeout)
	if err != nil {
		return nil, err
	}
	if resp.OK {
		return resp.Result, nil
	}

	// 引擎要求审批。
	if resp.ApprovalRequest == nil || resp.ApprovalRequest.App == "" {
		return nil, helperError(method, resp.Error)
	}

	req := resp.ApprovalRequest.toEngine()
	decision, derr := c.approver.Decide(ctx, req)
	if derr != nil {
		return nil, fmt.Errorf("审批出错: %w", derr)
	}
	if !decision.Allow {
		return nil, fmt.Errorf("未获批准操作 %s: %s", req.DisplayName, decision.Reason)
	}

	// 带批准标记重发。
	meta := map[string]any{approvedAppMetaKey: req.App}
	resp2, err := c.dispatch(ctx, method, params, meta, timeout)
	if err != nil {
		return nil, err
	}
	if resp2.OK {
		return resp2.Result, nil
	}
	return nil, helperError(method, resp2.Error)
}

func helperError(method, msg string) error {
	if msg == "" {
		return fmt.Errorf("helper 请求失败: %s", method)
	}
	return fmt.Errorf("%s", msg)
}

// requestInto 发送请求并把 result 解码到 out。
func (c *Client) requestInto(ctx context.Context, method string, params any, timeout time.Duration, out any) error {
	raw, err := c.request(ctx, method, params, timeout)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	// result 为 null 时（多数写操作如此）没有可解码的内容。
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("解析 %s 的结果失败: %w", method, err)
	}
	return nil
}
