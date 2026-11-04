package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type Event struct {
	Type    string         `json:"type"`
	ID      string         `json:"id"`
	Payload map[string]any `json:"payload"`
	Time    time.Time      `json:"time"`
}

type EventBus interface {
	Publish(ctx context.Context, event Event) error
	Close() error
}

type MemoryBus struct {
	mu     sync.Mutex
	events chan Event
	closed bool
}

func NewMemoryBus(buffer int) *MemoryBus {
	if buffer <= 0 {
		buffer = 64
	}
	return &MemoryBus{events: make(chan Event, buffer)}
}

func (b *MemoryBus) Publish(ctx context.Context, event Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return fmt.Errorf("event bus is closed")
	}
	select {
	case b.events <- event:
		return nil
	default:
		return fmt.Errorf("event bus buffer is full")
	}
}

func (b *MemoryBus) Subscribe() <-chan Event { return b.events }

func (b *MemoryBus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		close(b.events)
	}
	return nil
}

// GCPPubSubBus publishes through the Pub/Sub REST API. It uses Application
// Default Credentials, so no service-account key is stored in the repository.
type GCPPubSubBus struct {
	client    *http.Client
	projectID string
	topicID   string
	ts        oauth2.TokenSource
}

func NewGCPPubSubBus(ctx context.Context, projectID, topicID string) (*GCPPubSubBus, error) {
	if projectID == "" || topicID == "" {
		return nil, fmt.Errorf("GCP_PROJECT_ID and GCP_TOPIC_ID are required")
	}
	ts, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/pubsub")
	if err != nil {
		return nil, err
	}
	return &GCPPubSubBus{
		client:    &http.Client{Timeout: 15 * time.Second},
		projectID: projectID,
		topicID:   topicID,
		ts:        ts,
	}, nil
}

func (b *GCPPubSubBus) Publish(ctx context.Context, event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	token, err := b.ts.Token()
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{
		"messages": []map[string]string{{"data": base64.StdEncoding.EncodeToString(data)}},
	})
	endpoint := fmt.Sprintf("https://pubsub.googleapis.com/v1/projects/%s/topics/%s:publish", b.projectID, b.topicID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("pubsub publish failed: %s: %s", resp.Status, string(msg))
	}
	return nil
}

func (b *GCPPubSubBus) Close() error { return nil }

// AzureServiceBusBus publishes through the Service Bus REST API using a
// short-lived SAS token generated from the connection string.
type AzureServiceBusBus struct {
	client    *http.Client
	namespace string
	queue     string
	keyName   string
	key       string
}

func NewAzureServiceBusBus(connectionString, queue string) (*AzureServiceBusBus, error) {
	if connectionString == "" || queue == "" {
		return nil, fmt.Errorf("AZURE_SERVICE_BUS_CONNECTION_STRING and AZURE_QUEUE are required")
	}
	parts := map[string]string{}
	for _, entry := range strings.Split(connectionString, ";") {
		kv := strings.SplitN(entry, "=", 2)
		if len(kv) == 2 {
			parts[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	namespace := strings.TrimSuffix(strings.TrimPrefix(parts["Endpoint"], "sb://"), "/")
	if namespace == "" || parts["SharedAccessKeyName"] == "" || parts["SharedAccessKey"] == "" {
		return nil, fmt.Errorf("connection string must contain Endpoint, SharedAccessKeyName and SharedAccessKey")
	}
	return &AzureServiceBusBus{
		client:    &http.Client{Timeout: 15 * time.Second},
		namespace: namespace,
		queue:     queue,
		keyName:   parts["SharedAccessKeyName"],
		key:       parts["SharedAccessKey"],
	}, nil
}

func (b *AzureServiceBusBus) sasToken(now time.Time) string {
	resource := "https://" + b.namespace + "/" + b.queue
	expiry := strconv.FormatInt(now.Add(5*time.Minute).Unix(), 10)
	signature := url.QueryEscape(resource) + "\n" + expiry
	mac := hmac.New(sha256.New, []byte(b.key))
	_, _ = mac.Write([]byte(signature))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("SharedAccessSignature sr=%s&sig=%s&se=%s&skn=%s",
		url.QueryEscape(resource), url.QueryEscape(sig), expiry, url.QueryEscape(b.keyName))
}

func (b *AzureServiceBusBus) Publish(ctx context.Context, event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint := "https://" + b.namespace + "/" + b.queue + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", b.sasToken(time.Now()))
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("service bus publish failed: %s: %s", resp.Status, string(msg))
	}
	return nil
}

func (b *AzureServiceBusBus) Close() error { return nil }

// NewEventBusFromEnv selects a cloud event bus when configured and falls back
// to the in-memory bus for local development and CI.
func NewEventBusFromEnv(ctx context.Context) EventBus {
	switch os.Getenv("EVENT_BUS") {
	case "gcp":
		bus, err := NewGCPPubSubBus(ctx, os.Getenv("GCP_PROJECT_ID"), os.Getenv("GCP_TOPIC_ID"))
		if err == nil {
			log.Printf("event bus: GCP Pub/Sub topic %s", os.Getenv("GCP_TOPIC_ID"))
			return bus
		}
		log.Printf("event bus: GCP unavailable (%v); using memory bus", err)
	case "azure":
		bus, err := NewAzureServiceBusBus(os.Getenv("AZURE_SERVICE_BUS_CONNECTION_STRING"), os.Getenv("AZURE_QUEUE"))
		if err == nil {
			log.Printf("event bus: Azure Service Bus queue %s", os.Getenv("AZURE_QUEUE"))
			return bus
		}
		log.Printf("event bus: Azure unavailable (%v); using memory bus", err)
	}
	return NewMemoryBus(256)
}
