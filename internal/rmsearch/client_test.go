package rmsearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchSurfacesAPIErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Meilisearch error shape, delivered with an HTTP 200 in the
		// edge case where a proxy stripped the status.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message":"Attribute ` + "`create_time`" + ` is not filterable","code":"invalid_search_filter"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.Search(context.Background(), &SearchRequest{Q: "x"})
	if err == nil {
		t.Fatal("expected an error for an api error body, got nil")
	}
	if want := "invalid_search_filter"; !contains(err.Error(), want) {
		t.Errorf("error %q missing code %q", err.Error(), want)
	}
}

func TestSearchParsesHits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hits":[{"id":"bbs-post-1","title":"云台","source":"论坛文章"}],"estimatedTotalHits":1}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	res, err := c.Search(context.Background(), &SearchRequest{Q: "云台"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].Title != "云台" || res.Total != 1 {
		t.Errorf("unexpected result: %+v", res)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
