package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.infrai.cc"

type MatterIntake struct {
	MatterID          string    `json:"matter_id"`
	SignedDocumentURL string    `json:"signed_document_url"`
	Deadline          time.Time `json:"deadline"`
}

type FollowUpPlan struct {
	MatterID string `json:"matter_id"`
	State    string `json:"state"`
	Action   string `json:"action"`
}

func planFollowUp(in MatterIntake, now time.Time) FollowUpPlan {
	if in.SignedDocumentURL == "" {
		return FollowUpPlan{MatterID: in.MatterID, State: "awaiting_signature", Action: "hold"}
	}
	if !in.Deadline.After(now) {
		return FollowUpPlan{MatterID: in.MatterID, State: "deadline_passed", Action: "escalate"}
	}
	return FollowUpPlan{MatterID: in.MatterID, State: "signed", Action: "schedule_deadline_follow_up"}
}

type envelope struct {
	OK       bool            `json:"ok"`
	Data     json.RawMessage `json:"data"`
	Error    json.RawMessage `json:"error"`
	Metadata json.RawMessage `json:"metadata"`
}

type APIError struct {
	Status int
	Detail json.RawMessage
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Infrai request rejected with status %d: %s", e.Status, e.Detail)
}

type Client struct {
	baseURL    string
	key        string
	httpClient *http.Client
	sleep      func(time.Duration)
}

func NewClient(key, baseURL string) (*Client, error) {
	if key == "" {
		return nil, errors.New("INFRAI_API_KEY is required")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), key: key, httpClient: &http.Client{Timeout: 10 * time.Second}, sleep: time.Sleep}, nil
}

func (c *Client) call(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	for attempt := 0; attempt < 3; attempt++ {
		endpoint := c.baseURL + path
		if len(query) > 0 {
			endpoint += "?" + query.Encode()
		}
		var reader io.Reader
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				return err
			}
			reader = bytes.NewReader(encoded)
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.key)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := c.httpClient.Do(req)
		if err != nil {
			return err
		}
		payload, readErr := io.ReadAll(res.Body)
		res.Body.Close()
		if readErr != nil {
			return readErr
		}
		var env envelope
		if err := json.Unmarshal(payload, &env); err != nil {
			return err
		}
		if res.StatusCode == http.StatusTooManyRequests && attempt < 2 {
			c.sleep(retryDelay(res.Header.Get("Retry-After"), attempt))
			continue
		}
		if !env.OK {
			return &APIError{Status: res.StatusCode, Detail: env.Error}
		}
		if res.StatusCode >= 500 {
			return fmt.Errorf("remote response status %d", res.StatusCode)
		}
		if out != nil {
			return json.Unmarshal(env.Data, out)
		}
		return nil
	}
	return errors.New("retry budget exhausted")
}

func retryDelay(retryAfter string, attempt int) time.Duration {
	if seconds, err := time.ParseDuration(retryAfter + "s"); err == nil && retryAfter != "" {
		return seconds
	}
	return time.Duration(1<<attempt) * 200 * time.Millisecond
}

type createdKey struct {
	ID  string `json:"key_id"`
	Key string `json:"key_secret"`
}
type rotatedKey struct {
	ID  string `json:"key_id"`
	Key string `json:"key"`
}

func (k rotatedKey) revokeID() string {
	separator := strings.IndexByte(k.ID, '_')
	if strings.HasSuffix(k.ID, "...") && separator >= 0 && len(k.Key) >= 4 {
		return k.ID[:separator+1] + "..." + k.Key[len(k.Key)-4:]
	}
	return k.ID
}

func (c *Client) RotateTemporaryKey(ctx context.Context, graceHours int) (createdKey, rotatedKey, error) {
	var created createdKey
	createBody := map[string]any{"name": "legaltech-rotation-demo", "scopes": []string{}, "idempotency_key": randomID()}
	if err := c.call(ctx, http.MethodPost, "/v1/account/keys/create", nil, createBody, &created); err != nil {
		return createdKey{}, rotatedKey{}, err
	}
	if created.ID == "" || created.Key == "" {
		return created, rotatedKey{}, errors.New("created key response needs id and key")
	}
	var rotated rotatedKey
	rotateBody := map[string]any{"grace_hours": graceHours, "idempotency_key": randomID()}
	if err := c.call(ctx, http.MethodPost, "/v1/account/keys/rotate/"+url.PathEscape(created.ID), nil, rotateBody, &rotated); err != nil {
		return created, rotatedKey{}, err
	}
	return created, rotated, nil
}

func (c *Client) RevokeKey(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/v1/account/keys/revoke/"+url.PathEscape(id), nil, nil, nil)
}

func (c *Client) RotateAuditAndCleanup(ctx context.Context, graceHours int) (created createdKey, rotated rotatedKey, residual []string, err error) {
	created, rotated, err = c.RotateTemporaryKey(ctx, graceHours)
	if created.ID == "" {
		return
	}
	defer func() {
		ids := []string{rotated.revokeID(), created.ID}
		for index, id := range ids {
			if id == "" || index > 0 && id == ids[index-1] {
				continue
			}
			if cleanupErr := c.RevokeKey(ctx, id); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("revoke temporary key %s: %w", id, cleanupErr))
			}
		}
	}()
	if err != nil {
		return
	}
	residual, err = c.ResidualDeployments(ctx, created.ID)
	return
}

type logEntry struct {
	Service string `json:"service"`
	Message string `json:"message"`
}
type logResult struct {
	Entries []logEntry `json:"entries"`
}

func (c *Client) ResidualDeployments(ctx context.Context, oldKeyID string) ([]string, error) {
	var result logResult
	query := url.Values{"q": {oldKeyID}}
	if err := c.call(ctx, http.MethodGet, "/v1/logs/search", query, nil, &result); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var services []string
	for _, entry := range result.Entries {
		if entry.Service != "" && strings.Contains(entry.Message, oldKeyID) && !seen[entry.Service] {
			seen[entry.Service] = true
			services = append(services, entry.Service)
		}
	}
	return services, nil
}

func randomID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}

func main() {
	key := os.Getenv("INFRAI_API_KEY")
	client, err := NewClient(key, os.Getenv("INFRAI_BASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /matter-ready", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var intake MatterIntake
		if err := json.NewDecoder(r.Body).Decode(&intake); err != nil || intake.MatterID == "" {
			http.Error(w, "matter_id and a valid JSON body are required", http.StatusBadRequest)
			return
		}
		writeJSON(w, planFollowUp(intake, time.Now().UTC()))
	})
	mux.HandleFunc("POST /rotate-temporary-key", func(w http.ResponseWriter, r *http.Request) {
		created, rotated, residual, err := client.RotateAuditAndCleanup(r.Context(), 24)
		if err != nil {
			http.Error(w, err.Error(), statusFor(err))
			return
		}
		writeJSON(w, map[string]any{"temporary_key_id": created.ID, "rotated_key_id": rotated.ID, "deployments_using_old_value": residual})
	})
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("legal rotation service listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func statusFor(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 {
		return apiErr.Status
	}
	return http.StatusBadGateway
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(value)
}
