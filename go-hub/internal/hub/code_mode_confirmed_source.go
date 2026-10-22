package hub

import (
	"fmt"
	"net/http"
	"strings"
)

// A ShellMCP command can answer with a stdout tail plus the path of the
// complete spool file. The tail is not the log.
//
// Indexing the tail would produce an index that looks complete but is not: the
// markers near the end of the log would simply be missing, and the caller
// would never learn that. The Hub already knows the complete output of one
// executed operation, because it kept the node-side job record. That record
// advertises the confirmed resource gptadmin://task/<job>/stdout, and the
// confirmed reader reads it back in bounded chunks with the size metadata that
// proves the result is complete.
//
// Three rules hold here:
//
//   - the original command is never re-run;
//   - the spool path is never taken from the MCP text, only from the
//     node-side job record;
//   - a tail is never indexed as if it were the full log.

// codeModeConfirmedReader reads one chunk of a confirmed output resource.
//
// It is the signature of the confirmed reader in server.go. The indirection
// exists so the chunked read can be exercised without a live ShellMCP host.
type codeModeConfirmedReader func(r *http.Request, uri, owner string) map[string]any

const (
	// codeModeConfirmedChunkBytes bounds one confirmed chunk read.
	codeModeConfirmedChunkBytes = 1 << 20
	// codeModeConfirmedMaxBytes bounds the complete output that may be read
	// back for one operation.
	codeModeConfirmedMaxBytes = 8 << 20
)

// codeModeConfirmedSource is the confirmed full output of one executed
// operation, as the server knows it.
type codeModeConfirmedSource struct {
	JobID  string
	URI    string
	Owner  string
	Target string
}

// codeModeConfirmedOutput is the outcome of reading a confirmed source.
type codeModeConfirmedOutput struct {
	Text      string
	Complete  bool
	Reason    string
	TotalSize int64
	BytesRead int64
	Chunks    int
	URI       string
	Owner     string
}

// codeModeConfirmedSourceFor returns the confirmed full output of one already
// executed operation.
//
// The lookup is server side only. The job must exist in the server's own job
// table, it must have run on the host the caller addressed directly, and it
// must advertise a stdout resource. Nothing that the MCP server said is
// trusted: the spool path comes from the job record, never from the response
// text, and the caller can never point the Hub at an arbitrary path or URL.
//
// The host check is what makes the job a task of the caller: a job id that
// merely appears somewhere in a response is not enough, the call must have
// addressed the host that owns the job.
func (s *Server) codeModeConfirmedSourceFor(jobID, target string) (codeModeConfirmedSource, bool) {
	if s == nil || jobID == "" {
		return codeModeConfirmedSource{}, false
	}
	if !strings.HasPrefix(target, "shell:") {
		// The confirmed full source belongs to a shell host. A call that did
		// not address one has no confirmed source to offer.
		return codeModeConfirmedSource{}, false
	}
	host := canonicalShellQueueName(strings.TrimPrefix(target, "shell:"))
	s.mu.Lock()
	job := s.shellJobs[jobID]
	if job == nil {
		s.mu.Unlock()
		return codeModeConfirmedSource{}, false
	}
	server := job.Server
	result := mapValue(job.Result)
	spilled := truthy(result["_spilled"])
	stdoutPath := firstString(result, "stdout_path")
	resources := shellJobOutputResources(job)
	s.mu.Unlock()
	if canonicalShellQueueName(server) != host {
		// The job ran on another host than the one the caller addressed.
		return codeModeConfirmedSource{}, false
	}
	if !spilled || stdoutPath == "" {
		return codeModeConfirmedSource{}, false
	}
	owner := "shell:" + server
	uri := ""
	for _, item := range resources {
		row := mapValue(item)
		if firstString(row, "name") == "stdout" {
			uri = firstString(row, "uri")
			break
		}
	}
	if uri == "" {
		return codeModeConfirmedSource{}, false
	}
	return codeModeConfirmedSource{JobID: jobID, URI: uri, Owner: owner, Target: owner}, true
}

// codeModeConfirmedTrimSlack bounds how many bytes the confirmed reader may
// have trimmed from one chunk. The confirmed reader returns the chunk it read
// with the surrounding whitespace removed, while its byte count stays exact.
const codeModeConfirmedTrimSlack = 64

// readCodeModeConfirmedOutput reads a confirmed output resource in bounded
// chunks and verifies that the result is complete.
//
// The size metadata of the confirmed reader is authoritative: the reported
// total size must not change between chunks, every chunk must continue at the
// expected offset, and the consumed bytes must reach the reported total
// exactly. A resource that ends early is reported as incomplete with the
// reason; it is never treated as the full log.
//
// The confirmed reader returns the chunk it read with the surrounding
// whitespace removed, so the returned text can be shorter than the byte count
// it reports. The trimmed trailing newline is restored, otherwise the line
// structure of the log would be broken at every chunk boundary.
func readCodeModeConfirmedOutput(r *http.Request, reader codeModeConfirmedReader, source codeModeConfirmedSource, maxBytes int64) codeModeConfirmedOutput {
	out := codeModeConfirmedOutput{URI: source.URI, Owner: source.Owner}
	if reader == nil || source.URI == "" {
		out.Reason = "no confirmed full output resource is available"
		return out
	}
	if maxBytes <= 0 {
		maxBytes = codeModeConfirmedMaxBytes
	}
	total := int64(-1)
	consumed := int64(0)
	next := source.URI
	for next != "" {
		if consumed >= maxBytes {
			out.Reason = "the confirmed output exceeded the bounded read limit"
			return out
		}
		response := reader(r, next, source.Owner)
		contents := outputRows(response["contents"])
		if len(contents) == 0 {
			out.Reason = "the confirmed output resource returned no content"
			return out
		}
		chunk := mapValue(contents[0])
		// The text is read without trimming here: a confirmed log is read chunk
		// for chunk, and trimming would corrupt it.
		text, _ := chunk["text"].(string)
		offset := int64(intFromAny(response["offset"]))
		bytesRead := int64(intFromAny(response["bytesRead"]))
		size := int64(intFromAny(response["totalSize"]))
		if total < 0 {
			total = size
		} else if size != total {
			out.Reason = "the confirmed output changed size while it was read"
			return out
		}
		if offset != consumed {
			out.Reason = "the confirmed output chunk did not continue at the expected offset"
			return out
		}
		if bytesRead <= 0 {
			out.Reason = "the confirmed output chunk reported no bytes"
			return out
		}
		if int64(len(text)) > bytesRead {
			out.Reason = "the confirmed output chunk returned more bytes than it reported"
			return out
		}
		if missing := bytesRead - int64(len(text)); missing > 0 {
			if missing > codeModeConfirmedTrimSlack {
				out.Reason = "the confirmed output chunk lost bytes"
				return out
			}
			text += strings.Repeat("\n", int(missing))
		}
		out.Text += text
		out.BytesRead += bytesRead
		consumed = offset + bytesRead
		out.Chunks++
		next = firstString(response, "nextUri")
	}
	if total > 0 && consumed != total {
		out.Reason = fmt.Sprintf("the confirmed output ended after %d of %d bytes", consumed, total)
		return out
	}
	out.Complete = true
	out.TotalSize = total
	return out
}

// codeModeConfirmedFullOutput returns the confirmed full output of the
// operation whose receipt carries jobID.
//
// The second result reports whether a confirmed full source exists at all. It
// is false both when the server never knew a full source and when the
// confirmed source could not be read completely; the reason says which.
func (s *Server) codeModeConfirmedFullOutput(r *http.Request, jobID, target string) (codeModeConfirmedOutput, bool) {
	source, ok := s.codeModeConfirmedSourceFor(jobID, target)
	if !ok {
		return codeModeConfirmedOutput{Reason: "the server holds no confirmed full output for this operation"}, false
	}
	output := readCodeModeConfirmedOutput(r, s.taskOutputResourceRead, source, codeModeConfirmedMaxBytes)
	return output, output.Complete
}
