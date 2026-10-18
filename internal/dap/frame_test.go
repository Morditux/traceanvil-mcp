package dap

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func frame(payload string) string {
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(payload), payload)
}

func readFrame(t *testing.T, data string, maxHeader, maxFrame int64) (wireMessage, error) {
	t.Helper()
	reader := bufio.NewReader(strings.NewReader(data))
	return readMessage(reader, maxHeader, maxFrame)
}

func TestReadMessageEnvelopes(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		check   func(*testing.T, wireMessage)
	}{
		{
			name:    "request",
			payload: `{"seq":1,"type":"request","command":"initialize","arguments":{"adapterID":"mock"}}`,
			check: func(t *testing.T, message wireMessage) {
				if message.Seq != 1 || message.Type != "request" || message.Command != "initialize" {
					t.Errorf("message = %+v", message)
				}
				if string(message.Arguments) != `{"adapterID":"mock"}` {
					t.Errorf("arguments = %s", message.Arguments)
				}
			},
		},
		{
			name:    "response",
			payload: `{"seq":2,"type":"response","request_seq":1,"success":true,"command":"initialize","body":{"supportsConfigurationDoneRequest":true}}`,
			check: func(t *testing.T, message wireMessage) {
				if message.Type != "response" || message.RequestSeq != 1 || !message.Success {
					t.Errorf("message = %+v", message)
				}
				var body map[string]bool
				if err := json.Unmarshal(message.Body, &body); err != nil {
					t.Fatal(err)
				}
				if !body["supportsConfigurationDoneRequest"] {
					t.Errorf("body = %+v", body)
				}
			},
		},
		{
			name:    "event",
			payload: `{"seq":3,"type":"event","event":"stopped","body":{"reason":"breakpoint"}}`,
			check: func(t *testing.T, message wireMessage) {
				if message.Type != "event" || message.Event != "stopped" {
					t.Errorf("message = %+v", message)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message, err := readFrame(t, frame(test.payload), 1024, 4096)
			if err != nil {
				t.Fatalf("readMessage() error = %v", err)
			}
			test.check(t, message)
		})
	}
}

func TestReadMessageMultipleFramesAndFragments(t *testing.T) {
	first := frame(`{"seq":1,"type":"event","event":"initialized"}`)
	second := frame(`{"seq":2,"type":"event","event":"stopped"}`)
	data := first + second

	t.Run("multiple frames in one buffer", func(t *testing.T) {
		reader := bufio.NewReader(strings.NewReader(data))
		for i, want := range []string{"initialized", "stopped"} {
			message, err := readMessage(reader, 1024, 4096)
			if err != nil {
				t.Fatalf("frame %d error = %v", i, err)
			}
			if message.Event != want {
				t.Errorf("frame %d event = %q, want %q", i, message.Event, want)
			}
		}
	})

	t.Run("one byte at a time", func(t *testing.T) {
		reader := bufio.NewReader(iotest.OneByteReader(strings.NewReader(data)))
		for i, want := range []string{"initialized", "stopped"} {
			message, err := readMessage(reader, 1024, 4096)
			if err != nil {
				t.Fatalf("frame %d error = %v", i, err)
			}
			if message.Event != want {
				t.Errorf("frame %d event = %q, want %q", i, message.Event, want)
			}
		}
	})
}

func TestReadMessageUTF8UsesByteLength(t *testing.T) {
	payload := `{"seq":1,"type":"event","event":"output","body":{"output":"héllo €"}}`
	data := frame(payload)
	if !strings.HasPrefix(data, fmt.Sprintf("Content-Length: %d", len(payload))) {
		t.Fatalf("frame header does not use the byte length: %q", data)
	}
	message, err := readFrame(t, data, 1024, 4096)
	if err != nil {
		t.Fatalf("readMessage() error = %v", err)
	}
	var body struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(message.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Output != "héllo €" {
		t.Errorf("output = %q", body.Output)
	}
}

func TestReadMessageHeaderVariants(t *testing.T) {
	payload := `{"seq":1,"type":"event","event":"initialized"}`
	tests := []struct {
		name string
		data string
	}{
		{name: "lowercase name", data: fmt.Sprintf("content-length: %d\r\n\r\n%s", len(payload), payload)},
		{name: "unknown header", data: fmt.Sprintf("Content-Type: application/vscode-jsonrpc; charset=utf-8\r\nContent-Length: %d\r\n\r\n%s", len(payload), payload)},
		{name: "lf only", data: fmt.Sprintf("Content-Length: %d\n\n%s", len(payload), payload)},
		{name: "spaces around value", data: fmt.Sprintf("Content-Length:  %d  \r\n\r\n%s", len(payload), payload)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := readFrame(t, test.data, 1024, 4096); err != nil {
				t.Fatalf("readMessage() error = %v", err)
			}
		})
	}
}

func TestReadMessageErrors(t *testing.T) {
	valid := `{"seq":1,"type":"event","event":"initialized"}`
	tests := []struct {
		name      string
		data      string
		maxHeader int64
		maxFrame  int64
		want      error
	}{
		{name: "empty input", data: "", want: io.EOF},
		{name: "missing content length", data: "X-Test: 1\r\n\r\n{}", want: ErrMalformedFrame},
		{name: "malformed header", data: "no colon\r\n\r\n{}", want: ErrMalformedFrame},
		{name: "duplicate content length", data: "Content-Length: 2\r\nContent-Length: 2\r\n\r\n{}", want: ErrMalformedFrame},
		{name: "zero content length", data: "Content-Length: 0\r\n\r\n", want: ErrMalformedFrame},
		{name: "negative content length", data: "Content-Length: -1\r\n\r\n", want: ErrMalformedFrame},
		{name: "non numeric content length", data: "Content-Length: many\r\n\r\n", want: ErrMalformedFrame},
		{name: "frame too large", data: "Content-Length: 100\r\n\r\n{}", maxFrame: 10, want: ErrFrameTooLarge},
		{name: "header too large", data: "X-Padding: " + strings.Repeat("a", 200) + "\r\n\r\n", maxHeader: 20, want: ErrHeaderTooLarge},
		{name: "truncated body", data: "Content-Length: 10\r\n\r\n{}", want: io.ErrUnexpectedEOF},
		{name: "invalid json", data: "Content-Length: 3\r\n\r\n{{{", want: ErrMalformedFrame},
		{name: "missing type", data: frame(`{"seq":1}`), want: ErrMalformedFrame},
		{name: "invalid type", data: frame(`{"seq":1,"type":"bogus"}`), want: ErrMalformedFrame},
		{name: "negative seq", data: frame(`{"seq":-1,"type":"event","event":"initialized"}`), want: ErrMalformedFrame},
		{name: "response without request seq", data: frame(`{"seq":1,"type":"response","success":true,"command":"x"}`), want: ErrMalformedFrame},
		{name: "event without name", data: frame(`{"seq":1,"type":"event"}`), want: ErrMalformedFrame},
		{name: "request without command", data: frame(`{"seq":1,"type":"request"}`), want: ErrMalformedFrame},
		{name: "trailing json", data: frame(valid + `{}`), want: ErrMalformedFrame},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			maxHeader := test.maxHeader
			if maxHeader == 0 {
				maxHeader = 4096
			}
			maxFrame := test.maxFrame
			if maxFrame == 0 {
				maxFrame = 4096
			}
			_, err := readFrame(t, test.data, maxHeader, maxFrame)
			if err == nil {
				t.Fatal("readMessage() error = nil, want an error")
			}
			if !errors.Is(err, test.want) {
				t.Errorf("error = %v, want errors.Is(%v)", err, test.want)
			}
		})
	}
}

func TestWriteFrame(t *testing.T) {
	var buffer bytes.Buffer
	payload := []byte(`{"seq":1,"type":"request","command":"initialize","arguments":{"adapterID":"mock"}}`)
	if err := writeFrame(&buffer, payload); err != nil {
		t.Fatalf("writeFrame() error = %v", err)
	}
	got := buffer.String()
	wantHeader := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(payload))
	if !strings.HasPrefix(got, wantHeader) {
		t.Fatalf("frame = %q, want header %q", got, wantHeader)
	}
	if got[len(wantHeader):] != string(payload) {
		t.Errorf("payload = %q", got[len(wantHeader):])
	}

	message, err := readFrame(t, got, 4096, 4096)
	if err != nil {
		t.Fatalf("readFrame() error = %v", err)
	}
	if message.Command != "initialize" {
		t.Errorf("message = %+v", message)
	}
}

func TestWriteFrameUTF8ByteLength(t *testing.T) {
	var buffer bytes.Buffer
	payload := []byte(`{"seq":1,"type":"event","event":"output","body":{"output":"€"}}`)
	if err := writeFrame(&buffer, payload); err != nil {
		t.Fatal(err)
	}
	wantHeader := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(payload))
	if !strings.HasPrefix(buffer.String(), wantHeader) {
		t.Errorf("frame = %q, want %q", buffer.String(), wantHeader)
	}
}

func TestWriteFrameShortWritesAndWriterFailures(t *testing.T) {
	payload := []byte(`{"seq":1,"type":"event","event":"initialized"}`)
	var buffer bytes.Buffer
	if err := writeFrame(&oneByteWriter{writer: &buffer}, payload); err != nil {
		t.Fatalf("writeFrame(one byte) error = %v", err)
	}
	if !strings.HasSuffix(buffer.String(), string(payload)) {
		t.Errorf("frame = %q", buffer.String())
	}

	if err := writeFrame(&zeroWriter{}, payload); !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("writeFrame(zero writer) error = %v, want ErrShortWrite", err)
	}
	if err := writeFrame(&errorWriter{}, payload); err == nil {
		t.Error("writeFrame(error writer) error = nil")
	}
}

type oneByteWriter struct {
	writer io.Writer
}

func (w *oneByteWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return w.writer.Write(p[:1])
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestReadMessageAcceptsZeroSequenceFromLLDB(t *testing.T) {
	message, err := readFrame(t, frame(`{"seq":0,"type":"event","event":"initialized"}`), 1024, 4096)
	if err != nil {
		t.Fatalf("readMessage() error = %v", err)
	}
	if message.Seq != 0 || message.Event != "initialized" {
		t.Fatalf("message = %+v", message)
	}
}
