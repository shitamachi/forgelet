package executor

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMaskAcrossEveryChunkBoundary(t *testing.T) {
	text := []byte("prefix secret\nwith-newline suffix\n")
	for boundary := 0; boundary <= len(text); boundary++ {
		mask := newMasker()
		if err := mask.add("secret\nwith-newline"); err != nil {
			t.Fatal(err)
		}
		result := append([]byte(nil), mask.consume(text[:boundary], false)...)
		result = append(result, mask.consume(text[boundary:], false)...)
		result = append(result, mask.consume(nil, true)...)
		if string(result) != "prefix *** suffix\n" {
			t.Fatalf("boundary %d: %q", boundary, result)
		}
	}
}

func TestLogSinkBoundedLongLineAndEncodedMask(t *testing.T) {
	var output bytes.Buffer
	sink := &logSink{mask: newMasker(), emit: func(_ string, data []byte) error { _, err := output.Write(data); return err }}
	for _, chunk := range []string{"::add-", "mask::a%0Ab%25c\n", "a\nb%c\n", strings.Repeat("x", 3*maxLogLine), "\n"} {
		if _, err := sink.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
		if len(sink.line) > maxLogLine || len(sink.mask.tail) > 4096 {
			t.Fatal("unbounded buffered log")
		}
	}
	if err := sink.finish(); err != nil {
		t.Fatal(err)
	}
	if output.String() != "***\n"+strings.Repeat("x", 3*maxLogLine)+"\n" {
		t.Fatalf("unexpected output length %d", output.Len())
	}
}

func TestLogSinkMaskLimits(t *testing.T) {
	mask := newMasker()
	if err := mask.add(strings.Repeat("x", 4097)); err == nil {
		t.Fatal("accepted oversized mask")
	}
	sink := &logSink{mask: newMasker()}
	if _, err := sink.Write([]byte("::add-mask::" + strings.Repeat("x", maxLogLine))); err == nil {
		t.Fatal("accepted oversized add-mask command")
	}
}

func TestMaskKeepsUnicodeAcrossByteAndSecretBoundaries(t *testing.T) {
	for _, secret := range []string{"private-key", "私钥🔑"} {
		m := newMasker()
		if err := m.add(secret); err != nil {
			t.Fatal(err)
		}
		input := []byte("开始 🚀 " + secret + " 完成\n")
		var all bytes.Buffer
		for _, b := range input {
			chunk := m.consume([]byte{b}, false)
			if !utf8.Valid(chunk) {
				t.Fatalf("invalid chunk: %x", chunk)
			}
			all.Write(chunk)
		}
		last := m.consume(nil, true)
		if !utf8.Valid(last) {
			t.Fatal("invalid final chunk")
		}
		all.Write(last)
		if all.String() != "开始 🚀 *** 完成\n" {
			t.Fatalf("masked Unicode=%q", all.String())
		}
	}
}
