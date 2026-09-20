package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// UploadResponse is returned after a successful upload.
type UploadResponse struct {
	Status    string `json:"status"`
	Portal    string `json:"portal"`
	Filename  string `json:"filename"`
	RelPath   string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

// JSONUploadPayload defines the JSON format for uploading notes or feedback.
type JSONUploadPayload struct {
	Filename string `json:"filename"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	Author   string `json:"author"`
}

// sanitizeFilename extracts the basename and removes hazardous characters.
func sanitizeFilename(raw string) string {
	base := filepath.Base(filepath.Clean(raw))
	base = strings.ReplaceAll(base, " ", "-")
	base = strings.ReplaceAll(base, "..", "")
	base = strings.Trim(base, ".-_")
	if base == "" || base == "." || base == "/" {
		base = "uploaded-note"
	}
	if !strings.HasSuffix(base, ".md") && !strings.HasSuffix(base, ".txt") && !strings.HasSuffix(base, ".json") {
		base += ".md"
	}
	return base
}

// HandlePortalUpload processes upload requests for a given portal.
func HandlePortalUpload(portal *Portal, rw http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost && req.Method != http.MethodPut {
		http.Error(rw, `{"error":"Method not allowed. Use POST or PUT to upload."}`, http.StatusMethodNotAllowed)
		return
	}

	contentType := req.Header.Get("Content-Type")
	timestampPrefix := time.Now().Format("20060102-150405")

	var savedFilename string
	var savedBytes int64

	if strings.HasPrefix(contentType, "multipart/form-data") {
		// 1. Multipart Form file upload
		err := req.ParseMultipartForm(32 << 20) // 32MB max memory
		if err != nil {
			http.Error(rw, fmt.Sprintf(`{"error":"Failed to parse multipart form: %v"}`, err), http.StatusBadRequest)
			return
		}

		files := req.MultipartForm.File["file"]
		if len(files) == 0 {
			for _, fheaders := range req.MultipartForm.File {
				if len(fheaders) > 0 {
					files = fheaders
					break
				}
			}
		}

		if len(files) == 0 {
			http.Error(rw, `{"error":"No file found in form. Use form field 'file'."}`, http.StatusBadRequest)
			return
		}

		fileHeader := files[0]
		src, err := fileHeader.Open()
		if err != nil {
			http.Error(rw, fmt.Sprintf(`{"error":"Failed to open uploaded file: %v"}`, err), http.StatusInternalServerError)
			return
		}
		defer src.Close()

		base := sanitizeFilename(fileHeader.Filename)
		savedFilename = fmt.Sprintf("%s-%s", timestampPrefix, base)
		destPath := filepath.Join(portal.UploadDir, savedFilename)

		dst, err := os.Create(destPath)
		if err != nil {
			http.Error(rw, fmt.Sprintf(`{"error":"Failed to create destination file: %v"}`, err), http.StatusInternalServerError)
			return
		}
		defer dst.Close()

		n, err := io.Copy(dst, src)
		if err != nil {
			http.Error(rw, fmt.Sprintf(`{"error":"Failed to save file: %v"}`, err), http.StatusInternalServerError)
			return
		}
		savedBytes = n

	} else if strings.HasPrefix(contentType, "application/json") {
		// 2. JSON structured payload
		var payload JSONUploadPayload
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			http.Error(rw, fmt.Sprintf(`{"error":"Failed to parse JSON: %v"}`, err), http.StatusBadRequest)
			return
		}

		if strings.TrimSpace(payload.Content) == "" {
			http.Error(rw, `{"error":"'content' field cannot be empty."}`, http.StatusBadRequest)
			return
		}

		name := payload.Filename
		if name == "" {
			if payload.Title != "" {
				name = payload.Title
			} else {
				name = "agent-experience"
			}
		}
		base := sanitizeFilename(name)
		savedFilename = fmt.Sprintf("%s-%s", timestampPrefix, base)
		destPath := filepath.Join(portal.UploadDir, savedFilename)

		bodyContent := payload.Content
		if payload.Title != "" && !strings.HasPrefix(bodyContent, "# ") {
			bodyContent = fmt.Sprintf("# %s\n\n%s", payload.Title, bodyContent)
		}

		if err := os.WriteFile(destPath, []byte(bodyContent), 0644); err != nil {
			http.Error(rw, fmt.Sprintf(`{"error":"Failed to save content: %v"}`, err), http.StatusInternalServerError)
			return
		}
		savedBytes = int64(len(bodyContent))

	} else {
		// 3. Raw text or markdown payload
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil || len(bodyBytes) == 0 {
			http.Error(rw, `{"error":"Empty body content."}`, http.StatusBadRequest)
			return
		}

		suggestedName := req.URL.Query().Get("filename")
		if suggestedName == "" {
			suggestedName = "note.md"
		}
		base := sanitizeFilename(suggestedName)
		savedFilename = fmt.Sprintf("%s-%s", timestampPrefix, base)
		destPath := filepath.Join(portal.UploadDir, savedFilename)

		if err := os.WriteFile(destPath, bodyBytes, 0644); err != nil {
			http.Error(rw, fmt.Sprintf(`{"error":"Failed to save raw content: %v"}`, err), http.StatusInternalServerError)
			return
		}
		savedBytes = int64(len(bodyBytes))
	}

	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(rw).Encode(UploadResponse{
		Status:    "ok",
		Portal:    portal.Name,
		Filename:  savedFilename,
		RelPath:   filepath.Join("portals", portal.Name, "uploads", savedFilename),
		SizeBytes: savedBytes,
		Message:   "Successfully uploaded feedback/note to entrance.",
		Timestamp: time.Now().Format(time.RFC3339),
	})
}
