package executor

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

const maxLogLine = 64 << 10

type masker struct {
	values []string
	max    int
	tail   []byte
}

func newMasker() *masker { return &masker{} }

func (m *masker) add(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 4096 || len(m.values) >= 256 {
		return fmt.Errorf("secret mask limit exceeded")
	}
	for _, existing := range m.values {
		if value == existing {
			return nil
		}
	}
	m.values = append(m.values, value)
	sort.Slice(m.values, func(i, j int) bool { return len(m.values[i]) > len(m.values[j]) })
	if len(value) > m.max {
		m.max = len(value)
	}
	return nil
}

func (m *masker) consume(data []byte, final bool) []byte {
	m.tail = append(m.tail, data...)
	end := len(m.tail)
	if !final && m.max > 0 {
		end -= m.max - 1
	}
	if end <= 0 {
		return nil
	}
	var result bytes.Buffer
	index := 0
	for index < end {
		matched := false
		for _, value := range m.values {
			if bytes.HasPrefix(m.tail[index:], []byte(value)) {
				result.WriteString("***")
				index += len(value)
				matched = true
				break
			}
		}
		if !matched {
			result.WriteByte(m.tail[index])
			index++
		}
	}
	copy(m.tail, m.tail[index:])
	m.tail = m.tail[:len(m.tail)-index]
	return result.Bytes()
}

type logSink struct {
	mu      sync.Mutex
	stepID  string
	mask    *masker
	emit    func(string, []byte) error
	cancel  context.CancelFunc
	line    []byte
	err     error
	partial bool
}

func (s *logSink) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	total := len(data)
	for len(data) > 0 {
		length := len(data)
		if length > maxLogLine-len(s.line) {
			length = maxLogLine - len(s.line)
		}
		if newline := bytes.IndexByte(data[:length], '\n'); newline >= 0 {
			length = newline + 1
		}
		s.line = append(s.line, data[:length]...)
		data = data[length:]
		complete := len(s.line) > 0 && s.line[len(s.line)-1] == '\n'
		if complete {
			s.processLine()
			s.partial = false
		} else if len(s.line) == maxLogLine {
			if !s.partial && bytes.HasPrefix(s.line, []byte("::add-mask::")) {
				s.fail(fmt.Errorf("add-mask command exceeds size limit"))
			} else {
				s.output(s.mask.consume(s.line, false))
				s.line = s.line[:0]
				s.partial = true
			}
		}
		if s.err != nil {
			return total - len(data), s.err
		}
	}
	return total, nil
}

func (s *logSink) processLine() {
	if !s.partial && bytes.HasPrefix(s.line, []byte("::add-mask::")) {
		value := strings.TrimSuffix(strings.TrimSuffix(string(s.line[len("::add-mask::"):]), "\n"), "\r")
		value = strings.NewReplacer("%0D", "\r", "%0A", "\n", "%25", "%").Replace(value)
		if err := s.mask.add(value); err != nil {
			s.fail(err)
		}
	} else {
		s.output(s.mask.consume(s.line, false))
	}
	s.line = s.line[:0]
}

func (s *logSink) output(data []byte) {
	if len(data) > 0 && s.emit != nil && s.err == nil {
		if err := s.emit(s.stepID, data); err != nil {
			s.fail(err)
		}
	}
}

func (s *logSink) fail(err error) {
	if s.err == nil {
		s.err = err
		if s.cancel != nil {
			s.cancel()
		}
	}
}

func (s *logSink) finish() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.line) > 0 {
		s.processLine()
	}
	s.output(s.mask.consume(nil, true))
	return s.err
}
