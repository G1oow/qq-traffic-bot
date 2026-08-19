package qqbot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestSendReplyIncrementsMsgSeq(t *testing.T) {
	var lastSeq int64
	var tokenCalls int32
	var messageCalls int32

	mux := http.NewServeMux()
	mux.HandleFunc("/app/getAppAccessToken", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenCalls, 1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":7200}`))
	})
	mux.HandleFunc("/v2/users/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&messageCalls, 1)
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			MsgSeq int64  `json:"msg_seq"`
			MsgID  string `json:"msg_id"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("解析请求失败: %v", err)
		}
		atomic.StoreInt64(&lastSeq, payload.MsgSeq)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewWithAPIBase(srv.URL, "id", "secret")
	if err := c.SendReply(context.Background(), "u1", "m1", "hi"); err != nil {
		t.Fatalf("第一次发送失败: %v", err)
	}
	if err := c.SendReply(context.Background(), "u1", "m1", "hi2"); err != nil {
		t.Fatalf("第二次发送失败: %v", err)
	}
	if got := atomic.LoadInt64(&lastSeq); got != 2 {
		t.Fatalf("msg_seq 期望递增到 2，实际 %d", got)
	}
	if got := atomic.LoadInt32(&tokenCalls); got != 1 {
		t.Fatalf("access token 应只取 1 次（缓存命中），实际 %d", got)
	}
	if got := atomic.LoadInt32(&messageCalls); got != 2 {
		t.Fatalf("消息请求应发送 2 次，实际 %d", got)
	}
}
