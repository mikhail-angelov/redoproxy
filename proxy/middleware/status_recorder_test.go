package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStatusRecorderDefaultStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := newStatusRecorder(rr)

	assert.Equal(t, http.StatusOK, rec.status)
	assert.Equal(t, 0, rec.bytes)
}

func TestStatusRecorderWriteRecordsBytesAndStatusOK(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := newStatusRecorder(rr)

	n, err := rec.Write([]byte("hello"))
	assert.NoError(t, err)
	assert.Equal(t, len("hello"), n)
	assert.Equal(t, http.StatusOK, rec.status)
	assert.Equal(t, len("hello"), rec.bytes)
	assert.Equal(t, "hello", rr.Body.String())
}

func TestStatusRecorderWriteHeaderRecordsStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := newStatusRecorder(rr)

	rec.WriteHeader(http.StatusNotFound)

	assert.Equal(t, http.StatusNotFound, rec.status)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestStatusRecorderWriteHeaderOnlyRecordsFirstStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := newStatusRecorder(rr)

	rec.WriteHeader(http.StatusNotFound)
	rec.WriteHeader(http.StatusBadGateway)

	assert.Equal(t, http.StatusNotFound, rec.status)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestStatusRecorderWriteAfterWriteHeaderRecordsBytes(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := newStatusRecorder(rr)

	rec.WriteHeader(http.StatusCreated)

	n, err := rec.Write([]byte("created"))
	assert.NoError(t, err)
	assert.Equal(t, len("created"), n)
	assert.Equal(t, http.StatusCreated, rec.status)
	assert.Equal(t, len("created"), rec.bytes)
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

	assert.Error(t, err)
	assert.Nil(t, conn)
	assert.Nil(t, rw)
	assert.Contains(t, err.Error(), "does not support hijacking")
}
