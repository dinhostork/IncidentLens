package pg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestErrorType(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{&pgconn.PgError{Code: "53300"}, "53300"},
		{fmt.Errorf("query: %w", &pgconn.PgError{Code: "57014"}), "57014"},
		{fmt.Errorf("acquire: %w", context.DeadlineExceeded), "timeout"},
		{&net.OpError{Op: "read", Err: timeoutErr{}}, "timeout"},
		{context.Canceled, "canceled"},
		{&net.OpError{Op: "dial", Err: errors.New("connection refused")}, "connection_error"},
		{fmt.Errorf("read: %w", io.ErrUnexpectedEOF), "connection_error"},
		{errors.New("something else"), "_OTHER"},
	}
	for _, tt := range tests {
		if got := errorType(tt.err); got != tt.want {
			t.Errorf("errorType(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }
