package valgrind

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ParserVersion versions the XML normalization behaviour.
const ParserVersion = "valgrind-xml-v1"

// Parser limits bound untrusted Valgrind XML. They are deliberately below the
// artifact quota: a single record can never force the whole report in memory.
const (
	DefaultMaxXMLBytes      int64 = 8 << 20
	DefaultMaxDiagnostics         = 10000
	DefaultMaxFrames              = 128
	DefaultMaxFramesTotal         = 65536
	DefaultMaxContextStacks       = 8
	DefaultMaxStringBytes         = 4096
	DefaultMaxSuppressions        = 512
	DefaultMaxDepth               = 64
)

// ParseOptions bounds one parse. Zero values select the documented defaults.
type ParseOptions struct {
	MaxBytes         int64
	MaxDiagnostics   int
	MaxFrames        int
	MaxFramesTotal   int
	MaxContextStacks int
	MaxStringBytes   int
	MaxSuppressions  int
	MaxDepth         int
}

func (o ParseOptions) withDefaults() ParseOptions {
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxXMLBytes
	}
	if o.MaxDiagnostics <= 0 {
		o.MaxDiagnostics = DefaultMaxDiagnostics
	}
	if o.MaxFrames <= 0 {
		o.MaxFrames = DefaultMaxFrames
	}
	if o.MaxFramesTotal <= 0 {
		o.MaxFramesTotal = DefaultMaxFramesTotal
	}
	if o.MaxContextStacks <= 0 {
		o.MaxContextStacks = DefaultMaxContextStacks
	}
	if o.MaxStringBytes <= 0 {
		o.MaxStringBytes = DefaultMaxStringBytes
	}
	if o.MaxSuppressions <= 0 {
		o.MaxSuppressions = DefaultMaxSuppressions
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = DefaultMaxDepth
	}
	return o
}

// ParseError reports a structural or syntax failure while parsing XML. The
// partial XMLResult returned alongside it is still useful evidence.
type ParseError struct {
	Reason string
	Offset int64
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("valgrind: xml at byte %d: %s", e.Offset, e.Reason)
}

// XMLResult is the raw, bounded view of one Valgrind XML document.
type XMLResult struct {
	ProtocolVersion    int
	ProtocolTool       string
	Tool               string
	Preamble           []string
	PID                int
	PPID               int
	VArgv              []string
	Argv               []string
	Errors             []XMLError
	DroppedDiagnostics int
	ErrorCounts        []XMLErrorCount
	Suppressions       []XMLSuppression
	ErrorSummary       *XMLErrorSummary
	HeapSummary        *XMLHeapSummary
	AllHeapBlocksFree  *bool
	FinalState         string
	Complete           bool
	Truncated          bool
	// Invalid is set when a field held malformed data; Complete is false.
	Invalid   bool
	Warnings  []string
	BytesRead int64
}

// XMLError is one raw <error> element.
type XMLError struct {
	Unique       string
	TID          string
	Kind         string
	What         string
	XWhat        string
	LeakedBytes  int64
	LeakedBlocks int64
	Stack        []XMLFrame
	Contexts     []XMLContextStack
	Origin       []XMLFrame
	Auxiliary    []string
}

// XMLContextStack is an additional <stack> element (for example an allocation
// site following an <auxwhat>).
type XMLContextStack struct {
	Label  string
	Frames []XMLFrame
}

// XMLFrame is one raw <frame> element. Addresses (ip) are intentionally not
// retained: they are not stable evidence and are never used by fingerprints.
type XMLFrame struct {
	Function string
	Object   string
	Dir      string
	File     string
	Line     int
}

// XMLErrorCount is one <errorcounts><pair> entry.
type XMLErrorCount struct {
	Unique string
	Count  int
}

// XMLSuppression is one <suppcounts><pair> entry.
type XMLSuppression struct {
	Name   string
	Unique string
	Count  int
}

// XMLErrorSummary is Valgrind's own <error_summary>.
type XMLErrorSummary struct {
	Errors             int
	ErrorContexts      int
	Suppressed         int
	SuppressedContexts int
}

// XMLHeapSummary is the Memcheck <heap_summary> block.
type XMLHeapSummary struct {
	InUseBytes     int64
	InUseBlocks    int64
	Allocs         int64
	Frees          int64
	BytesAllocated int64
}

// Parse consumes a Valgrind XML document token by token. It returns a partial
// result together with a ParseError when the document is truncated, malformed
// or exceeds a structural limit. Callers must not treat a partial result as a
// complete analysis.
func Parse(r io.Reader, opts ParseOptions) (*XMLResult, error) {
	if r == nil {
		return nil, errors.New("valgrind: nil XML reader")
	}
	opts = opts.withDefaults()

	result := &XMLResult{}
	p := &parser{
		opts:   opts,
		result: result,
		seen:   make(map[string]struct{}),
	}

	counted := &countingReader{reader: r}
	limited := io.LimitReader(counted, opts.MaxBytes+1)
	decoder := xml.NewDecoder(limited)
	decoder.Strict = true

	rootSeen := false
	rootClosed := false
	var parseErr error

loop:
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			result.Truncated = true
			parseErr = &ParseError{Reason: err.Error(), Offset: decoder.InputOffset()}
			break
		}

		switch value := token.(type) {
		case xml.StartElement:
			if len(p.path) == 0 {
				if rootSeen {
					parseErr = &ParseError{Reason: "multiple root elements", Offset: decoder.InputOffset()}
					result.Truncated = true
					break loop
				}
				if value.Name.Local != "valgrindoutput" {
					parseErr = &ParseError{
						Reason: fmt.Sprintf("unexpected root element %q", value.Name.Local),
						Offset: decoder.InputOffset(),
					}
					result.Truncated = true
					break loop
				}
				rootSeen = true
			}
			if rootClosed {
				parseErr = &ParseError{Reason: "content after </valgrindoutput>", Offset: decoder.InputOffset()}
				result.Truncated = true
				break loop
			}
			if len(p.path) >= opts.MaxDepth {
				p.markTruncated("xml depth limit reached")
				parseErr = &ParseError{Reason: "element nesting limit reached", Offset: decoder.InputOffset()}
				break loop
			}
			p.path = append(p.path, value.Name.Local)
			p.startElement()
		case xml.EndElement:
			if len(p.path) == 0 {
				parseErr = &ParseError{Reason: "unexpected end element", Offset: decoder.InputOffset()}
				result.Truncated = true
				break loop
			}
			if p.path[len(p.path)-1] != value.Name.Local {
				parseErr = &ParseError{
					Reason: fmt.Sprintf("mismatched end element %q", value.Name.Local),
					Offset: decoder.InputOffset(),
				}
				result.Truncated = true
				break loop
			}
			p.endElement()
			p.path = p.path[:len(p.path)-1]
			if len(p.path) == 0 {
				rootClosed = true
			}
		case xml.CharData:
			if rootClosed {
				if strings.TrimSpace(string(value)) != "" {
					parseErr = &ParseError{Reason: "content after </valgrindoutput>", Offset: decoder.InputOffset()}
					result.Truncated = true
					break loop
				}
				continue
			}
			p.charData(string(value))
		}
	}

	result.BytesRead = counted.read
	if counted.read > opts.MaxBytes {
		p.markTruncated("xml byte limit reached")
	}
	if parseErr != nil {
		result.Complete = false
	} else if !rootSeen {
		result.Complete = false
		parseErr = &ParseError{Reason: "empty document", Offset: 0}
	} else if !rootClosed {
		result.Complete = false
		parseErr = &ParseError{Reason: "missing </valgrindoutput> (truncated document)", Offset: counted.read}
	} else {
		result.Complete = !result.Truncated && !result.Invalid
	}
	return result, parseErr
}

type parser struct {
	opts   ParseOptions
	result *XMLResult
	path   []string
	seen   map[string]struct{}

	currentError *XMLError
	inError      bool
	skipError    bool

	currentFrame *XMLFrame
	currentStack *[]XMLFrame
	inOrigin     bool
	framesStored int

	pendingAux string
	auxText    string

	currentErrorCount *XMLErrorCount
	currentSuppCount  *XMLSuppression
}

// markInvalid records malformed data. The document is incomplete, but it was
// not cut short, so Truncated stays unset and the value is reported as bad.
func (p *parser) markInvalid(reason string) {
	p.result.Invalid = true
	if _, exists := p.seen[reason]; exists {
		return
	}
	p.seen[reason] = struct{}{}
	p.result.Warnings = append(p.result.Warnings, reason)
}

func (p *parser) markTruncated(reason string) {
	p.result.Truncated = true
	if _, exists := p.seen[reason]; exists {
		return
	}
	p.seen[reason] = struct{}{}
	p.result.Warnings = append(p.result.Warnings, reason)
}

func (p *parser) appendText(target *string, value string) {
	if value == "" {
		return
	}
	remaining := p.opts.MaxStringBytes - len(*target)
	if remaining <= 0 {
		p.markTruncated("xml string limit reached")
		return
	}
	if len(value) > remaining {
		value = value[:remaining]
		for len(value) > 0 {
			last := value[len(value)-1]
			if last&0xC0 == 0x80 {
				value = value[:len(value)-1]
				continue
			}
			if last >= 0xC0 {
				// Incomplete leading byte at the cut point.
				value = value[:len(value)-1]
			}
			break
		}
		p.markTruncated("xml string limit reached")
	}
	*target += value
}

func (p *parser) startElement() {
	name := p.path[len(p.path)-1]

	if len(p.path) == 2 && p.path[0] == "valgrindoutput" && name == "error" {
		p.inError = true
		if len(p.result.Errors) >= p.opts.MaxDiagnostics {
			p.skipError = true
			p.result.DroppedDiagnostics++
			p.markTruncated("diagnostic limit reached")
			return
		}
		p.skipError = false
		p.currentError = &XMLError{}
		return
	}

	if p.inError {
		p.startErrorElement(name)
		return
	}

	switch {
	case len(p.path) == 3 && p.path[0] == "valgrindoutput" && p.path[1] == "errorcounts" && name == "pair":
		p.currentErrorCount = &XMLErrorCount{}
	case len(p.path) == 3 && p.path[0] == "valgrindoutput" && p.path[1] == "suppcounts" && name == "pair":
		if len(p.result.Suppressions) >= p.opts.MaxSuppressions {
			p.markTruncated("suppression limit reached")
			return
		}
		p.currentSuppCount = &XMLSuppression{}
	}
}

func (p *parser) startErrorElement(name string) {
	if p.skipError {
		return
	}
	depth := len(p.path)

	switch {
	case name == "stack" && (depth == 3 || (depth == 4 && p.path[2] == "origin")):
		stack := make([]XMLFrame, 0, 16)
		switch {
		case p.inOrigin:
			p.currentError.Origin = stack
			p.currentStack = &p.currentError.Origin
		case depth == 3 && p.currentError.Stack == nil:
			p.currentError.Stack = stack
			p.currentStack = &p.currentError.Stack
		case depth == 3:
			if len(p.currentError.Contexts) >= p.opts.MaxContextStacks {
				p.markTruncated("context stack limit reached")
				p.currentStack = nil
				return
			}
			p.currentError.Contexts = append(p.currentError.Contexts, XMLContextStack{Label: p.pendingAux})
			p.pendingAux = ""
			p.currentStack = &p.currentError.Contexts[len(p.currentError.Contexts)-1].Frames
		}
	case name == "origin" && depth == 3:
		p.inOrigin = true
	case name == "frame" && depth >= 4 && p.path[depth-2] == "stack":
		if p.currentStack == nil {
			return
		}
		if len(*p.currentStack) >= p.opts.MaxFrames || p.framesStored >= p.opts.MaxFramesTotal {
			p.markTruncated("stack frame limit reached")
			p.currentFrame = nil
			return
		}
		p.currentFrame = &XMLFrame{}
	}
}

func (p *parser) endElement() {
	name := p.path[len(p.path)-1]
	depth := len(p.path)

	if p.inError {
		if depth == 2 && name == "error" {
			if !p.skipError && p.currentError != nil {
				p.result.Errors = append(p.result.Errors, *p.currentError)
			}
			p.currentError = nil
			p.inError = false
			p.skipError = false
			p.currentStack = nil
			p.currentFrame = nil
			p.inOrigin = false
			p.pendingAux = ""
			return
		}
		if p.skipError {
			return
		}
		switch name {
		case "frame":
			if p.currentFrame != nil && p.currentStack != nil {
				*p.currentStack = append(*p.currentStack, *p.currentFrame)
				p.framesStored++
			}
			p.currentFrame = nil
		case "stack":
			p.currentStack = nil
		case "auxwhat":
			if text := strings.TrimSpace(p.auxText); text != "" {
				p.currentError.Auxiliary = append(p.currentError.Auxiliary, text)
				p.pendingAux = text
			}
			p.auxText = ""
		case "origin":
			p.inOrigin = false
		}
		return
	}

	if depth == 3 && p.path[0] == "valgrindoutput" && p.path[2] == "pair" {
		switch p.path[1] {
		case "errorcounts":
			if p.currentErrorCount != nil {
				p.result.ErrorCounts = append(p.result.ErrorCounts, *p.currentErrorCount)
				p.currentErrorCount = nil
			}
		case "suppcounts":
			if p.currentSuppCount != nil {
				p.result.Suppressions = append(p.result.Suppressions, *p.currentSuppCount)
				p.currentSuppCount = nil
			}
		}
	}
}

func (p *parser) charData(value string) {
	if p.skipError {
		return
	}
	if p.inError && p.currentError != nil {
		if p.currentFrame != nil {
			p.charDataFrame(value)
			return
		}
		p.charDataError(value)
		return
	}
	p.charDataDocument(value)
}

func (p *parser) charDataError(value string) {
	depth := len(p.path)
	switch {
	case depth == 3:
		switch p.path[2] {
		case "unique":
			p.appendText(&p.currentError.Unique, strings.TrimSpace(value))
		case "tid":
			p.appendText(&p.currentError.TID, strings.TrimSpace(value))
		case "kind":
			p.appendText(&p.currentError.Kind, strings.TrimSpace(value))
		case "what":
			p.appendText(&p.currentError.What, strings.TrimSpace(value))
		case "auxwhat":
			p.appendText(&p.auxText, strings.TrimSpace(value))
		}
	case depth == 4 && p.path[2] == "xwhat":
		switch p.path[3] {
		case "text":
			p.appendText(&p.currentError.XWhat, strings.TrimSpace(value))
		case "leakedbytes":
			p.currentError.LeakedBytes = p.textInt(value, "xwhat.leakedbytes")
		case "leakedblocks":
			p.currentError.LeakedBlocks = p.textInt(value, "xwhat.leakedblocks")
		}
	}
}

func (p *parser) charDataDocument(value string) {
	depth := len(p.path)
	if depth < 2 || p.path[0] != "valgrindoutput" {
		return
	}

	if depth == 2 {
		switch p.path[1] {
		case "protocolversion":
			p.result.ProtocolVersion = int(p.textInt(value, "protocolversion"))
		case "protocoltool":
			p.appendText(&p.result.ProtocolTool, strings.TrimSpace(value))
		case "tool":
			p.appendText(&p.result.Tool, strings.TrimSpace(value))
		case "pid":
			p.result.PID = int(p.textInt(value, "pid"))
		case "ppid":
			p.result.PPID = int(p.textInt(value, "ppid"))
		case "all_heap_blocks_freed":
			flag := strings.TrimSpace(value) == "true"
			p.result.AllHeapBlocksFree = &flag
		}
		return
	}

	if depth == 3 {
		switch p.path[1] {
		case "preamble":
			if p.path[2] == "line" {
				line := strings.TrimSpace(value)
				if line != "" && len(p.result.Preamble) < 64 {
					p.result.Preamble = append(p.result.Preamble, line)
				}
			}
		case "status":
			if p.path[2] == "state" {
				p.result.FinalState = strings.TrimSpace(value)
			}
		case "error_summary":
			summary := p.ensureErrorSummary()
			switch p.path[2] {
			case "errors":
				summary.Errors = int(p.textInt(value, "error_summary.errors"))
			case "error_contexts":
				summary.ErrorContexts = int(p.textInt(value, "error_summary.error_contexts"))
			case "suppressed":
				summary.Suppressed = int(p.textInt(value, "error_summary.suppressed"))
			case "suppressed_contexts":
				summary.SuppressedContexts = int(p.textInt(value, "error_summary.suppressed_contexts"))
			}
		}
		return
	}

	if depth == 4 {
		switch p.path[1] {
		case "args":
			p.charDataArgs(value)
		case "errorcounts":
			if p.currentErrorCount != nil {
				switch p.path[3] {
				case "count":
					p.currentErrorCount.Count = int(p.textInt(value, "errorcounts.count"))
				case "unique":
					p.appendText(&p.currentErrorCount.Unique, strings.TrimSpace(value))
				}
			}
		case "suppcounts":
			if p.currentSuppCount != nil {
				switch p.path[3] {
				case "count":
					p.currentSuppCount.Count = int(p.textInt(value, "suppcounts.count"))
				case "name":
					p.appendText(&p.currentSuppCount.Name, strings.TrimSpace(value))
				case "unique":
					p.appendText(&p.currentSuppCount.Unique, strings.TrimSpace(value))
				}
			}
		case "heap_summary":
			p.charDataHeap(value)
		}
	}
}

func (p *parser) charDataArgs(value string) {
	if len(p.path) != 4 {
		return
	}
	if p.path[3] != "exe" && p.path[3] != "arg" {
		return
	}
	switch p.path[2] {
	case "vargv":
		if len(p.result.VArgv) >= 256 {
			p.markTruncated("argv limit reached")
			return
		}
		p.result.VArgv = append(p.result.VArgv, value)
	case "argv":
		if len(p.result.Argv) >= 256 {
			p.markTruncated("argv limit reached")
			return
		}
		p.result.Argv = append(p.result.Argv, value)
	}
}

func (p *parser) charDataFrame(value string) {
	if p.currentFrame == nil || len(p.path) < 5 {
		return
	}
	if p.path[len(p.path)-2] != "frame" {
		return
	}
	switch p.path[len(p.path)-1] {
	case "obj":
		p.appendText(&p.currentFrame.Object, strings.TrimSpace(value))
	case "fn":
		p.appendText(&p.currentFrame.Function, strings.TrimSpace(value))
	case "dir":
		p.appendText(&p.currentFrame.Dir, strings.TrimSpace(value))
	case "file":
		p.appendText(&p.currentFrame.File, strings.TrimSpace(value))
	case "line":
		p.currentFrame.Line = int(p.textInt(value, "frame.line"))
	}
}

func (p *parser) charDataHeap(value string) {
	if p.result.HeapSummary == nil {
		p.result.HeapSummary = &XMLHeapSummary{}
	}
	heap := p.result.HeapSummary
	switch strings.Join(p.path[2:], "/") {
	case "memory_in_use_at_exit/bytes":
		heap.InUseBytes = p.textInt(value, "heap_summary.bytes")
	case "memory_in_use_at_exit/blocks":
		heap.InUseBlocks = p.textInt(value, "heap_summary.blocks")
	case "total_heap_usage/allocs":
		heap.Allocs = p.textInt(value, "heap_summary.allocs")
	case "total_heap_usage/frees":
		heap.Frees = p.textInt(value, "heap_summary.frees")
	case "total_heap_usage/bytes_allocated":
		heap.BytesAllocated = p.textCommaInt(value, "heap_summary.bytes_allocated")
	}
}

func (p *parser) ensureErrorSummary() *XMLErrorSummary {
	if p.result.ErrorSummary == nil {
		p.result.ErrorSummary = &XMLErrorSummary{}
	}
	return p.result.ErrorSummary
}

func (p *parser) textInt(raw string, field string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		p.markInvalid("invalid numeric field " + field)
		return 0
	}
	return value
}

func (p *parser) textCommaInt(raw string, field string) int64 {
	return p.textInt(strings.ReplaceAll(strings.TrimSpace(raw), ",", ""), field)
}

// countingReader records how many bytes the decoder consumed so byte-limit
// truncation can be reported without reading the file twice.
type countingReader struct {
	reader io.Reader
	read   int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += int64(n)
	return n, err
}
