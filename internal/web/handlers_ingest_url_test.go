package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"boobies-media/internal/jobs"
)

func TestIngestURLQueuesNormalizedJob(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	srv.Queue = jobs.New(srv.Store, 1)
	cookie := authenticate(t, srv, "aiden")

	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/ingest", `{"url":" https://example.com/cat.png#x "}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	list, err := srv.Store.ListJobs(context.Background(), 10)
	if err != nil || len(list) != 1 || list[0].Type != jobs.TypeIngestURL {
		t.Fatalf("jobs = %+v, err=%v", list, err)
	}
	var payload struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(list[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.URL != "https://example.com/cat.png" {
		t.Errorf("queued URL = %q", payload.URL)
	}
}

func TestIngestURLNormalizesFixupXToTwitter(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	srv.Queue = jobs.New(srv.Store, 1)
	cookie := authenticate(t, srv, "aiden")

	rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/ingest", `{"url":"https://fixupx.com/zoulilustration/status/2081867199158042728?s=46&t=tracking"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	list, err := srv.Store.ListJobs(context.Background(), 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("jobs = %+v, err=%v", list, err)
	}
	var payload struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(list[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.URL != "https://x.com/zoulilustration/status/2081867199158042728" {
		t.Errorf("queued URL = %q", payload.URL)
	}
}

func TestIngestURLRejectsUnsafeURLAndMissingQueue(t *testing.T) {
	srv, _, _ := mediaTestServer(t)
	srv.Queue = jobs.New(srv.Store, 1)
	cookie := authenticate(t, srv, "aiden")
	if rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/ingest", `{"url":"file:///etc/passwd"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unsafe URL status = %d", rec.Code)
	}
	srv.Queue = nil
	if rec := apiRequest(t, srv, cookie, http.MethodPost, "/api/ingest", `{"url":"https://example.com/a.png"}`); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("missing queue status = %d", rec.Code)
	}
}
