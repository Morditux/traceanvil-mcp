package daptest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAdapterRejectsUnknownCommandWithExplicitSuccessFalse(t *testing.T) {
	conn, adapter := NewPair()
	t.Cleanup(func() { _ = adapter.Close() })
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetDeadline() error = %v", err)
	}

	payload := `{"seq":1,"type":"request","command":"unknown"}`
	if _, err := fmt.Fprintf(conn, "Content-Length: %d\r\n\r\n%s", len(payload), payload); err != nil {
		t.Fatalf("write request: %v", err)
	}

	reader := bufio.NewReader(conn)
	var contentLength int64 = -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read response header: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("malformed response header %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			contentLength, err = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil {
				t.Fatalf("parse Content-Length: %v", err)
			}
		}
	}
	if contentLength < 0 {
		t.Fatal("missing Content-Length in response")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		t.Fatalf("read response body: %v", err)
	}

	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["type"] != "response" || response["command"] != "unknown" {
		t.Errorf("response = %+v", response)
	}
	success, present := response["success"]
	if !present {
		t.Fatal("response omits success")
	}
	if success != false {
		t.Errorf("success = %v, want false", success)
	}
	if response["message"] == "" {
		t.Error("rejection message is empty")
	}
}
