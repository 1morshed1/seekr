package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/1morshed1/seekr/internal/protocol"
)

// shardClient is the coordinator's HTTP client for talking to one shard node.
// Each shard in the cluster has its own base address (e.g. http://localhost:9001).
type shardClient struct {
	id      int
	address string
	http    *http.Client
}

func newShardClient(id int, address string) *shardClient {
	return &shardClient{
		id:      id,
		address: address,
		// A modest timeout so one slow/dead shard cannot stall the whole query;
		// the coordinator degrades to partial results instead.
		http: &http.Client{Timeout: 5 * time.Second},
	}
}

// postJSON sends v as JSON to the shard path and decodes the response into out.
func (c *shardClient) postJSON(ctx context.Context, path string, v, out any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.address+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("shard %d %s: status %d: %s", c.id, path, resp.StatusCode, string(msg))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *shardClient) index(ctx context.Context, req protocol.IndexRequest) error {
	return c.postJSON(ctx, "/v1/index", req, nil)
}

func (c *shardClient) search(ctx context.Context, req protocol.SearchRequest) (protocol.SearchResponse, error) {
	var resp protocol.SearchResponse
	err := c.postJSON(ctx, "/v1/search", req, &resp)
	return resp, err
}

func (c *shardClient) hydrate(ctx context.Context, req protocol.HydrateRequest) (protocol.HydrateResponse, error) {
	var resp protocol.HydrateResponse
	err := c.postJSON(ctx, "/v1/hydrate", req, &resp)
	return resp, err
}

func (c *shardClient) stats(ctx context.Context) (protocol.StatsResponse, error) {
	var resp protocol.StatsResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.address+"/v1/stats", nil)
	if err != nil {
		return resp, err
	}
	r, err := c.http.Do(req)
	if err != nil {
		return resp, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return resp, fmt.Errorf("shard %d stats: status %d", c.id, r.StatusCode)
	}
	err = json.NewDecoder(r.Body).Decode(&resp)
	return resp, err
}
