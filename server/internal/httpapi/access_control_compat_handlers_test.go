package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccessControlCompatSearchReturnsEmptyEnvelope(t *testing.T) {
	h := &handlers{}
	req := httptest.NewRequest(http.MethodPost, "/api/v4/access_control_policies/search", bytes.NewBufferString(`{"term":"finance"}`))
	rr := httptest.NewRecorder()

	h.searchAccessControlPolicies(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Policies   []any `json:"policies"`
		TotalCount int   `json:"total_count"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Policies) != 0 || got.TotalCount != 0 {
		t.Fatalf("search = %#v, want empty envelope", got)
	}
}

func TestAccessControlCompatPolicyUsesRouteID(t *testing.T) {
	h := &handlers{}
	req := requestWithRouteParam(http.MethodGet, "/api/v4/access_control_policies/policy-1", "policyID", "policy-1", nil)
	rr := httptest.NewRecorder()

	h.getAccessControlPolicy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["id"] != "policy-1" || got["active"] != false {
		t.Fatalf("policy = %#v", got)
	}
}

func TestAccessControlCompatUpsertPreservesBody(t *testing.T) {
	h := &handlers{}
	req := httptest.NewRequest(http.MethodPut, "/api/v4/access_control_policies", bytes.NewBufferString(`{"id":"policy-2","name":"secure"}`))
	rr := httptest.NewRecorder()

	h.upsertAccessControlPolicy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["id"] != "policy-2" || got["name"] != "secure" {
		t.Fatalf("upsert policy = %#v", got)
	}
}

func TestAccessControlCompatCELFields(t *testing.T) {
	h := &handlers{}
	req := httptest.NewRequest(http.MethodGet, "/api/v4/access_control_policies/cel/autocomplete/fields", nil)
	rr := httptest.NewRecorder()

	h.getAccessControlCELFields(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got []map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) == 0 || got[0]["name"] == "" {
		t.Fatalf("fields = %#v, want at least one named field", got)
	}
}
