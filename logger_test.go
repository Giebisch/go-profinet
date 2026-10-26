// SPDX-License-Identifier: MIT

package profinet

import (
	"bytes"
	"context"
	"errors"
	"log"
	"log/slog"
	"strings"
	"testing"
)

func TestSentinelsDistinct(t *testing.T) {
	all := []error{
		ErrClosed, ErrTimeout, ErrMalformed, ErrUnsupported, ErrInvalidConfig,
		ErrInvalidState, ErrRejected, ErrAborted, ErrNotFound, ErrResourceExhausted, ErrLinkDown,
	}
	seen := map[string]bool{}
	for i, a := range all {
		if a == nil {
			t.Fatalf("sentinel %d is nil", i)
		}
		if !strings.HasPrefix(a.Error(), "profinet: ") {
			t.Errorf("message %q lacks prefix", a)
		}
		if seen[a.Error()] {
			t.Errorf("duplicate message %q", a)
		}
		seen[a.Error()] = true
		for j, b := range all {
			if i != j && errors.Is(a, b) {
				t.Errorf("%v matches %v", a, b)
			}
		}
	}
}

func TestNopLogger(t *testing.T) {
	l := NopLogger()
	l.Debugf("x %d", 1)
	l.Infof("x")
	l.Warnf("x")
	l.Errorf("x")
	if _, ok := l.(FieldLogger); ok {
		t.Fatal("NopLogger should not be a FieldLogger")
	}
}

func TestStdLogger(t *testing.T) {
	var buf bytes.Buffer
	l := NewStdLogger(log.New(&buf, "", 0))
	l.Debugf("d %d", 1)
	l.Infof("i %s", "two")
	l.Warnf("w")
	l.Errorf("e")
	want := "DEBUG d 1\nINFO  i two\nWARN  w\nERROR e\n"
	if buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}
	if NewStdLogger(nil) == nil {
		t.Fatal("nil std logger")
	}
	if s, ok := NewStdLogger(nil).(stdLogger); !ok || s.l == nil {
		t.Fatal("NewStdLogger(nil) has no logger")
	}
}

func newTextHandler(buf *bytes.Buffer, level slog.Level) slog.Handler {
	return slog.NewTextHandler(buf, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey && len(groups) == 0 {
				return slog.Attr{}
			}
			return a
		},
	})
}

func TestSlogLoggerNil(t *testing.T) {
	if _, ok := NewSlogLogger(nil).(nopLogger); !ok {
		t.Fatal("NewSlogLogger(nil) is not a nop logger")
	}
	fl := NewSlogFieldLogger(nil)
	if _, ok := fl.(nopFieldLogger); !ok {
		t.Fatal("NewSlogFieldLogger(nil) is not a nop field logger")
	}
	// Every method is a no-op and With keeps the nop logger.
	fl = fl.With("k", "v")
	fl.DebugKV("m")
	fl.InfoKV("m")
	fl.WarnKV("m")
	fl.ErrorKV("m")
	fl.Debugf("m")
	if _, ok := fl.(nopFieldLogger); !ok {
		t.Fatal("With changed the nop logger")
	}
}

func TestSlogLoggerFormatted(t *testing.T) {
	var buf bytes.Buffer
	l := NewSlogLogger(newTextHandler(&buf, slog.LevelInfo))
	if _, ok := l.(FieldLogger); !ok {
		t.Fatal("not a FieldLogger")
	}
	if _, ok := l.(ContextLogger); !ok {
		t.Fatal("not a ContextLogger")
	}
	l.Debugf("hidden %d", 1)
	l.Infof("info %d", 2)
	l.Warnf("warn %s", "x")
	l.Errorf("error")
	want := "level=INFO msg=\"info 2\"\nlevel=WARN msg=\"warn x\"\nlevel=ERROR msg=error\n"
	if buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}
	de := l.(interface{ DebugEnabled() bool })
	if de.DebugEnabled() {
		t.Fatal("debug enabled at info level")
	}

	buf.Reset()
	l = NewSlogLogger(newTextHandler(&buf, slog.LevelDebug))
	l.Debugf("shown %d", 1)
	if buf.String() != "level=DEBUG msg=\"shown 1\"\n" {
		t.Fatalf("got %q", buf.String())
	}
	if !l.(interface{ DebugEnabled() bool }).DebugEnabled() {
		t.Fatal("debug not enabled at debug level")
	}
}

func TestSlogFieldLogger(t *testing.T) {
	var buf bytes.Buffer
	fl := NewSlogFieldLogger(newTextHandler(&buf, slog.LevelDebug))
	w := fl.With("component", "dcp")
	w.DebugKV("d", "a", 1)
	w.InfoKV("i", "b", 2)
	w.WarnKV("w")
	w.ErrorKV("e", "error", "boom")
	want := "level=DEBUG msg=d component=dcp a=1\n" +
		"level=INFO msg=i component=dcp b=2\n" +
		"level=WARN msg=w component=dcp\n" +
		"level=ERROR msg=e component=dcp error=boom\n"
	if buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}
	// The parent has no fields.
	buf.Reset()
	fl.InfoKV("p")
	if buf.String() != "level=INFO msg=p\n" {
		t.Fatalf("got %q", buf.String())
	}
	// Formatted methods of a With logger carry the fields too.
	buf.Reset()
	w.Infof("n=%d", 3)
	if buf.String() != "level=INFO msg=\"n=3\" component=dcp\n" {
		t.Fatalf("got %q", buf.String())
	}
}

type ctxKey struct{}

// ctxHandler records the context value of every entry.
type ctxHandler struct {
	slog.Handler
	got *[]any
}

func (h ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	*h.got = append(*h.got, ctx.Value(ctxKey{}))
	return h.Handler.Handle(ctx, r)
}

func TestSlogContextLogger(t *testing.T) {
	var buf bytes.Buffer
	var got []any
	h := ctxHandler{Handler: newTextHandler(&buf, slog.LevelDebug), got: &got}
	cl := NewSlogLogger(h).(ContextLogger)
	ctx := context.WithValue(context.Background(), ctxKey{}, "trace-1")
	cl.DebugContext(ctx, "d", "k", 1)
	cl.InfoContext(ctx, "i")
	cl.WarnContext(ctx, "w")
	cl.ErrorContext(ctx, "e")
	want := "level=DEBUG msg=d k=1\nlevel=INFO msg=i\nlevel=WARN msg=w\nlevel=ERROR msg=e\n"
	if buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}
	if len(got) != 4 {
		t.Fatalf("handled %d entries", len(got))
	}
	for _, v := range got {
		if v != "trace-1" {
			t.Fatalf("context not passed: %v", got)
		}
	}
}
