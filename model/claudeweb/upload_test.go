package claudeweb

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/v2/kernel"
)

// The upload is the browser's: multipart file + client_upload_id + reuse_first_upload=true to
// conversations/<id>/wiggle/upload-file; the returned id goes into the first completion's
// "files" (not "attachments"), and the file is written locally where the sandbox has it.
func TestFilesAreUploadedIntoTheSandboxNotAttached(t *testing.T) {
	var gotPath, gotName, gotReuse, gotUploadID, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("multipart: %v", err)
		}
		f, h, _ := r.FormFile("file")
		b, _ := io.ReadAll(f)
		gotName, gotBody = h.Filename, string(b)
		gotReuse, gotUploadID = r.FormValue("reuse_first_upload"), r.FormValue("client_upload_id")
		w.Write([]byte(`{"success":true,"file_uuid":"b49844a8-a7cf-4488-9071-3ba501b622ed","path":"/mnt/user-data/uploads/x.md"}`))
	}))
	defer srv.Close()
	c := NewClient(ClientConfig{BaseURL: srv.URL, OrgID: "org1", Cookie: "c=1"})
	old := localUploadsDir
	defer func() { localUploadsDir = old }()
	localUploadsDir = t.TempDir()

	files := uploadForkFiles(c.UploadFile, "conv-1234-5678", []kernel.ForkFile{{Name: "dev_conversation_u1.md", Data: []byte("sed -i s/2e-3/0.5/ t.cu\n")}})
	if gotPath != "/api/organizations/org1/conversations/conv-1234-5678/wiggle/upload-file" {
		t.Fatalf("path %s", gotPath)
	}
	if gotName != "dev_conversation_u1.md" || gotReuse != "true" || gotUploadID == "" || !strings.Contains(gotBody, "0.5") {
		t.Fatalf("form: name=%q reuse=%q id=%q body=%q", gotName, gotReuse, gotUploadID, gotBody)
	}
	if len(files) != 1 || string(files[0]) != `"b49844a8-a7cf-4488-9071-3ba501b622ed"` {
		t.Fatalf("files entry %s", files)
	}
	if b, err := os.ReadFile(filepath.Join(localUploadsDir, "dev_conversation_u1.md")); err != nil || !strings.Contains(string(b), "0.5") {
		t.Fatalf("local copy: %v", err)
	}
	// the server's actual response (11/07)
	real := `{"success":true,"path":"/mnt/user-data/uploads/1104_1情况.zip","sanitized_name":"1104_1情况.zip","file_kind":"blob","file_uuid":"b49844a8-a7cf-4488-9071-3ba501b622ed","file_name":"1104_1情况.zip","created_at":null,"user_uuid":null,"size_bytes":11823,"media_type":"application/zip","uuid":"b49844a8-a7cf-4488-9071-3ba501b622ed"}`
	if got := uploadedFileID([]byte(real)); got != "b49844a8-a7cf-4488-9071-3ba501b622ed" {
		t.Fatalf("real response → %q", got)
	}
	for body, want := range map[string]string{`{"uuid":"a"}`: "a", `{"file":{"file_uuid":"b"}}`: "b", `{"id":"c"}`: "c", `{"ok":true}`: "", `x`: ""} {
		if got := uploadedFileID([]byte(body)); got != want {
			t.Errorf("%s → %q want %q", body, got, want)
		}
	}
}

// The proxy's refusal of the account itself is the kernel's to know: 11/19 08:29:51 the
// upload of unit 32's review was answered 403 "你当前的车暂时无法使用，请重新选择车辆", and four
// seconds later the last attempt of another unit was started into it. An answered request
// says the account is back.
func TestTheProxysRefusalOfTheAccountReachesTheKernel(t *testing.T) {
	refuse := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuse {
			w.WriteHeader(403)
			w.Write([]byte(`{"error":{"details":{"error_visibility":"user_facing"},"message":"你当前的车暂时无法使用，请重新选择车辆。","type":"permission_error"},"type":"error"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("event: message_stop\ndata: {}\n\n"))
	}))
	defer srv.Close()
	defer kernel.ProxyAccountAnswered()
	oldDir := localUploadsDir
	localUploadsDir = t.TempDir()
	defer func() { localUploadsDir = oldDir }()
	c := NewClient(ClientConfig{BaseURL: srv.URL, OrgID: "org1", Cookie: "c=1"})
	if _, err := c.UploadFile("8c15ab93-cf4f-4ed0-a723-fe34286aa185", "dev_conversation_p45808a8a9c.md", []byte("x")); err == nil {
		t.Fatal("a refused upload returned no error")
	}
	if since, why := kernel.ProxyAccountUnusable(); since.IsZero() || !strings.Contains(why, "重新选择车辆") {
		t.Fatalf("the kernel was not told the account is refused: %v %q", since, why)
	}
	refuse = false
	body, err := c.Completion("8c15ab93-cf4f-4ed0-a723-fe34286aa185", &CompletionRequest{Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	body.Close()
	if since, _ := kernel.ProxyAccountUnusable(); !since.IsZero() {
		t.Fatal("an answered request did not tell the kernel the account is back")
	}
}
