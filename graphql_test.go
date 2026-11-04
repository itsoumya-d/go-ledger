package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGraphQLCreateCaptureAndBalance(t *testing.T) {
	store := NewStore()
	if err := store.Fund("acct_1", 1000); err != nil {
		t.Fatal(err)
	}
	handler := graphQLHandler(store)

	do := func(query string) map[string]any {
		body, _ := json.Marshal(map[string]any{"query": query})
		req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK && rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	created := do(`mutation { createIntent(idempotencyKey:"gql-1", accountId:"acct_1", amountCents:250, currency:"USD") { id status } }`)
	data, _ := created["data"].(map[string]any)
	if data == nil {
		t.Fatalf("no data in response: %v", created)
	}
	intent, _ := data["createIntent"].(map[string]any)
	if intent["status"] != "requires_capture" {
		t.Fatalf("intent = %v", intent)
	}
	id, _ := intent["id"].(string)

	captured := do(`mutation { capture(id:"` + id + `") { status } }`)
	data, _ = captured["data"].(map[string]any)
	capturedIntent, _ := data["capture"].(map[string]any)
	if capturedIntent["status"] != "captured" {
		t.Fatalf("captured = %v", capturedIntent)
	}

	balance := do(`{ balance(accountId:"acct_1") }`)
	if !strings.Contains(toString(balance), "750") {
		t.Fatalf("balance response = %v", balance)
	}
}

func toString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
