package dap

import (
	"bufio"
	"bytes"
	"testing"
)

func FuzzReadMessage(f *testing.F) {
	seeds := []string{
		"Content-Length: 2\r\n\r\n{}",
		frame(`{"seq":1,"type":"event","event":"initialized"}`),
		frame(`{"seq":1,"type":"response","request_seq":2,"success":true,"command":"initialize"}`),
		"Content-Length: 0\r\n\r\n",
		"Content-Length: 999999999\r\n\r\n",
		"Content-Length: nope\r\n\r\n{}",
		"X: " + string(bytes.Repeat([]byte("a"), 2048)) + "\r\n\r\n",
		string([]byte{0x00, 0xff, '\r', '\n', '\r', '\n'}),
	}
	for _, seed := range seeds {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		reader := bufio.NewReader(bytes.NewReader(data))
		message, err := readMessage(reader, 2048, 4096)
		if err != nil {
			return
		}
		if message.Seq <= 0 {
			t.Fatalf("accepted a message with seq %d", message.Seq)
		}
		switch message.Type {
		case "request", "response", "event":
		default:
			t.Fatalf("accepted a message with type %q", message.Type)
		}
	})
}
