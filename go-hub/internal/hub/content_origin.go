package hub

import (
	"strings"
)

// A content source records where its data came from, so that reading or
// searching it can re-check the caller's current rights on that data.
//
// The historical form of one origin is "<target>\x00<tool>". A derived result
// (for example the saved return value of one script) is built from more than
// one source, so it needs more than one origin.
//
// The multi form is "exec:multi\x00<origin>\x01<origin>...". It keeps the
// single origin bytes untouched inside it, so:
//
//   - a source written before this change still decodes as exactly one origin;
//   - a multi origin decodes into the complete list of its single origins;
//   - "exec:index", the free-text origin, stays a single origin and is never
//     confused with the multi form.
const (
	// contentOriginSeparator splits the target from the tool inside one origin.
	contentOriginSeparator = "\x00"
	// contentOriginMultiSeparator splits the origins of one derived result.
	contentOriginMultiSeparator = "\x01"
	// contentOriginMultiPrefix marks an origin that carries several sources.
	contentOriginMultiPrefix = "exec:multi"
	// contentFreeTextOrigin is the origin of a source the caller created from
	// its own text. It has no external source to re-check.
	contentFreeTextOrigin = "exec:index"
)

// contentOriginSingle encodes one target/tool pair.
func contentOriginSingle(target, tool string) string {
	return target + contentOriginSeparator + tool
}

// contentOriginMulti encodes every source a derived result was built from.
// The result may only be read again while every one of them is still allowed.
func contentOriginMulti(origins []string) string {
	return contentOriginMultiPrefix + contentOriginSeparator + strings.Join(origins, contentOriginMultiSeparator)
}

// contentOriginList decodes an origin into the list of single origins it
// covers. A single origin decodes into exactly one entry, a multi origin into
// all of its entries, and an empty origin into none.
func contentOriginList(origin string) []string {
	if origin == "" {
		return nil
	}
	prefix := contentOriginMultiPrefix + contentOriginSeparator
	if !strings.HasPrefix(origin, prefix) {
		return []string{origin}
	}
	body := strings.TrimPrefix(origin, prefix)
	parts := strings.Split(body, contentOriginMultiSeparator)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// contentOriginDescription renders one origin for the caller. A multi origin
// lists every source the result was built from, so a caller can see what it
// would have to keep access to.
func contentOriginDescription(origin string) any {
	if origin == "" || origin == contentFreeTextOrigin {
		return map[string]any{"kind": "exec_text"}
	}
	origins := contentOriginList(origin)
	if len(origins) == 1 {
		pair := strings.SplitN(origins[0], contentOriginSeparator, 2)
		if len(pair) == 2 {
			return map[string]any{"target": pair[0], "tool": pair[1]}
		}
		return map[string]any{"kind": "exec_text"}
	}
	sources := make([]any, 0, len(origins))
	for _, item := range origins {
		pair := strings.SplitN(item, contentOriginSeparator, 2)
		if len(pair) == 2 {
			sources = append(sources, map[string]any{"target": pair[0], "tool": pair[1]})
		}
	}
	return map[string]any{"kind": "exec_derived", "sources": sources}
}

// contentOriginMerge merges origin encodings into one multi origin. It keeps
// the single origin form when only one distinct source is involved, so the
// common case stays byte-identical to the historical format.
//
// The free-text origin means "this content has no external source". It is
// therefore never mixed with real sources: as soon as one external source is
// known, the content is bound to it.
func contentOriginMerge(origins ...string) string {
	seen := map[string]bool{}
	merged := []string{}
	for _, origin := range origins {
		for _, item := range contentOriginList(origin) {
			if item == "" || item == contentFreeTextOrigin || seen[item] {
				continue
			}
			seen[item] = true
			merged = append(merged, item)
		}
	}
	switch len(merged) {
	case 0:
		return contentFreeTextOrigin
	case 1:
		return merged[0]
	default:
		return contentOriginMulti(merged)
	}
}
