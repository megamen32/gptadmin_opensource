package hub

import "errors"

var errReceiptRedactionCapability = errors.New("worker lacks receipt redaction capability")

// shellQueueChoice changes no job, native lease, permission or argument.
// Queue keeps incompatible jobs in their original relative order.
type shellQueueChoice struct {
	ID                    string
	Payload               []byte
	Queue                 []string
	Oversized             bool
	WindowExhausted       bool
	NeedsReceiptRedaction bool
}

func chooseShellQueueEnvelope(queue []string, maxBytes int, inspect func(string) ([]byte, bool, error)) (shellQueueChoice, error) {
	choice := shellQueueChoice{Queue: make([]string, 0, len(queue))}
	var deferred []string
	for i, id := range queue {
		if i == 16 {
			choice.WindowExhausted = true
			choice.Queue = append(choice.Queue, queue[i:]...)
			choice.Queue = append(choice.Queue, deferred...)
			return choice, nil
		}
		payload, pending, err := inspect(id)
		if errors.Is(err, errReceiptRedactionCapability) {
			choice.NeedsReceiptRedaction = true
			deferred = append(deferred, id)
			continue
		}
		if err != nil {
			return shellQueueChoice{}, err
		}
		if !pending {
			continue
		} // Only refreshed, known non-queued records.
		if len(payload)+1 > maxBytes {
			choice.Oversized = true
			choice.Queue = append(choice.Queue, id)
			continue
		}
		choice.ID, choice.Payload = id, payload
		choice.Queue = append(choice.Queue, queue[i+1:]...)
		choice.Queue = append(choice.Queue, deferred...)
		return choice, nil
	}
	choice.Queue = append(choice.Queue, deferred...)
	return choice, nil
}
