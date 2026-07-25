package kiro

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"log"
)

const (
	awsEventStreamPreludeSize = 12
	awsEventStreamMinSize     = 16
)

type awsEventStreamFrame struct {
	headers map[string]string
	payload []byte
}

func (f awsEventStreamFrame) header(name string) string {
	return f.headers[name]
}

func (p *AwsEventStreamParser) parseBuffer() []StreamEvent {
	if p.terminalError {
		return nil
	}

	var events []StreamEvent
	consumed := 0

	for len(p.buffer)-consumed >= awsEventStreamPreludeSize {
		frameData := p.buffer[consumed:]
		totalLength := int(binary.BigEndian.Uint32(frameData[0:4]))
		headersLength := int(binary.BigEndian.Uint32(frameData[4:8]))

		if totalLength < awsEventStreamMinSize || totalLength > p.maxBufferSize {
			return append(events, p.failEventStream(
				"event_stream_decode_error",
				fmt.Errorf("invalid frame length %d", totalLength),
			)...)
		}
		if headersLength < 0 || awsEventStreamPreludeSize+headersLength+4 > totalLength {
			return append(events, p.failEventStream(
				"event_stream_decode_error",
				fmt.Errorf("invalid headers length %d for frame length %d", headersLength, totalLength),
			)...)
		}

		expectedPreludeCRC := binary.BigEndian.Uint32(frameData[8:12])
		actualPreludeCRC := crc32.ChecksumIEEE(frameData[:8])
		if actualPreludeCRC != expectedPreludeCRC {
			return append(events, p.failEventStream(
				"event_stream_decode_error",
				fmt.Errorf("prelude CRC mismatch: expected %08x, got %08x", expectedPreludeCRC, actualPreludeCRC),
			)...)
		}

		if len(frameData) < totalLength {
			break
		}

		frameBytes := frameData[:totalLength]
		expectedMessageCRC := binary.BigEndian.Uint32(frameBytes[totalLength-4:])
		actualMessageCRC := crc32.ChecksumIEEE(frameBytes[:totalLength-4])
		if actualMessageCRC != expectedMessageCRC {
			return append(events, p.failEventStream(
				"event_stream_decode_error",
				fmt.Errorf("message CRC mismatch: expected %08x, got %08x", expectedMessageCRC, actualMessageCRC),
			)...)
		}

		frame, err := decodeAWSEventStreamFrame(frameBytes, headersLength)
		if err != nil {
			return append(events, p.failEventStream("event_stream_decode_error", err)...)
		}

		parsed, err := p.parseFrame(frame)
		if err != nil {
			return append(events, p.failEventStream("event_stream_decode_error", err)...)
		}
		events = append(events, parsed...)
		consumed += totalLength
	}

	if consumed > 0 {
		p.buffer = p.buffer[consumed:]
	}
	return events
}

func (p *AwsEventStreamParser) failEventStream(errorType string, err error) []StreamEvent {
	if p.terminalError {
		return nil
	}
	if errorType == "" {
		errorType = "event_stream_decode_error"
	}
	p.parseErrorCount++
	p.terminalError = true
	p.buffer = nil
	log.Printf("[kiro-parser] event stream decode error: %v", err)
	return []StreamEvent{{
		Type:         EventError,
		ErrorType:    errorType,
		ErrorMessage: err.Error(),
	}}
}

func decodeAWSEventStreamFrame(frameBytes []byte, headersLength int) (awsEventStreamFrame, error) {
	headersStart := awsEventStreamPreludeSize
	headersEnd := headersStart + headersLength
	if headersEnd > len(frameBytes)-4 {
		return awsEventStreamFrame{}, fmt.Errorf("headers exceed frame boundary")
	}

	headers, err := parseAWSEventStreamHeaders(frameBytes[headersStart:headersEnd])
	if err != nil {
		return awsEventStreamFrame{}, err
	}

	payload := make([]byte, len(frameBytes[headersEnd:len(frameBytes)-4]))
	copy(payload, frameBytes[headersEnd:len(frameBytes)-4])
	return awsEventStreamFrame{headers: headers, payload: payload}, nil
}

func parseAWSEventStreamHeaders(data []byte) (map[string]string, error) {
	headers := make(map[string]string)
	for offset := 0; offset < len(data); {
		nameLength := int(data[offset])
		offset++
		if nameLength == 0 || offset+nameLength+1 > len(data) {
			return nil, fmt.Errorf("invalid event stream header name length %d", nameLength)
		}

		name := string(data[offset : offset+nameLength])
		offset += nameLength
		valueType := data[offset]
		offset++

		value, nextOffset, err := parseAWSEventStreamHeaderValue(data, offset, valueType)
		if err != nil {
			return nil, fmt.Errorf("header %q: %w", name, err)
		}
		offset = nextOffset
		if value != nil {
			headers[name] = *value
		}
	}
	return headers, nil
}

func parseAWSEventStreamHeaderValue(data []byte, offset int, valueType byte) (*string, int, error) {
	require := func(size int) error {
		if size < 0 || offset+size > len(data) {
			return fmt.Errorf("truncated value")
		}
		return nil
	}

	switch valueType {
	case 0, 1:
		return nil, offset, nil
	case 2:
		if err := require(1); err != nil {
			return nil, offset, err
		}
		return nil, offset + 1, nil
	case 3:
		if err := require(2); err != nil {
			return nil, offset, err
		}
		return nil, offset + 2, nil
	case 4:
		if err := require(4); err != nil {
			return nil, offset, err
		}
		return nil, offset + 4, nil
	case 5, 8:
		if err := require(8); err != nil {
			return nil, offset, err
		}
		return nil, offset + 8, nil
	case 6, 7:
		if err := require(2); err != nil {
			return nil, offset, err
		}
		valueLength := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		valueStart := offset + 2
		if valueStart+valueLength > len(data) {
			return nil, offset, fmt.Errorf("truncated length-prefixed value")
		}
		if valueType == 7 {
			value := string(data[valueStart : valueStart+valueLength])
			return &value, valueStart + valueLength, nil
		}
		return nil, valueStart + valueLength, nil
	case 9:
		if err := require(16); err != nil {
			return nil, offset, err
		}
		return nil, offset + 16, nil
	default:
		return nil, offset, fmt.Errorf("unsupported value type %d", valueType)
	}
}

func (p *AwsEventStreamParser) parseFrame(frame awsEventStreamFrame) ([]StreamEvent, error) {
	messageType := frame.header(":message-type")
	if messageType == "" {
		messageType = "event"
	}

	switch messageType {
	case "event":
		return p.parseJSONEvent(frame.header(":event-type"), frame.payload)
	case "error":
		return p.buildUpstreamError(frame.header(":error-code"), frame.payload), nil
	case "exception":
		return p.buildUpstreamError(frame.header(":exception-type"), frame.payload), nil
	default:
		return nil, fmt.Errorf("unsupported message type %q", messageType)
	}
}

func (p *AwsEventStreamParser) buildUpstreamError(errorType string, payload []byte) []StreamEvent {
	if errorType == "" {
		errorType = "upstream_error"
	}
	errorMessage := extractAWSEventStreamErrorMessage(payload)
	log.Printf("[kiro-parser] upstream error: type=%s message=%s", errorType, errorMessage)

	events := p.flushThinkingBeforeToolUse()
	if p.thinkingBlockIndex != nil {
		events = append(events, StreamEvent{Type: EventContentBlockStop, Index: *p.thinkingBlockIndex})
		p.thinkingBlockIndex = nil
		p.inThinkingBlock = false
		p.stripThinkingLeadingNewline = false
	}
	if p.textBlockIndex != nil {
		events = append(events, StreamEvent{Type: EventContentBlockStop, Index: *p.textBlockIndex})
		p.textBlockIndex = nil
		p.inTextBlock = false
	}
	for toolID, accumulator := range p.toolAccumulators {
		if accumulator.started {
			events = append(events,
				StreamEvent{Type: EventToolUseStop, ToolID: toolID},
				StreamEvent{Type: EventContentBlockStop, Index: accumulator.blockIndex},
			)
		}
	}
	p.toolAccumulators = make(map[string]*toolAccumulator)

	return append(events, StreamEvent{Type: EventError, ErrorType: errorType, ErrorMessage: errorMessage})
}

func extractAWSEventStreamErrorMessage(payload []byte) string {
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err == nil {
		for _, key := range []string{"message", "Message", "errorMessage"} {
			if value, ok := body[key].(string); ok && value != "" {
				return value
			}
		}
	}
	if len(payload) == 0 {
		return "upstream event stream error"
	}
	return string(payload)
}
