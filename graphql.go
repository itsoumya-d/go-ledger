package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/graphql-go/graphql"
)

type graphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables"`
}

func newGraphQLSchema(store *Store) (graphql.Schema, error) {
	intentType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Intent",
		Fields: graphql.Fields{
			"id":          &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"accountId":   &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"amountCents": &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
			"currency":    &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"status":      &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"createdAt":   &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		},
	})
	postingType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Posting",
		Fields: graphql.Fields{
			"intentId":     &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"accountId":    &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"amountCents":  &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
			"currency":     &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"settlementId": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		},
	})

	intentFields := func(i *PaymentIntent) map[string]interface{} {
		return map[string]interface{}{
			"id":          i.ID,
			"accountId":   i.AccountID,
			"amountCents": int(i.AmountCents),
			"currency":    i.Currency,
			"status":      i.Status,
			"createdAt":   i.CreatedAt.Format(time.RFC3339Nano),
		}
	}

	query := graphql.NewObject(graphql.ObjectConfig{
		Name: "Query",
		Fields: graphql.Fields{
			"intent": &graphql.Field{
				Type: intentType,
				Args: graphql.FieldConfigArgument{
					"id": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
				},
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					i, ok := store.Intent(p.Args["id"].(string))
					if !ok {
						return nil, nil
					}
					return intentFields(i), nil
				},
			},
			"balance": &graphql.Field{
				Type: graphql.NewNonNull(graphql.Int),
				Args: graphql.FieldConfigArgument{
					"accountId": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
				},
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					return int(store.Balance(p.Args["accountId"].(string))), nil
				},
			},
			"postings": &graphql.Field{
				Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(postingType))),
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					out := make([]map[string]interface{}, 0, len(store.Postings()))
					for _, post := range store.Postings() {
						out = append(out, map[string]interface{}{
							"intentId":     post.IntentID,
							"accountId":    post.AccountID,
							"amountCents":  int(post.AmountCents),
							"currency":     post.Currency,
							"settlementId": post.SettlementID,
						})
					}
					return out, nil
				},
			},
		},
	})

	mutation := graphql.NewObject(graphql.ObjectConfig{
		Name: "Mutation",
		Fields: graphql.Fields{
			"createIntent": &graphql.Field{
				Type: intentType,
				Args: graphql.FieldConfigArgument{
					"idempotencyKey": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
					"accountId":      &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
					"amountCents":    &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.Int)},
					"currency":       &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
				},
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					i, _, err := store.CreateIntent(
						p.Args["idempotencyKey"].(string),
						p.Args["accountId"].(string),
						int64(p.Args["amountCents"].(int)),
						p.Args["currency"].(string),
					)
					if err != nil {
						return nil, err
					}
					return intentFields(i), nil
				},
			},
			"capture": &graphql.Field{
				Type: intentType,
				Args: graphql.FieldConfigArgument{
					"id": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
				},
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					i, err := store.Capture(p.Args["id"].(string))
					if err != nil {
						return nil, err
					}
					return intentFields(i), nil
				},
			},
		},
	})

	return graphql.NewSchema(graphql.SchemaConfig{Query: query, Mutation: mutation})
}

func graphQLHandler(store *Store) http.HandlerFunc {
	schema, err := newGraphQLSchema(store)
	if err != nil {
		panic(err)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var req graphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		result := graphql.Do(graphql.Params{
			Schema:         schema,
			RequestString:  req.Query,
			VariableValues: req.Variables,
		})
		w.Header().Set("Content-Type", "application/json")
		if len(result.Errors) > 0 {
			w.WriteHeader(http.StatusUnprocessableEntity)
		}
		_ = json.NewEncoder(w).Encode(result)
	}
}
