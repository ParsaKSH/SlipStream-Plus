package users

import (
	"context"
	"io"
	"time"

	"golang.org/x/time/rate"
)

type trackingReader struct {
	r    io.Reader
	user *User
}

func (r *trackingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.user.AddUsedBytes(int64(n))
	}
	return n, err
}

type trackingWriter struct {
	w    io.Writer
	user *User
}

func (w *trackingWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if n > 0 {
		w.user.AddUsedBytes(int64(n))
	}
	return n, err
}

type rateLimitedReader struct {
	r       io.Reader
	limiter *rate.Limiter
	user    *User
}

func (r *rateLimitedReader) Read(p []byte) (int, error) {
	// Limit read size to burst for proper WaitN
	burst := r.limiter.Burst()
	if burst > 0 && len(p) > burst {
		p = p[:burst]
	}
	n, err := r.r.Read(p)
	if n > 0 {
		r.user.AddUsedBytes(int64(n))
		// Block until tokens available — this creates backpressure
		if waitErr := r.limiter.WaitN(context.Background(), n); waitErr != nil {
			// If WaitN fails (shouldn't with Background()), sleep as fallback
			time.Sleep(time.Duration(n) * time.Second / time.Duration(r.limiter.Limit()))
		}
	}
	return n, err
}

type rateLimitedWriter struct {
	w       io.Writer
	limiter *rate.Limiter
	user    *User
}

func (w *rateLimitedWriter) Write(p []byte) (int, error) {
	total := 0
	burst := w.limiter.Burst()
	for len(p) > 0 {
		chunk := len(p)
		if burst > 0 && chunk > burst {
			chunk = burst
		}
		// Wait BEFORE writing — this is the correct rate-limiting approach
		if waitErr := w.limiter.WaitN(context.Background(), chunk); waitErr != nil {
			time.Sleep(time.Duration(chunk) * time.Second / time.Duration(w.limiter.Limit()))
		}
		n, err := w.w.Write(p[:chunk])
		total += n
		if n > 0 {
			w.user.AddUsedBytes(int64(n))
		}
		if err != nil {
			return total, err
		}
		p = p[n:]
	}
	return total, nil
}
