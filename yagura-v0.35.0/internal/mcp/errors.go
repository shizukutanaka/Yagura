// errors.go — MCP tool エラーの構造化定義。
//
// ToolError は user-visible message と内部 cause を分離する。
// 内部 cause は logger に渡すが、JSON-RPC error response には message のみ含める
// (secret leak 防止)。
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ToolError は MCP tool 実行中に発生する公開可能なエラー。
type ToolError struct {
	Code    string // "not_found" / "invalid_input" / "internal"
	Message string // user-visible
	Cause   error  // internal, logger 用
}

// Error は Code と Message と Cause を含む error 文字列を返す。
func (e *ToolError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap は内部 Cause error を返す(errors.As/Is のチェーン用)。
func (e *ToolError) Unwrap() error { return e.Cause }

// 公開エラーカテゴリ。&ToolError{Code: ...} を直接生成しても良いが、
// 良くあるパターンは sentinel として比較できる。
var (
	// ErrNotFound はリソースが見つからない場合の sentinel ToolError。
	ErrNotFound = &ToolError{Code: "not_found", Message: "resource not found"}
	// ErrInvalidInput は入力値が不正な場合の sentinel ToolError。
	ErrInvalidInput = &ToolError{Code: "invalid_input", Message: "invalid input"}
	// ErrInternal はサーバー内部エラーの sentinel ToolError。
	ErrInternal = &ToolError{Code: "internal", Message: "internal error"}
)

// IsCode は err が指定された code の ToolError と一致するかを判定する。
func IsCode(err error, code string) bool {
	var te *ToolError
	if errors.As(err, &te) {
		return te.Code == code
	}
	return false
}

// asJSONError は ToolError を JSON-RPC error response 用に変換する。
//
// JSON-RPC 標準 error codes:
//
//	-32602: invalid params(クライアント側エラー)
//	-32603: internal error(サーバ側エラー)
//	-32000: server error(application-specific、その他)
func asJSONError(err error) json.RawMessage {
	var te *ToolError
	if !errors.As(err, &te) {
		te = &ToolError{Code: "internal", Message: "internal error", Cause: err}
	}
	code := -32000
	switch te.Code {
	case "invalid_input":
		code = -32602
	case "internal":
		code = -32603
	case "not_found":
		// not_found は application-specific として -32001
		code = -32001
	}
	b, _ := json.Marshal(map[string]any{
		"code":    code,
		"message": te.Message,
		"data":    map[string]any{"code": te.Code},
	})
	return b
}
