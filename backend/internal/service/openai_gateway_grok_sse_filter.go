package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

const (
	grokResponsesPingMaxLines = 16
	grokResponsesPingMaxBytes = 16 * 1024
)

type grokResponsesPingFilterBody struct {
	*io.PipeReader
	source    io.Closer
	closeOnce sync.Once
	closeErr  error
}

func newGrokResponsesPingFilterBody(source io.ReadCloser, account *Account, maxLineSize int) io.ReadCloser {
	if source == nil || account == nil || !account.IsGrok() {
		return source
	}
	reader, writer := io.Pipe()
	body := &grokResponsesPingFilterBody{PipeReader: reader, source: source}
	go filterGrokResponsesPings(source, writer, body.closeSource, maxLineSize)
	return body
}

func (b *grokResponsesPingFilterBody) Close() error {
	readerErr := b.PipeReader.Close()
	sourceErr := b.closeSource()
	if readerErr != nil {
		return readerErr
	}
	return sourceErr
}

func (b *grokResponsesPingFilterBody) closeSource() error {
	b.closeOnce.Do(func() { b.closeErr = b.source.Close() })
	return b.closeErr
}

func filterGrokResponsesPings(source io.Reader, destination *io.PipeWriter, closeSource func() error, maxLineSize int) {
	defer func() { _ = closeSource() }()
	if maxLineSize <= 0 {
		maxLineSize = defaultMaxLineSize
	}
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 64*1024), maxLineSize)
	scanner.Split(scanGrokSSELines)
	pingFrame := make([][]byte, 0, 3)
	pingBytes := 0
	passthrough := false
	replay := func() error {
		for _, line := range pingFrame {
			if _, err := destination.Write(line); err != nil {
				return err
			}
		}
		pingFrame = pingFrame[:0]
		pingBytes = 0
		return nil
	}
	finish := func(blank []byte) error {
		if isGrokPingFrame(pingFrame) {
			pingFrame = pingFrame[:0]
			pingBytes = 0
			_, err := destination.Write([]byte(": ping\n\n"))
			return err
		}
		if err := replay(); err != nil {
			return err
		}
		if blank != nil {
			_, err := destination.Write(blank)
			return err
		}
		return nil
	}
	abort := func(err error) { _ = destination.CloseWithError(err) }

	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		blank := len(bytes.TrimSpace(trimGrokSSELineEnding(line))) == 0
		if passthrough {
			if _, err := destination.Write(line); err != nil {
				abort(err)
				return
			}
			if blank {
				passthrough = false
			}
			continue
		}
		if len(pingFrame) > 0 {
			if blank {
				if err := finish(line); err != nil {
					abort(err)
					return
				}
				continue
			}
			if canExtendGrokPingFrame(line) && len(pingFrame) < grokResponsesPingMaxLines && pingBytes+len(line) <= grokResponsesPingMaxBytes {
				pingFrame = append(pingFrame, line)
				pingBytes += len(line)
				continue
			}
			if err := replay(); err != nil {
				abort(err)
				return
			}
			if _, err := destination.Write(line); err != nil {
				abort(err)
				return
			}
			passthrough = true
			continue
		}
		if !blank {
			if event, ok := extractGrokSSEEventLine(line); ok && event == "ping" {
				pingFrame = append(pingFrame, line)
				pingBytes = len(line)
				continue
			}
		}
		if _, err := destination.Write(line); err != nil {
			abort(err)
			return
		}
		passthrough = !blank
	}
	if len(pingFrame) > 0 {
		if err := finish(nil); err != nil {
			abort(err)
			return
		}
	}
	if err := scanner.Err(); err != nil {
		abort(fmt.Errorf("filter Grok Responses ping: %w", err))
		return
	}
	_ = destination.Close()
}

func scanGrokSSELines(data []byte, atEOF bool) (int, []byte, error) {
	for index, value := range data {
		if value == '\n' {
			return index + 1, data[:index+1], nil
		}
		if value == '\r' {
			if index+1 == len(data) && !atEOF {
				return 0, nil, nil
			}
			if index+1 < len(data) && data[index+1] == '\n' {
				return index + 2, data[:index+2], nil
			}
			return index + 1, data[:index+1], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func trimGrokSSELineEnding(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte("\n"))
	return bytes.TrimSuffix(line, []byte("\r"))
}

func extractGrokSSEEventLine(line []byte) (string, bool) {
	line = trimGrokSSELineEnding(line)
	if !bytes.HasPrefix(line, []byte("event:")) {
		return "", false
	}
	return strings.TrimSpace(string(line[len("event:"):])), true
}

func canExtendGrokPingFrame(line []byte) bool {
	line = trimGrokSSELineEnding(line)
	if len(line) > 0 && line[0] == ':' {
		return true
	}
	_, ok := extractGrokSSEDataLine(line)
	return ok
}

func extractGrokSSEDataLine(line []byte) (string, bool) {
	line = trimGrokSSELineEnding(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return "", false
	}
	line = line[len("data:"):]
	line = bytes.TrimLeft(line, " \t")
	return string(line), true
}

func isGrokPingFrame(lines [][]byte) bool {
	parts := make([]string, 0, len(lines))
	for _, line := range lines[1:] {
		if value, ok := extractGrokSSEDataLine(line); ok {
			parts = append(parts, value)
		}
	}
	if len(parts) == 0 {
		return true
	}
	var payload struct {
		Type *string `json:"type"`
	}
	if err := json.Unmarshal([]byte(strings.Join(parts, "\n")), &payload); err != nil || payload.Type == nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(*payload.Type), "ping")
}
