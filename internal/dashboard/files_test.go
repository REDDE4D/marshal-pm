package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"marshal/internal/pb"
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
