package claudeweb

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ParseSSEStream reads SSE events from a reader and sends them to a channel.
// It closes the channel when the stream ends.
func ParseSSEStream(r io.Reader) <-chan SSEEvent {
	ch := make(chan SSEEvent, 32)
	go func() {
		defer close(ch)
		scanner := bufio.NewScanner(r)
		// Allow large lines for big tool call inputs
		scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

		var eventType string
		var dataLines []string

		for scanner.Scan() {
			line := scanner.Text()

			if line == "" {
				// Empty line = end of event
				if eventType != "" && len(dataLines) > 0 {
					data := strings.TrimSpace(strings.Join(dataLines, "\n"))
					ch <- SSEEvent{
						Event: eventType,
						Data:  json.RawMessage(data),
					}
				}
				eventType = ""
				dataLines = nil
				continue
			}

			if strings.HasPrefix(line, "event: ") {
				eventType = strings.TrimPrefix(line, "event: ")
				eventType = strings.TrimSpace(eventType)
			} else if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				data = strings.TrimSpace(data)
				dataLines = append(dataLines, data)
			}
		}

		// Flush last event if stream ended without trailing newline
		if eventType != "" && len(dataLines) > 0 {
			data := strings.TrimSpace(strings.Join(dataLines, "\n"))
			ch <- SSEEvent{
				Event: eventType,
				Data:  json.RawMessage(data),
			}
		}
	}()
	return ch
}

// ClassifyEvent returns the event type from the SSE data.
func ClassifyEvent(data json.RawMessage) (string, error) {
	var et EventType
	if err := json.Unmarshal(data, &et); err != nil {
		return "", fmt.Errorf("classify event: %w", err)
	}
	return et.Type, nil
}
