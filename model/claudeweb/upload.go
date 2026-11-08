package claudeweb

// upload.go — files given to a new conversation the way the claude.ai client gives them: each
// is POSTed to conversations/<id>/wiggle/upload-file (multipart: file, client_upload_id,
// reuse_first_upload=true) before the first message, and the first completion lists the ids
// the uploads returned in "files". An uploaded file lands in the conversation's sandbox under
// /mnt/user-data/uploads/<name>, where the conversation reads it with its tools; only its
// path enters the conversation, unlike an "attachments" entry, whose extracted_content is put
// into the prompt whole.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"google.golang.org/adk/v2/kernel"
)

// localUploadsDir: where an uploaded file sits in the sandbox; the kernel writes the same
// file here so the commands the shadow replays on this machine read what the sandbox reads.
var localUploadsDir = "/mnt/user-data/uploads"

// UploadFile uploads data as name to conversation convID and returns the id to list in the
// completion's "files".
func (c *Client) UploadFile(convID, name string, data []byte) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("file", name)
	if err != nil {
		return "", err
	}
	fw.Write(data)
	w.WriteField("client_upload_id", generateUUID())
	w.WriteField("reuse_first_upload", "true")
	w.Close()
	url := fmt.Sprintf("%s/api/organizations/%s/conversations/%s/wiggle/upload-file", c.baseURL, c.orgID, convID)
	req, err := http.NewRequest("POST", url, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Cookie", c.cookie)
	req.Header.Set("anthropic-client-platform", "web_claude_ai")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return "", fmt.Errorf("upload-file %s %s: status %d: %s", shortConv(convID), name, resp.StatusCode, truncBody(rb))
	}
	id := uploadedFileID(rb)
	if id == "" {
		return "", fmt.Errorf("upload-file %s %s: no file id in response: %s", shortConv(convID), name, truncBody(rb))
	}
	log.Printf("claudeweb: upload-file %s %s: %d bytes → %s", shortConv(convID), name, len(data), id)
	return id, nil
}

// uploadedFileID: the id an upload-file response names (the first of the keys it is known
// under, at the top level or under "file").
func uploadedFileID(body []byte) string {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return ""
	}
	if f, ok := m["file"].(map[string]any); ok {
		if id := uploadedFileID(mustJSON(f)); id != "" {
			return id
		}
	}
	for _, k := range []string{"file_uuid", "uuid", "file_id", "id"} {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func shortConv(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// uploadForkFiles uploads the files the kernel gave the next fork and returns the "files"
// entries of its first completion; each is also written to localUploadsDir.
func uploadForkFiles(up func(convID, name string, data []byte) (string, error), convID string, files []kernel.ForkFile) []json.RawMessage {
	var out []json.RawMessage
	for _, f := range files {
		id, err := up(convID, f.Name, f.Data)
		if err != nil {
			log.Printf("claudeweb: %v — %s is not given to the conversation", err, f.Name)
			continue
		}
		out = append(out, json.RawMessage(strconv.Quote(id)))
		if os.MkdirAll(localUploadsDir, 0o755) == nil {
			os.WriteFile(filepath.Join(localUploadsDir, filepath.Base(f.Name)), f.Data, 0o644)
		}
	}
	return out
}
