package ring

import "bytes"

type Tail struct {
	limit int64
	buf   bytes.Buffer
}

func NewTail(limit int64) *Tail {
	return &Tail{limit: limit}
}

func (t *Tail) Write(p []byte) (int, error) {
	n := len(p)
	if t.limit <= 0 {
		return n, nil
	}
	if int64(len(p)) >= t.limit {
		t.buf.Reset()
		t.buf.Write(p[int64(len(p))-t.limit:])
		return n, nil
	}
	t.buf.Write(p)
	over := int64(t.buf.Len()) - t.limit
	if over > 0 {
		b := t.buf.Bytes()
		kept := append([]byte(nil), b[over:]...)
		t.buf.Reset()
		t.buf.Write(kept)
	}
	return n, nil
}

func (t *Tail) String() string { return t.buf.String() }
func (t *Tail) Len() int       { return t.buf.Len() }
