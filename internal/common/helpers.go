package common

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v2"
)

var requestSeq atomic.Uint32

// GenerateSTAN creates a pseudo-unique STAN using source-app suffix + UTC time.
// Why needed: DK requests require a transaction reference identifier.
// Called from: business and DKPG controllers when stan_number is absent.
func GenerateSTAN(sourceApp string) string {
	suffix := "0000"
	if len(sourceApp) >= 4 {
		suffix = sourceApp[len(sourceApp)-4:]
	} else if len(sourceApp) > 0 {
		suffix = strings.Repeat("0", 4-len(sourceApp)) + sourceApp
	}
	stamp := time.Now().UTC()
	ms2 := stamp.Nanosecond() / 1e7
	timestampPart := fmt.Sprintf("%02d%02d%02d%02d", stamp.Hour(), stamp.Minute(), stamp.Second(), ms2)
	return suffix + timestampPart
}

// NewRequestID generates a unique request identifier for outbound DK calls.
// Called from: business and DKPG handlers when request_id is omitted by clients.
func NewRequestID() string {
	now := time.Now().UTC()
	seq := requestSeq.Add(1) % 1000
	// Compact format: REQ-yyyymmddHHMMSS-mmmsss (ms + sequence)
	return fmt.Sprintf("REQ-%s-%03d%03d", now.Format("20060102150405"), now.Nanosecond()/1e6, seq)
}

// RequestIDFromFiber returns request id already assigned to current Fiber request, if present.
// Priority: middleware local value first, then incoming request header.
func RequestIDFromFiber(c *fiber.Ctx) string {
	if c == nil {
		return ""
	}
	if v, ok := c.Locals("request_id").(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return strings.TrimSpace(c.Get("X-Request-Id"))
}

// NewBFSOrderNo generates a compact fallback BFS order number.
// Why needed: pull-confirm can proceed even when upstream initiate did not return bfs_orderNo.
func NewBFSOrderNo(reference string) string {
	now := time.Now().UTC()
	suffix := ""
	trimmed := strings.TrimSpace(reference)
	if len(trimmed) >= 6 {
		suffix = trimmed[len(trimmed)-6:]
	} else {
		suffix = trimmed
	}
	suffix = sanitizeAlphaNumUpper(suffix)
	if suffix == "" {
		suffix = strconv.FormatInt(now.Unix()%1000000, 10)
	}
	return fmt.Sprintf("BFS-%s-%s", now.Format("060102150405"), suffix)
}

func sanitizeAlphaNumUpper(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ExtractResponseCode parses response_code and response message/description.
// Supports both direct and wrapped payloads:
// - {"response_code":"...","response_message":"...","response_data":{...}}
// - {"response":{"response_code":"...","response_message":"...","response_data":{...}}}
func ExtractResponseCode(raw []byte) (string, string) {
	var res struct {
		ResponseCode        string `json:"response_code"`
		ResponseMessage     string `json:"response_message"`
		ResponseDescription string `json:"response_description"`
		Response            struct {
			ResponseCode        string `json:"response_code"`
			ResponseMessage     string `json:"response_message"`
			ResponseDescription string `json:"response_description"`
		} `json:"response"`
	}
	_ = json.Unmarshal(raw, &res)

	code := strings.TrimSpace(res.ResponseCode)
	msg := strings.TrimSpace(res.ResponseMessage)
	desc := strings.TrimSpace(res.ResponseDescription)
	if code == "" {
		code = strings.TrimSpace(res.Response.ResponseCode)
	}
	if msg == "" {
		msg = strings.TrimSpace(res.Response.ResponseMessage)
	}
	if desc == "" {
		desc = strings.TrimSpace(res.Response.ResponseDescription)
	}
	if msg == "" {
		msg = desc
	}
	return code, msg
}

// ExtractBFSTxnID reads response_data.bfs_txn_id from DK response payload.
// Supports both direct and wrapped payload shapes.
func ExtractBFSTxnID(raw []byte) string {
	var res struct {
		ResponseData struct {
			BFSTxnID string `json:"bfs_txn_id"`
		} `json:"response_data"`
		Response struct {
			ResponseData struct {
				BFSTxnID string `json:"bfs_txn_id"`
			} `json:"response_data"`
		} `json:"response"`
	}
	_ = json.Unmarshal(raw, &res)
	if v := strings.TrimSpace(res.ResponseData.BFSTxnID); v != "" {
		return v
	}
	return strings.TrimSpace(res.Response.ResponseData.BFSTxnID)
}

// ExtractBFSOrderNo reads bfs_orderNo/bfs_order_no from response_data.
// Supports both direct and wrapped payload shapes.
func ExtractBFSOrderNo(raw []byte) string {
	var res struct {
		ResponseData struct {
			BFSOrderNo    string `json:"bfs_orderNo"`
			BFSOrderNoAlt string `json:"bfs_order_no"`
		} `json:"response_data"`
		Response struct {
			ResponseData struct {
				BFSOrderNo    string `json:"bfs_orderNo"`
				BFSOrderNoAlt string `json:"bfs_order_no"`
			} `json:"response_data"`
		} `json:"response"`
	}
	_ = json.Unmarshal(raw, &res)

	v := strings.TrimSpace(res.ResponseData.BFSOrderNo)
	if v == "" {
		v = strings.TrimSpace(res.ResponseData.BFSOrderNoAlt)
	}
	if v != "" {
		return v
	}

	v = strings.TrimSpace(res.Response.ResponseData.BFSOrderNo)
	if v == "" {
		v = strings.TrimSpace(res.Response.ResponseData.BFSOrderNoAlt)
	}
	return v
}

// CtxFromFiber returns Fiber's request context or background context fallback.
// Called from: controllers before repository/client operations.
func CtxFromFiber(c *fiber.Ctx) context.Context {
	ctx := c.UserContext()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
