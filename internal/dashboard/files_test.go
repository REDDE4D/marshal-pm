package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/pb"
)

type fakeFilesController struct {
	res   *pb.ControlResult
	err   error
	gotOp *pb.ControlOp
}

func (f *fakeFilesController) Control(_ context.Context, _ string, op *pb.ControlOp) (*pb.ControlResult, error) {
	f.gotOp = op
	return f.res, f.err
}

func TestListDirEndpoint(t *testing.T) {
	c := &fakeFilesController{res: &pb.ControlResult{
		Ok: true,
		Dir: &pb.DirListing{Path: "", Entries: []*pb.DirEntry{
			{Name: "main.go", IsDir: false, Size: 12, Mode: 0o644},
		}},
	}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, c, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()

	cookie := loginCookie(t, srv.Client(), srv.URL)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/fleet/dev-1/apps/app1/dir?path=", nil)
	req.AddCookie(cookie)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got dirListingDTO
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Name != "main.go" {
		t.Fatalf("got %+v", got)
	}
	// Verify the op carried the right app/path.
	ld := c.gotOp.GetListDir()
	if ld.GetApp() != "app1" || ld.GetPath() != "" {
		t.Fatalf("op app/path = %q/%q", ld.GetApp(), ld.GetPath())
	}
}

func TestReadFileEndpoint_OpRejected(t *testing.T) {
	c := &fakeFilesController{res: &pb.ControlResult{Ok: false, Error: "path escapes deploy root"}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, c, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()

	cookie := loginCookie(t, srv.Client(), srv.URL)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/fleet/dev-1/apps/app1/file?path=../../etc/passwd", nil)
	req.AddCookie(cookie)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestReadFileEndpoint_RawMode(t *testing.T) {
	rawBytes := []byte{0x00, 0x01, 0x02, 0xFF}
	c := &fakeFilesController{res: &pb.ControlResult{
		Ok:   true,
		File: &pb.FileContent{Path: "img/logo.png", Content: rawBytes, Size: 4, Binary: true},
	}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, c, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()

	cookie := loginCookie(t, srv.Client(), srv.URL)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/fleet/dev-1/apps/app1/file?path=img/logo.png&raw=1", nil)
	req.AddCookie(cookie)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", ct)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, `filename="`) {
		t.Errorf("Content-Disposition = %q, want attachment with filename", cd)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if string(body) != string(rawBytes) {
		t.Errorf("body = %v, want %v", body, rawBytes)
	}
}

func TestWriteFileEndpoint(t *testing.T) {
	c := &fakeFilesController{res: &pb.ControlResult{Ok: true, Commit: &pb.CommitResult{Sha: "abc1234", Branch: "main"}}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, c, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	cookie := loginCookie(t, srv.Client(), srv.URL)

	body := strings.NewReader(`{"content":"hello\n","message":"Update README.md"}`)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/fleet/dev-1/apps/app1/file?path=README.md", body)
	req.AddCookie(cookie)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got map[string]string
	json.NewDecoder(resp.Body).Decode(&got)
	if got["sha"] != "abc1234" || got["branch"] != "main" {
		t.Fatalf("body = %+v", got)
	}
	cr := c.gotOp.GetCommit()
	if cr.GetApp() != "app1" || cr.GetKind() != pb.CommitKind_COMMIT_EDIT ||
		cr.GetPath() != "README.md" || string(cr.GetContent()) != "hello\n" {
		t.Fatalf("op = %+v", cr)
	}
}

func TestDeleteFileEndpoint(t *testing.T) {
	c := &fakeFilesController{res: &pb.ControlResult{Ok: true, Commit: &pb.CommitResult{Sha: "d1", Branch: "main"}}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, c, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	cookie := loginCookie(t, srv.Client(), srv.URL)

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/fleet/dev-1/apps/app1/file?path=old.txt", strings.NewReader(`{"message":"Delete old.txt"}`))
	req.AddCookie(cookie)
	resp, _ := srv.Client().Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if c.gotOp.GetCommit().GetKind() != pb.CommitKind_COMMIT_DELETE {
		t.Fatalf("kind = %v", c.gotOp.GetCommit().GetKind())
	}
}

func TestRenameFileEndpoint(t *testing.T) {
	c := &fakeFilesController{res: &pb.ControlResult{Ok: true, Commit: &pb.CommitResult{Sha: "r1", Branch: "main"}}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, c, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	cookie := loginCookie(t, srv.Client(), srv.URL)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/fleet/dev-1/apps/app1/rename", strings.NewReader(`{"from":"a.txt","to":"b.txt","message":"Rename a.txt → b.txt"}`))
	req.AddCookie(cookie)
	resp, _ := srv.Client().Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	cr := c.gotOp.GetCommit()
	if cr.GetKind() != pb.CommitKind_COMMIT_RENAME || cr.GetPath() != "a.txt" || cr.GetNewPath() != "b.txt" {
		t.Fatalf("op = %+v", cr)
	}
}

func TestWriteFileEndpoint_TooLarge(t *testing.T) {
	c := &fakeFilesController{res: &pb.ControlResult{Ok: true}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, c, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	cookie := loginCookie(t, srv.Client(), srv.URL)

	big := strings.Repeat("a", (1<<20)+1)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/fleet/dev-1/apps/app1/file?path=big.txt", strings.NewReader(`{"content":"`+big+`"}`))
	req.AddCookie(cookie)
	resp, _ := srv.Client().Do(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for oversize", resp.StatusCode)
	}
}

func TestCreateFileEndpoint(t *testing.T) {
	c := &fakeFilesController{res: &pb.ControlResult{Ok: true, Commit: &pb.CommitResult{Sha: "c1234567", Branch: "main"}}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, c, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	cookie := loginCookie(t, srv.Client(), srv.URL)

	body := strings.NewReader(`{"content":"# new\n","message":"Create new.txt"}`)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/fleet/dev-1/apps/app1/file?path=new.txt&create=1", body)
	req.AddCookie(cookie)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got map[string]string
	json.NewDecoder(resp.Body).Decode(&got)
	if got["sha"] != "c1234567" || got["branch"] != "main" {
		t.Fatalf("body = %+v", got)
	}
	cr := c.gotOp.GetCommit()
	if cr.GetKind() != pb.CommitKind_COMMIT_CREATE {
		t.Fatalf("kind = %v, want COMMIT_CREATE", cr.GetKind())
	}
	if cr.GetPath() != "new.txt" {
		t.Fatalf("path = %q, want new.txt", cr.GetPath())
	}
	if string(cr.GetContent()) != "# new\n" {
		t.Fatalf("content = %q, want \"# new\\n\"", cr.GetContent())
	}
}

func TestWriteFileEndpoint_EmptyPath(t *testing.T) {
	c := &fakeFilesController{res: &pb.ControlResult{Ok: true}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, c, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	cookie := loginCookie(t, srv.Client(), srv.URL)

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/fleet/dev-1/apps/app1/file?path=", strings.NewReader(`{"content":"x"}`))
	req.AddCookie(cookie)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for empty path", resp.StatusCode)
	}
	var got map[string]string
	json.NewDecoder(resp.Body).Decode(&got)
	if got["error"] == "" {
		t.Fatalf("expected non-empty error in body, got %+v", got)
	}
}

func TestDeleteFileEndpoint_EmptyPath(t *testing.T) {
	c := &fakeFilesController{res: &pb.ControlResult{Ok: true}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, c, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	cookie := loginCookie(t, srv.Client(), srv.URL)

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/fleet/dev-1/apps/app1/file?path=", strings.NewReader(`{}`))
	req.AddCookie(cookie)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for empty path", resp.StatusCode)
	}
	var got map[string]string
	json.NewDecoder(resp.Body).Decode(&got)
	if got["error"] == "" {
		t.Fatalf("expected non-empty error in body, got %+v", got)
	}
}
