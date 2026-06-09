package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusRecorderDefaultStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := newStatusRecorder(rr)

	if rec.status != http.StatusOK {
		t.Fatalf("expected default status 200, got %d", rec.status)
	}

	if rec.bytes != 0 {
		t.Fatalf("expected default bytes 0, got %d", rec.bytes)
	}
}

func TestStatusRecorderWriteRecordsBytesAndStatusOK(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := newStatusRecorder(rr)

	n, err := rec.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	if n != len("hello") {
		t.Fatalf("expected written bytes %d, got %d", len("hello"), n)
	}

	if rec.status != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.status)
	}

	if rec.bytes != len("hello") {
		t.Fatalf("expected recorded bytes %d, got %d", len("hello"), rec.bytes)
	}

	if rr.Body.String() != "hello" {
		t.Fatalf("expected response body hello, got %q", rr.Body.String())
	}
}

func TestStatusRecorderWriteHeaderRecordsStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := newStatusRecorder(rr)

	rec.WriteHeader(http.StatusNotFound)

	if rec.status != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", rec.status)
	}

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected recorder code 404, got %d", rr.Code)
	}
}

func TestStatusRecorderWriteHeaderOnlyRecordsFirstStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := newStatusRecorder(rr)

	rec.WriteHeader(http.StatusNotFound)
	rec.WriteHeader(http.StatusBadGateway)

	if rec.status != http.StatusNotFound {
		t.Fatalf("expected first status 404 to be kept, got %d", rec.status)
	}

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected recorder code 404, got %d", rr.Code)
	}
}

func TestStatusRecorderWriteAfterWriteHeaderRecordsBytes(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := newStatusRecorder(rr)

	rec.WriteHeader(http.StatusCreated)

	n, err := rec.Write([]byte("created"))
	if err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	if n != len("created") {
		t.Fatalf("expected written bytes %d, got %d", len("created"), n)
	}

	if rec.status != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", rec.status)
	}

	if rec.bytes != len("created") {
		t.Fatalf("expected recorded bytes %d, got %d", len("created"), rec.bytes)
	}
}

func TestStatusRecorderFlushDoesNotPanicWithoutFlusher(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := newStatusRecorder(rr)

	rec.Flush()
}

func TestStatusRecorderHijackReturnsErrorWithoutHijacker(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := newStatusRecorder(rr)

	conn, rw, err := rec.Hijack()
	if err == nil {
		t.Fatal("expected hijack error")
	}

	if conn != nil {
		t.Fatal("expected nil conn")
	}

	if rw != nil {
		t.Fatal("expected nil read writer")
	}

	if !strings.Contains(err.Error(), "does not support hijacking") {
		t.Fatalf("unexpected error: %v", err)
	}
}
