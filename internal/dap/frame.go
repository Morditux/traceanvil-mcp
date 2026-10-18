package dap

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Bounds applied before allocating a frame or accepting a header block.
const (
	DefaultMaxFrameBytes  int64 = 16 << 20
	DefaultMaxHeaderBytes int64 = 64 << 10
	frameBufferSize             = 4096
)

// wireMessage is the common DAP envelope used for decoding. Bodies stay raw so
// backend-specific payloads are decoded by the caller.
type wireMessage struct {
	Seq        int             `json:"seq"`
	Type       string          `json:"type"`
	RequestSeq int             `json:"request_seq,omitempty"`
	Success    bool            `json:"success,omitempty"`
	Command    string          `json:"command,omitempty"`
	Message    string          `json:"message,omitempty"`
	Event      string          `json:"event,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Body       json.RawMessage `json:"body,omitempty"`
}

type wireRequest struct {
	Seq       int    `json:"seq"`
	Type      string `json:"type"`
	Command   string `json:"command"`
	Arguments any    `json:"arguments,omitempty"`
}

type wireResponse struct {
	Seq        int    `json:"seq"`
	Type       string `json:"type"`
	RequestSeq int    `json:"request_seq"`
	Success    bool   `json:"success"`
	Command    string `json:"command"`
	Message    string `json:"message,omitempty"`
	Body       any    `json:"body,omitempty"`
}

type wireEvent struct {
	Seq   int    `json:"seq"`
	Type  string `json:"type"`
	Event string `json:"event"`
	Body  any    `json:"body,omitempty"`
}

// readMessage reads exactly one Content-Length framed DAP message. Header and
// frame limits are enforced before allocating the body. The Content-Length value
// is a byte count, never a rune count.
func readMessage(reader *bufio.Reader, maxHeader, maxFrame int64) (wireMessage, error) {
	if reader == nil {
		return wireMessage{}, protocolError("nil reader", ErrMalformedFrame)
	}
	if maxHeader <= 0 {
		maxHeader = DefaultMaxHeaderBytes
	}
	if maxFrame <= 0 {
		maxFrame = DefaultMaxFrameBytes
	}

	var (
		headerBytes   int64
		contentLength int64 = -1
	)
	for {
		remaining := maxHeader - headerBytes
		if remaining <= 0 {
			return wireMessage{}, protocolError("header block exceeds limit", ErrHeaderTooLarge)
		}
		line, err := readLineLimited(reader, remaining)
		if err != nil {
			return wireMessage{}, err
		}
		headerBytes += int64(len(line)) + 2
		if headerBytes > maxHeader {
			return wireMessage{}, protocolError("header block exceeds limit", ErrHeaderTooLarge)
		}
		if line == "" {
			break
		}

		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return wireMessage{}, protocolError("header line without a colon", ErrMalformedFrame)
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" {
			return wireMessage{}, protocolError("empty header name", ErrMalformedFrame)
		}
		if !strings.EqualFold(name, "Content-Length") {
			continue
		}
		if contentLength >= 0 {
			return wireMessage{}, protocolError("duplicate Content-Length header", ErrMalformedFrame)
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			return wireMessage{}, protocolError("invalid Content-Length", ErrMalformedFrame)
		}
		contentLength = parsed
	}

	if contentLength < 0 {
		return wireMessage{}, protocolError("missing Content-Length header", ErrMalformedFrame)
	}
	if contentLength > maxFrame {
		return wireMessage{}, protocolError(fmt.Sprintf("frame of %d bytes exceeds limit", contentLength), ErrFrameTooLarge)
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return wireMessage{}, protocolWrap("truncated frame body", ErrMalformedFrame, err)
		}
		return wireMessage{}, protocolWrap("read frame body", ErrMalformedFrame, err)
	}

	var message wireMessage
	if err := json.Unmarshal(body, &message); err != nil {
		return wireMessage{}, protocolWrap("invalid JSON message", ErrMalformedFrame, err)
	}
	if err := validateWireMessage(message); err != nil {
		return wireMessage{}, err
	}
	return message, nil
}

// readLineLimited reads one header line without ever accumulating more than
// limit bytes. bufio.ReadLine bounds each fragment; long lines are rejected
// before they can grow an unbounded allocation.
func readLineLimited(reader *bufio.Reader, limit int64) (string, error) {
	if limit <= 0 {
		return "", protocolError("header block exceeds limit", ErrHeaderTooLarge)
	}
	var line []byte
	for {
		fragment, isPrefix, err := reader.ReadLine()
		if err != nil {
			return "", err
		}
		if int64(len(line)+len(fragment)) > limit {
			return "", protocolError("header line exceeds limit", ErrHeaderTooLarge)
		}
		line = append(line, fragment...)
		if !isPrefix {
			return string(line), nil
		}
	}
}

func validateWireMessage(message wireMessage) error {
	if message.Seq <= 0 {
		return protocolError("message seq must be positive", ErrMalformedFrame)
	}
	switch message.Type {
	case "response":
		if message.RequestSeq <= 0 {
			return protocolError("response request_seq must be positive", ErrMalformedFrame)
		}
	case "event":
		if message.Event == "" {
			return protocolError("event name is empty", ErrMalformedFrame)
		}
	case "request":
		if message.Command == "" {
			return protocolError("request command is empty", ErrMalformedFrame)
		}
	default:
		return protocolError(fmt.Sprintf("unsupported message type %q", message.Type), ErrMalformedFrame)
	}
	return nil
}

// protocolWrap annotates an underlying failure while preserving both the
// protocol sentinel and the cause through errors.Is.
func protocolWrap(reason string, sentinel, cause error) *ProtocolError {
	return &ProtocolError{Reason: reason, Err: errors.Join(sentinel, cause)}
}

// writeFrame writes one Content-Length frame. The caller serializes writers and
// enforces the payload limit; this function only frames bytes.
func writeFrame(writer io.Writer, payload []byte) error {
	if writer == nil {
		return protocolError("nil writer", ErrMalformedFrame)
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(payload))
	if err := writeAll(writer, []byte(header)); err != nil {
		return err
	}
	return writeAll(writer, payload)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if n < 0 || n > len(data) {
			return io.ErrShortWrite
		}
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
