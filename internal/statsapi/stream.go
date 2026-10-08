package statsapi

// ObjectSplitter extracts top-level JSON objects from an arbitrary byte
// stream (raw TCP mode sends concatenated objects with or without separators;
// WebSocket messages may also carry several objects). It is resilient: bytes
// outside of objects are skipped, and an oversized object is discarded.
//
// Unlike json.Decoder it never gets stuck after a syntax error: a malformed
// object is emitted as-is, fails to decode, gets logged and the stream goes on.
type ObjectSplitter struct {
	buf      []byte
	depth    int
	inString bool
	escape   bool
	MaxSize  int // 0 => 16 MiB
	// Emit is called with each complete object. The slice is only valid during
	// the call.
	Emit func(obj []byte)
}

func (s *ObjectSplitter) max() int {
	if s.MaxSize > 0 {
		return s.MaxSize
	}
	return 16 << 20
}

// Feed processes more bytes.
func (s *ObjectSplitter) Feed(p []byte) {
	for _, c := range p {
		if s.depth == 0 {
			if c == '{' {
				s.buf = append(s.buf[:0], c)
				s.depth = 1
				s.inString, s.escape = false, false
			}
			continue // anything outside an object is noise/whitespace
		}
		s.buf = append(s.buf, c)
		if len(s.buf) > s.max() {
			s.Reset()
			continue
		}
		if s.inString {
			switch {
			case s.escape:
				s.escape = false
			case c == '\\':
				s.escape = true
			case c == '"':
				s.inString = false
			}
			continue
		}
		switch c {
		case '"':
			s.inString = true
		case '{', '[':
			s.depth++
		case '}', ']':
			s.depth--
			if s.depth == 0 {
				if s.Emit != nil {
					s.Emit(s.buf)
				}
				s.buf = s.buf[:0]
			}
		}
	}
	// Do not keep a huge backing array around forever.
	if s.depth == 0 && cap(s.buf) > 1<<20 {
		s.buf = nil
	}
}

// Reset drops any partial object.
func (s *ObjectSplitter) Reset() {
	s.buf = s.buf[:0]
	s.depth = 0
	s.inString, s.escape = false, false
}

// Pending reports whether a partial object is buffered.
func (s *ObjectSplitter) Pending() bool { return s.depth > 0 }
