package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// countingReader reports how much of an endless body the server actually
// pulled in. Asserting on that number is the only way to tell "we refused the
// request" apart from "we buffered the whole thing and then refused it" —
// the status code alone looks identical.
type countingReader struct {
	chunk  []byte
	offset int
	read   int64
}

// Read cycles through chunk rather than restarting it on every call, so a
// short read can't splice two JSON tokens together and turn the test's
// "endless but well-formed array" into a syntax error.
func (c *countingReader) Read(p []byte) (int, error) {
	n := copy(p, c.chunk[c.offset:])
	c.offset = (c.offset + n) % len(c.chunk)
	c.read += int64(n)
	return n, nil
}

func TestDecodeCollectionBodyAcceptsBodyUnderTheCap(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v4/users/ids", strings.NewReader(`["a","b"]`))
	rr := httptest.NewRecorder()

	var ids []string
	if !decodeCollectionBody(rr, req, "api.test.invalid_body", &ids) {
		t.Fatalf("decode rejected a valid body, status = %d", rr.Code)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("ids = %v, want [a b]", ids)
	}
}

func TestDecodeCollectionBodyAnswers400ForMalformedJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v4/users/ids", strings.NewReader(`["a",`))
	rr := httptest.NewRecorder()

	var ids []string
	if decodeCollectionBody(rr, req, "api.test.invalid_body", &ids) {
		t.Fatal("decode accepted truncated JSON")
	}
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var body apiError
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.ID != "api.test.invalid_body" {
		t.Fatalf("error id = %q, want the caller's id", body.ID)
	}
}

func TestDecodeCollectionBodyStopsReadingAtTheCap(t *testing.T) {
	// An array that never ends: without the cap the decoder grows the slice
	// until the process runs out of memory.
	endless := &countingReader{chunk: []byte(strings.Repeat(`"aaaaaaaaaaaaaaaa",`, 512))}
	req := httptest.NewRequest(http.MethodPost, "/api/v4/users/ids", nil)
	req.Body = io.NopCloser(io.MultiReader(strings.NewReader("["), endless))
	rr := httptest.NewRecorder()

	var ids []string
	if decodeCollectionBody(rr, req, "api.test.invalid_body", &ids) {
		t.Fatal("decode accepted an endless body")
	}
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rr.Code)
	}
	// MaxBytesReader hands back one byte past the limit before erroring, and
	// the decoder buffers a read block on top of that; anything in that
	// neighborhood means the read stopped at the cap rather than at EOF.
	if endless.read > collectionBodyMaxBytes+(64<<10) {
		t.Fatalf("read %d bytes, want no more than roughly the %d-byte cap", endless.read, collectionBodyMaxBytes)
	}
}

func TestTooManyBatchItems(t *testing.T) {
	for _, tc := range []struct {
		name    string
		count   int
		rejects bool
	}{
		{name: "empty", count: 0},
		{name: "at the limit", count: maxBulkItems},
		{name: "one over the limit", count: maxBulkItems + 1, rejects: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			got := tooManyBatchItems(rr, "api.test.too_many", tc.count)
			if got != tc.rejects {
				t.Fatalf("tooManyBatchItems(%d) = %v, want %v", tc.count, got, tc.rejects)
			}
			if !tc.rejects {
				if rr.Code != http.StatusOK || rr.Body.Len() != 0 {
					t.Fatalf("accepted batch still wrote status=%d body=%q", rr.Code, rr.Body.String())
				}
				return
			}
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rr.Code)
			}
		})
	}
}

// sidebarOrderRequest builds a self-scoped
// PUT /users/{id}/teams/{id}/channels/categories/order request. The handler
// checks access, decodes, then caps — so a rejected request never reaches
// h.sidebar and a nil service is enough to prove the guard fires first.
func sidebarOrderRequest(t *testing.T, body io.Reader) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v4/users/u1/teams/t1/channels/categories/order", body)
	ctx := context.WithValue(req.Context(), userIDKey, "u1")
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("userID", "u1")
	routeCtx.URLParams.Add("teamID", "t1")
	return req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, routeCtx))
}

func TestUpdateSidebarCategoryOrderRefusesOversizedBatch(t *testing.T) {
	ids := make([]string, maxBulkItems+1)
	for i := range ids {
		ids[i] = "00000000-0000-0000-0000-000000000000"
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		t.Fatalf("marshal ids: %v", err)
	}

	h := &handlers{}
	rr := httptest.NewRecorder()
	h.updateSidebarCategoryOrder(rr, sidebarOrderRequest(t, strings.NewReader(string(encoded))))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var body apiError
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.ID != "api.sidebar.order.too_many" {
		t.Fatalf("error id = %q, want api.sidebar.order.too_many", body.ID)
	}
}

func TestUpdateSidebarCategoryOrderRefusesOversizedBody(t *testing.T) {
	endless := &countingReader{chunk: []byte(strings.Repeat(`"aaaaaaaaaaaaaaaa",`, 512))}
	req := sidebarOrderRequest(t, nil)
	req.Body = io.NopCloser(io.MultiReader(strings.NewReader("["), endless))

	h := &handlers{}
	rr := httptest.NewRecorder()
	h.updateSidebarCategoryOrder(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rr.Code)
	}
	if endless.read > collectionBodyMaxBytes+(64<<10) {
		t.Fatalf("read %d bytes before refusing, want no more than roughly the %d-byte cap",
			endless.read, collectionBodyMaxBytes)
	}
}
