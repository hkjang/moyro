package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Request bodies that carry a client-supplied collection — a bulk id array, a
// batch of memberships, a props map — cost the server the most per byte.
// json.Decode materializes the whole collection before any handler-side length
// check runs, so the `if len(ids) > 200 { ids = ids[:200] }` guards scattered
// through the compat handlers bound what we *query*, not what we *buffer*: the
// array is already on the heap by the time they run. Several of these handlers
// then spend one database round-trip per element. Left unbounded, a single
// authenticated request can pin arbitrary heap and issue arbitrarily many
// queries, so every collection-shaped body reads through decodeCollectionBody.
//
// collectionBodyMaxBytes is deliberately generous — the largest legitimate
// body in this group is a post with rich props, and Mattermost's own default
// payload ceiling is roughly 300 KB.
const collectionBodyMaxBytes = 1 << 20

// maxBulkItems bounds the element count of a batch that writes once per
// element. Read-side bulk lookups keep Mattermost's silent truncation to 200
// so an over-long lookup still answers; silently dropping half of a *write*
// would be worse than refusing it, so the write batches reject instead.
const maxBulkItems = 200

// decodeCollectionBody decodes an array- or map-shaped request body under
// collectionBodyMaxBytes. On failure it has already answered the request —
// 413 when the body outgrew the cap, 400 when it was malformed — and returns
// false, so call sites read `if !decodeCollectionBody(...) { return }`. errID
// is the Mattermost-style error id the handler already used for a bad body;
// the status code, not the id, is what tells the two failures apart.
func decodeCollectionBody(w http.ResponseWriter, r *http.Request, errID string, dst any) bool {
	if err := decodeCappedBody(w, r, dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, errID,
				fmt.Sprintf("request body exceeds %d bytes", collectionBodyMaxBytes))
			return false
		}
		writeError(w, http.StatusBadRequest, errID, err.Error())
		return false
	}
	return true
}

// decodeCappedBody is decodeCollectionBody without the response. A handful of
// compat stubs deliberately tolerate a malformed body (they decode with
// `_ =` and treat the result as empty); they still need the read capped, but
// turning their 200 into a 400 would be a behavior change unrelated to the
// resource bound.
func decodeCappedBody(w http.ResponseWriter, r *http.Request, dst any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, collectionBodyMaxBytes)).Decode(dst)
}

// tooManyBatchItems answers 400 and returns true when a batch that writes once
// per element carries more than maxBulkItems elements. The byte cap alone
// still leaves room for tens of thousands of ids, which is tens of thousands
// of round-trips inside one request.
func tooManyBatchItems(w http.ResponseWriter, errID string, count int) bool {
	if count <= maxBulkItems {
		return false
	}
	writeError(w, http.StatusBadRequest, errID,
		fmt.Sprintf("batch holds %d items; the limit is %d", count, maxBulkItems))
	return true
}
