package httpclient

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// curlRoundTripper executes each request via a curl-impersonate binary so the
// TLS/HTTP2 fingerprint matches a real browser (Chrome/Edge profiles).
type curlRoundTripper struct {
	bin          string
	impersonate  string
	dataDir      string
	accountProxy string

	resolveOnce sync.Once
	resolvedBin string
	resolveErr  error
}

func (t *curlRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("curl-impersonate: nil request")
	}
	bin, err := t.binary()
	if err != nil {
		return nil, err
	}

	bodyBytes, err := readRequestBody(req)
	if err != nil {
		return nil, err
	}

	args := []string{
		"--silent",
		"--show-error",
		"--no-progress-meter",
		"--compressed",
		"--http2",
		"--dump-header", "-",
		"--request", req.Method,
	}
	if proxy := resolveCurlProxy(t.accountProxy, req); proxy != "" {
		args = append(args, "--proxy", proxy)
	}
	// Prefer ChatGPT2API-GO-style header order when present.
	for _, key := range headerKeys(req.Header) {
		for _, value := range req.Header.Values(key) {
			if value == "" {
				continue
			}
			args = append(args, "--header", key+": "+value)
		}
	}
	if len(bodyBytes) > 0 || req.Method == http.MethodPost || req.Method == http.MethodPut || req.Method == http.MethodPatch {
		args = append(args, "--data-binary", "@-")
	}
	args = append(args, req.URL.String())

	ctx := req.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("curl-impersonate start failed: %w", err)
	}
	go func() {
		defer stdin.Close()
		if len(bodyBytes) > 0 {
			_, _ = stdin.Write(bodyBytes)
		}
	}()

	reader := bufio.NewReader(stdout)
	statusCode, statusText, header, err := readCurlResponseHeader(reader)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("curl-impersonate failed: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("curl-impersonate failed: %w", err)
	}
	body := &curlResponseBody{reader: reader, cmd: cmd, stderr: stderr}
	return &http.Response{
		Status:     fmt.Sprintf("%d %s", statusCode, statusText),
		StatusCode: statusCode,
		Proto:      "HTTP/2.0",
		ProtoMajor: 2,
		ProtoMinor: 0,
		Header:     header,
		Body:       body,
		Request:    req,
	}, nil
}

func (t *curlRoundTripper) binary() (string, error) {
	t.resolveOnce.Do(func() {
		t.resolvedBin, t.resolveErr = resolveCurlBinary(t.bin, t.impersonate, t.dataDir)
	})
	return t.resolvedBin, t.resolveErr
}

func resolveCurlBinary(explicit, impersonate, dataDir string) (string, error) {
	if bin := strings.TrimSpace(explicit); bin != "" {
		if st, err := os.Stat(bin); err == nil && !st.IsDir() {
			return bin, nil
		}
		return "", fmt.Errorf("VOICE_CURL_IMPERSONATE_BIN not found: %s", bin)
	}

	candidates := curlBinaryCandidates(impersonate)
	for _, name := range candidates {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}

	searchDirs := []string{
		// Docker image layout (see Dockerfile).
		"/app/bin/curl-impersonate",
	}
	if dataDir = strings.TrimSpace(dataDir); dataDir != "" {
		searchDirs = append(searchDirs,
			filepath.Join(dataDir, "bin", "curl-impersonate"),
			filepath.Join(dataDir, "bin"),
		)
	}
	// Common local/dev install locations next to the process working directory.
	searchDirs = append(searchDirs,
		filepath.Join("data", "bin", "curl-impersonate"),
		filepath.Join("bin", "curl-impersonate"),
	)
	for _, dir := range searchDirs {
		if bin := findCurlBinaryInDir(dir, candidates); bin != "" {
			return bin, nil
		}
	}
	return "", fmt.Errorf(
		"curl-impersonate binary not found (set VOICE_CURL_IMPERSONATE_BIN or install one of: %s)",
		strings.Join(candidates, ", "),
	)
}

func curlBinaryCandidates(impersonate string) []string {
	profile := strings.ToLower(strings.TrimSpace(impersonate))
	if profile == "" {
		// ChatGPT2API-GO account fingerprint default.
		profile = "edge_101"
	}
	// Normalize edge_101 / edge-101 → edge101 for launcher names (curl_edge101).
	launcher := strings.NewReplacer("-", "", "_", "").Replace(profile)
	// Prefer the real package launcher name first.
	out := []string{
		"curl_" + launcher,
		"curl-" + launcher,
	}
	if launcher != profile {
		out = append(out, "curl_"+profile, "curl-"+profile)
	}
	// Fallbacks when the exact profile is not packaged (lwthiker v0.6.1 set).
	out = append(out,
		"curl_edge101",
		"curl_edge99",
		"curl_chrome116",
		"curl_chrome110",
		"curl_chrome107",
		"curl_chrome104",
		"curl_chrome101",
		"curl-impersonate-chrome",
		"curl-impersonate",
	)
	// Deduplicate while preserving order.
	seen := map[string]struct{}{}
	unique := make([]string, 0, len(out))
	for _, name := range out {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		unique = append(unique, name)
	}
	return unique
}

func findCurlBinaryInDir(dir string, candidates []string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return ""
	}
	for _, name := range candidates {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	var found string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || found != "" {
			return nil
		}
		base := d.Name()
		for _, name := range candidates {
			if base == name {
				found = path
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}

func resolveCurlProxy(accountProxy string, req *http.Request) string {
	if proxy := strings.TrimSpace(accountProxy); proxy != "" {
		return proxy
	}
	// Mirror Go's ProxyFromEnvironment for the curl process.
	if req == nil || req.URL == nil {
		return ""
	}
	proxyURL, err := http.ProxyFromEnvironment(req)
	if err != nil || proxyURL == nil {
		return ""
	}
	return proxyURL.String()
}

func headerKeys(h http.Header) []string {
	if h == nil {
		return nil
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	// Order mirrors ChatGPT2API-GO HeaderOrderKey for upstream browser requests.
	preferred := []string{
		"User-Agent", "Accept", "Content-Type", "Authorization",
		"Origin", "Referer", "Accept-Language",
		"Cache-Control", "Pragma", "Priority",
		"Sec-Ch-Ua", "Sec-Ch-Ua-Arch", "Sec-Ch-Ua-Bitness",
		"Sec-Ch-Ua-Full-Version", "Sec-Ch-Ua-Full-Version-List",
		"Sec-Ch-Ua-Mobile", "Sec-Ch-Ua-Model",
		"Sec-Ch-Ua-Platform", "Sec-Ch-Ua-Platform-Version",
		"Sec-Fetch-Dest", "Sec-Fetch-Mode", "Sec-Fetch-Site",
		"Oai-Device-Id", "Oai-Session-Id",
		"Oai-Language", "Oai-Client-Version", "Oai-Client-Build-Number",
		"X-Openai-Target-Path", "X-Openai-Target-Route",
	}
	rank := map[string]int{}
	for i, k := range preferred {
		rank[http.CanonicalHeaderKey(k)] = i
	}
	// Simple insertion sort by rank then name.
	for i := 1; i < len(keys); i++ {
		j := i
		for j > 0 {
			a, b := http.CanonicalHeaderKey(keys[j-1]), http.CanonicalHeaderKey(keys[j])
			ra, aok := rank[a]
			rb, bok := rank[b]
			if !aok {
				ra = 1000
			}
			if !bok {
				rb = 1000
			}
			if ra < rb || (ra == rb && a <= b) {
				break
			}
			keys[j-1], keys[j] = keys[j], keys[j-1]
			j--
		}
	}
	return keys
}

func readRequestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	defer req.Body.Close()
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	// Restore body so callers that inspect req after RoundTrip still work.
	req.Body = io.NopCloser(bytes.NewReader(data))
	return data, nil
}

func readCurlResponseHeader(reader *bufio.Reader) (int, string, http.Header, error) {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0, "", nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		if !strings.HasPrefix(strings.ToUpper(line), "HTTP/") {
			return 0, "", nil, fmt.Errorf("unexpected response prefix: %q", line)
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return 0, "", nil, fmt.Errorf("bad status line: %q", line)
		}
		code, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, "", nil, err
		}
		statusText := strings.TrimSpace(strings.TrimPrefix(line, parts[0]+" "+parts[1]))
		header := make(http.Header)
		for {
			hl, err := reader.ReadString('\n')
			if err != nil {
				return 0, "", nil, err
			}
			hl = strings.TrimRight(hl, "\r\n")
			if hl == "" {
				break
			}
			k, v, ok := strings.Cut(hl, ":")
			if !ok {
				continue
			}
			header.Add(textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(k)), strings.TrimSpace(v))
		}
		// Skip interim 1xx blocks and continue to the real response.
		if code >= 100 && code < 200 && code != 101 {
			continue
		}
		return code, statusText, header, nil
	}
}

type curlResponseBody struct {
	reader *bufio.Reader
	cmd    *exec.Cmd
	stderr *bytes.Buffer
	once   sync.Once
	err    error
}

func (b *curlResponseBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if err == io.EOF {
		if waitErr := b.wait(); waitErr != nil {
			return n, waitErr
		}
	}
	return n, err
}

func (b *curlResponseBody) Close() error {
	if b.cmd != nil && b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
	}
	return b.wait()
}

func (b *curlResponseBody) wait() error {
	b.once.Do(func() {
		if b.cmd == nil {
			return
		}
		if err := b.cmd.Wait(); err != nil {
			if b.stderr != nil && b.stderr.Len() > 0 {
				b.err = fmt.Errorf("curl-impersonate failed: %w: %s", err, strings.TrimSpace(b.stderr.String()))
			} else {
				b.err = err
			}
		}
	})
	return b.err
}
