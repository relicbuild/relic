package httpserver

import (
	"errors"
	"io"
	"net/http"
	"time"
)

const (
	bodyReadGrace   = time.Minute
	minBodyReadRate = 512
)

func bodyDeadline(h http.Handler, grace time.Duration, rate int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
			h.ServeHTTP(w, r)
			return
		}

		body := &deadlineBody{
			ReadCloser: r.Body,
			controller: http.NewResponseController(w),
			start:      time.Now(),
			grace:      grace,
			rate:       rate,
		}
		body.extend()
		r.Body = body

		h.ServeHTTP(w, r)
	})
}

type deadlineBody struct {
	io.ReadCloser

	controller *http.ResponseController
	start      time.Time
	grace      time.Duration
	rate       int64
	read       int64
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.read += int64(n)

	switch {
	case errors.Is(err, io.EOF):
		_ = b.controller.SetReadDeadline(time.Time{})
	case n > 0:
		b.extend()
	}

	return n, err
}

func (b *deadlineBody) extend() {
	earned := time.Duration(b.read) * time.Second / time.Duration(b.rate)
	_ = b.controller.SetReadDeadline(b.start.Add(b.grace + earned))
}
