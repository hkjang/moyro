package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hkjang/moyro/server/internal/sidebar"
)

// TestWriteSidebarErrorPicksStatusByCause pins the status the sidebar write
// handlers choose from the service error: a category the caller cannot see is
// 404 (the same answer Get gives for that id), a rejected payload is 400, and
// anything else is a server fault rather than the client's. The error id the
// handler passes in survives unchanged so clients keep matching on it.
func TestWriteSidebarErrorPicksStatusByCause(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"missing category", sidebar.ErrNotFound, http.StatusNotFound},
		{"wrapped missing category", fmt.Errorf("update: %w", sidebar.ErrNotFound), http.StatusNotFound},
		{"invalid payload", fmt.Errorf("%w: sorting", sidebar.ErrInvalid), http.StatusBadRequest},
		{"database fault", errors.New("connection refused"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			writeSidebarError(rr, "api.sidebar.update.app_error", tc.err)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d", rr.Code, tc.want)
			}
			var body apiError
			if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if body.ID != "api.sidebar.update.app_error" {
				t.Fatalf("error id = %q, want api.sidebar.update.app_error", body.ID)
			}
			if body.StatusCode != tc.want {
				t.Fatalf("status_code in body = %d, want %d", body.StatusCode, tc.want)
			}
		})
	}
}
