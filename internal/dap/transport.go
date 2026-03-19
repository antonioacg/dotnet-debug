package dap

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Transport handles Content-Length framed DAP messages over a reader/writer pair.
type Transport struct {
	reader *bufio.Reader
	writer io.Writer
}

func NewTransport(r io.Reader, w io.Writer) *Transport {
	return &Transport{
		reader: bufio.NewReaderSize(r, 64*1024),
		writer: w,
	}
}

// ReadRaw reads a single Content-Length framed message and returns the raw JSON.
func (t *Transport) ReadRaw() (json.RawMessage, error) {
	var contentLength int
	for {
		line, err := t.reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("reading header: %w", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			contentLength, err = strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("invalid Content-Length %q: %w", val, err)
			}
		}
	}

	if contentLength == 0 {
		return nil, fmt.Errorf("missing or zero Content-Length header")
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(t.reader, body); err != nil {
		return nil, fmt.Errorf("reading body (%d bytes): %w", contentLength, err)
	}

	return json.RawMessage(body), nil
}

// WriteMessage marshals msg to JSON and writes it with a Content-Length header.
func (t *Transport) WriteMessage(msg interface{}) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling message: %w", err)
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(t.writer, header); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	if _, err := t.writer.Write(body); err != nil {
		return fmt.Errorf("writing body: %w", err)
	}
	return nil
}
