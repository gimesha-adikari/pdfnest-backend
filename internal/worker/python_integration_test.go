package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPythonReceiverHTTPCompatibility(t *testing.T) {
	root, python := os.Getenv("F15_WORKER_ROOT"), os.Getenv("F15_PYTHON")
	if root == "" || python == "" {
		t.Skip("set F15_WORKER_ROOT and F15_PYTHON for isolated loopback cross-repository test")
	}
	t.Setenv("WORKER_SHARED_SECRET", "f15-synthetic-secret")
	for _, baseline := range []bool{true, false} {
		t.Run(map[bool]string{true: "old_receiver", false: "strict_receiver"}[baseline], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			args := []string{filepath.Join(root, "tests/f15_http_fixture.py")}
			if baseline {
				args = append(args, "--baseline")
			}
			command := exec.CommandContext(ctx, python, args...)
			command.Dir = root
			command.Env = append(os.Environ(), "APP_ENV=test", "PYTHONPATH="+root, "REDIS_URL=")
			command.Stderr = os.Stderr
			stdout, err := command.StdoutPipe()
			require.NoError(t, err)
			require.NoError(t, command.Start())
			t.Cleanup(func() { cancel(); _ = command.Wait() })
			ready := make(chan string, 1)
			go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- strings.TrimSpace(line) }()
			var port string
			select {
			case port = <-ready:
			case <-ctx.Done():
				t.Fatal("loopback fixture failed to start")
			}
			value, err := strconv.Atoi(port)
			require.NoError(t, err)
			require.True(t, value > 0 && value < 65536)
			url := "http://127.0.0.1:" + port + "/probe?b=2&a=%2F"
			for _, body := range []string{"", `{"operation":"ocr","name":"café"}`} {
				req, _ := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(body))
				response, err := Client.Do(req)
				require.NoError(t, err)
				require.Equal(t, 200, response.StatusCode)
				var result struct {
					Size int `json:"size"`
				}
				require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
				_ = response.Body.Close()
				require.Equal(t, len(body), result.Size)
			}
			input := filepath.Join(t.TempDir(), "small.pdf")
			require.NoError(t, os.WriteFile(input, bytes.Repeat([]byte("synthetic-pdf-bytes"), 8000), 0600))
			stream, contentType, err := CreateMultipartRequest(input, func(w *multipart.Writer) error { return w.WriteField("language", "eng") })
			require.NoError(t, err)
			defer stream.Close()
			req, _ := http.NewRequestWithContext(ctx, "POST", url, stream)
			req.Header.Set("Content-Type", contentType)
			response, err := Client.Do(req)
			require.NoError(t, err)
			require.Equal(t, 200, response.StatusCode)
			var result struct {
				Language string `json:"language"`
				FileSize int    `json:"file_size"`
			}
			require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
			_ = response.Body.Close()
			require.Equal(t, "eng", result.Language)
			require.Equal(t, 19*8000, result.FileSize)
			if !baseline {
				req, _ := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader("one"))
				require.NoError(t, SignRequest(req))
				_ = req.Body.Close()
				req.Body = io.NopCloser(strings.NewReader("two"))
				response, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
				require.NoError(t, err)
				require.Equal(t, 401, response.StatusCode)
				_ = response.Body.Close()
			}
		})
	}
}
