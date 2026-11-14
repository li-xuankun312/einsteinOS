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
