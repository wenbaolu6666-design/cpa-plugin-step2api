package cpa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/WenPaul/cpa-plugin-step2api/internal/stepstudio"
	"github.com/gorilla/websocket"
)

// StepFunStudioURL is the page opened in the isolated login window.
const StepFunStudioURL = "https://studio.stepfun.com/"

// BrowserLoginSession is the state of one isolated-browser login attempt.
type BrowserLoginSession struct {
	ID         string           `json:"id"`
	Status     string           `json:"status"`
	Message    string           `json:"message,omitempty"`
	Browser    string           `json:"browser,omitempty"`
	ProfileDir string           `json:"-"`
	StartedAt  time.Time        `json:"started_at"`
	UpdatedAt  time.Time        `json:"updated_at"`
	Cookies    []string         `json:"cookies,omitempty"`
	Account    *AuthFilePayload `json:"account,omitempty"`
}

const (
	browserLoginWaiting   = "waiting"
	browserLoginSuccess   = "success"
	browserLoginError     = "error"
	browserLoginCancelled = "cancelled"
)

// FindLocalBrowser locates Chrome / Chromium / Edge / Brave on this machine.
func FindLocalBrowser(configured string) (string, error) {
	if v := strings.TrimSpace(configured); v != "" {
		if info, err := os.Stat(v); err == nil && !info.IsDir() {
			return v, nil
		}
	}
	for _, envKey := range []string{"STEP2API_CHROME_PATH", "CHROME_PATH", "BROWSER_PATH"} {
		if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
			if info, err := os.Stat(v); err == nil && !info.IsDir() {
				return v, nil
			}
		}
	}

	var candidates []string
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		for _, rel := range []string{
			"Google Chrome.app/Contents/MacOS/Google Chrome",
			"Chromium.app/Contents/MacOS/Chromium",
			"Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"Brave Browser.app/Contents/MacOS/Brave Browser",
		} {
			candidates = append(candidates, filepath.Join("/Applications", rel))
			if home != "" {
				candidates = append(candidates, filepath.Join(home, "Applications", rel))
			}
		}
	case "windows":
		roots := []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")}
		relPaths := []string{
			`Google\Chrome\Application\chrome.exe`,
			`Chromium\Application\chrome.exe`,
			`Microsoft\Edge\Application\msedge.exe`,
			`BraveSoftware\Brave-Browser\Application\brave.exe`,
		}
		for _, root := range roots {
			if strings.TrimSpace(root) == "" {
				continue
			}
			for _, rel := range relPaths {
				candidates = append(candidates, filepath.Join(root, rel))
			}
		}
	default:
		candidates = append(candidates,
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/usr/bin/microsoft-edge",
			"/usr/bin/brave-browser",
		)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	for _, bin := range []string{"google-chrome", "chromium", "chrome", "msedge", "microsoft-edge", "brave-browser"} {
		if path, err := exec.LookPath(bin); err == nil && path != "" {
			return path, nil
		}
	}
	return "", errors.New("未找到本机 Chrome / Edge / Brave，请先安装，或用 browser_path 指定浏览器路径")
}

func (p *Plugin) browserLoginSnapshot() *BrowserLoginSession {
	p.browserLoginMu.Lock()
	defer p.browserLoginMu.Unlock()
	if p.browserLogin == nil {
		return nil
	}
	copied := *p.browserLogin
	return &copied
}

func (p *Plugin) browserLoginSetStatus(sessionID, status, message string, mutate func(*BrowserLoginSession)) {
	p.browserLoginMu.Lock()
	defer p.browserLoginMu.Unlock()
	if p.browserLogin == nil || p.browserLogin.ID != sessionID {
		return
	}
	if status != "" {
		p.browserLogin.Status = status
	}
	if message != "" {
		p.browserLogin.Message = message
	}
	p.browserLogin.UpdatedAt = time.Now()
	if mutate != nil {
		mutate(p.browserLogin)
	}
}

// StartBrowserLogin launches an isolated browser window and waits for the
// official StepFun login to drop an Oasis-Token cookie.
func (p *Plugin) StartBrowserLogin() (*BrowserLoginSession, error) {
	browserPath, err := FindLocalBrowser(p.Config().BrowserPath)
	if err != nil {
		return nil, err
	}

	p.browserLoginMu.Lock()
	if p.browserLoginCancel != nil {
		p.browserLoginCancel()
		p.browserLoginCancel = nil
	}
	ctx, cancel := context.WithCancel(p.ctx)
	session := &BrowserLoginSession{
		ID:        fmt.Sprintf("login-%d", time.Now().UnixNano()),
		Status:    browserLoginWaiting,
		Message:   "已打开独立无痕窗口，请在窗口内完成 StepFun 登录",
		Browser:   filepath.Base(browserPath),
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	p.browserLogin = session
	p.browserLoginCancel = cancel
	p.browserLoginMu.Unlock()

	go p.runBrowserLogin(ctx, cancel, browserPath, session.ID)
	copied := *session
	return &copied, nil
}

func (p *Plugin) CancelBrowserLogin() *BrowserLoginSession {
	p.browserLoginMu.Lock()
	cancel := p.browserLoginCancel
	p.browserLoginCancel = nil
	p.browserLoginMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if session := p.browserLoginSnapshot(); session != nil && session.Status == browserLoginWaiting {
		p.browserLoginSetStatus(session.ID, browserLoginCancelled, "登录已取消", nil)
	}
	return p.browserLoginSnapshot()
}

func (p *Plugin) isCurrentLogin(sessionID string) bool {
	p.browserLoginMu.Lock()
	defer p.browserLoginMu.Unlock()
	return p.browserLogin != nil && p.browserLogin.ID == sessionID
}

func (p *Plugin) runBrowserLogin(ctx context.Context, cancel context.CancelFunc, browserPath, sessionID string) {
	defer cancel()
	timeout := time.Duration(p.Config().LoginTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	loginCtx, loginCancel := context.WithTimeout(ctx, timeout)
	defer loginCancel()

	sweepStaleLoginDirs()
	profileDir, err := os.MkdirTemp("", "step2api-login-*")
	if err != nil {
		p.browserLoginSetStatus(sessionID, browserLoginError, "创建临时浏览器目录失败: "+err.Error(), nil)
		return
	}
	p.browserLoginMu.Lock()
	if p.browserLogin != nil && p.browserLogin.ID == sessionID {
		p.browserLogin.ProfileDir = profileDir
	}
	p.browserLoginMu.Unlock()
	defer removeDirWithRetry(profileDir)

	args := []string{
		"--user-data-dir=" + profileDir,
		"--remote-debugging-port=0",
		"--disable-blink-features=AutomationControlled",
		"--no-first-run",
		"--no-default-browser-check",
		"--new-window",
		StepFunStudioURL,
	}
	cmd := exec.Command(browserPath, args...)
	if err := cmd.Start(); err != nil {
		p.browserLoginSetStatus(sessionID, browserLoginError, "启动浏览器失败: "+err.Error(), nil)
		return
	}
	var exited atomic.Bool
	waitDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		exited.Store(true)
		close(waitDone)
	}()

	closeBrowser := func(pageWS string) {
		if pageWS != "" {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = cdpCloseBrowser(closeCtx, pageWS)
			closeCancel()
		}
		if !exited.Load() && cmd.Process != nil {
			if runtime.GOOS == "windows" {
				_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
			}
			_ = cmd.Process.Kill()
		}
		select {
		case <-waitDone:
		case <-time.After(3 * time.Second):
		}
	}
	defer closeBrowser("")

	debugPort, err := waitForDevToolsPort(loginCtx, profileDir, &exited)
	if err != nil {
		p.browserLoginSetStatus(sessionID, browserLoginError, err.Error(), nil)
		return
	}

	httpClient := &http.Client{Timeout: 3 * time.Second}
	var pageWS string
	for {
		if !p.isCurrentLogin(sessionID) {
			p.browserLoginSetStatus(sessionID, browserLoginCancelled, "登录已取消", nil)
			return
		}
		if loginCtx.Err() != nil {
			if ctx.Err() != nil {
				p.browserLoginSetStatus(sessionID, browserLoginCancelled, "登录已取消", nil)
			} else {
				p.browserLoginSetStatus(sessionID, browserLoginError, "等待登录超时，已关闭无痕窗口", nil)
			}
			closeBrowser(pageWS)
			return
		}
		if exited.Load() {
			p.browserLoginSetStatus(sessionID, browserLoginError, "浏览器窗口已关闭，登录未完成", nil)
			return
		}

		targets, err := listCDPTargets(loginCtx, httpClient, debugPort)
		if err != nil {
			time.Sleep(400 * time.Millisecond)
			continue
		}
		for i := range targets {
			target := &targets[i]
			if target.Type == "page" && target.WebSocketDebuggerURL != "" {
				if strings.Contains(target.URL, "stepfun.com") || pageWS == "" {
					pageWS = target.WebSocketDebuggerURL
				}
			}
		}

		token, webid, names, cookieErr := readStepFunCookies(loginCtx, httpClient, debugPort, pageWS)
		if len(names) > 0 {
			p.browserLoginSetStatus(sessionID, "", "", func(session *BrowserLoginSession) { session.Cookies = names })
		}
		if cookieErr == nil && token != "" {
			payload, saveErr := saveStepFunAccount(resolveOAuthDir(), "", token, webid)
			if saveErr != nil {
				p.browserLoginSetStatus(sessionID, browserLoginError, "保存账号失败: "+saveErr.Error(), nil)
				closeBrowser(pageWS)
				return
			}
			p.markLoginStateDone(&payload)
			p.browserLoginSetStatus(sessionID, browserLoginSuccess, "已自动获取 Oasis-Token，账号已保存: "+payload.AccountID, func(session *BrowserLoginSession) {
				account := payload
				session.Account = &account
			})
			closeBrowser(pageWS)
			return
		}

		select {
		case <-loginCtx.Done():
		case <-time.After(700 * time.Millisecond):
		}
	}
}

// removeDirWithRetry deletes the temporary browser profile once the browser
// releases its file handles.
func removeDirWithRetry(dir string) {
	for range 24 {
		if err := os.RemoveAll(dir); err == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// sweepStaleLoginDirs removes profile directories left behind by an earlier
// crashed run so cookies never linger in the temp folder.
func sweepStaleLoginDirs() {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-2 * time.Hour)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "step2api-login-") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(os.TempDir(), entry.Name()))
	}
}

func (p *Plugin) markLoginStateDone(payload *AuthFilePayload) {
	p.loginStateMu.Lock()
	defer p.loginStateMu.Unlock()
	for _, state := range p.loginStates {
		state.Done = true
		state.Account = payload
	}
}

type cdpTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

type cdpCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires"`
	HTTPOnly bool    `json:"httpOnly"`
	Secure   bool    `json:"secure"`
}

func waitForDevToolsPort(ctx context.Context, profileDir string, exited *atomic.Bool) (int, error) {
	portFile := filepath.Join(profileDir, "DevToolsActivePort")
	for {
		if err := ctx.Err(); err != nil {
			return 0, errors.New("等待浏览器调试端口超时")
		}
		if exited.Load() {
			return 0, errors.New("浏览器窗口启动后立即退出，请确认本机浏览器可以正常打开")
		}
		if data, err := os.ReadFile(portFile); err == nil {
			line, _, hasNewline := strings.Cut(string(data), "\n")
			if hasNewline {
				if port, convErr := strconv.Atoi(strings.TrimSpace(line)); convErr == nil && port > 0 && port <= 65535 {
					return port, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func listCDPTargets(ctx context.Context, client *http.Client, port int) ([]cdpTarget, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/list", port), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cdp list http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	var targets []cdpTarget
	if err := json.Unmarshal(body, &targets); err != nil {
		return nil, err
	}
	return targets, nil
}

func cdpBrowserWebSocket(ctx context.Context, client *http.Client, port int) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/version", port), nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ""
	}
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if json.Unmarshal(body, &version) != nil {
		return ""
	}
	return version.WebSocketDebuggerURL
}

// readStepFunCookies collects Oasis-Token / Oasis-Webid from the isolated
// browser. The browser-level target covers both normal and incognito contexts.
func readStepFunCookies(ctx context.Context, client *http.Client, port int, pageWS string) (string, string, []string, error) {
	var lastErr error
	if browserWS := cdpBrowserWebSocket(ctx, client, port); browserWS != "" {
		token, webid, names, err := cookiesFromTarget(ctx, browserWS, "Storage.getCookies")
		if err == nil && token != "" {
			return token, webid, names, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if pageWS != "" {
		token, webid, names, err := cookiesFromTarget(ctx, pageWS, "Network.getAllCookies")
		if err == nil {
			return token, webid, names, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("尚未读取到浏览器 Cookie")
	}
	return "", "", nil, lastErr
}

func cookiesFromTarget(ctx context.Context, wsURL, method string) (string, string, []string, error) {
	callCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, _, err := dialer.DialContext(callCtx, wsURL, nil)
	if err != nil {
		return "", "", nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(callCtx, func() { _ = conn.Close() })
	defer stop()

	var result struct {
		Cookies []cdpCookie `json:"cookies"`
	}
	if err := cdpCall(callCtx, conn, 1, method, map[string]any{}, &result); err != nil {
		return "", "", nil, err
	}
	token, webid, names := pickStepFunCookies(result.Cookies)
	return token, webid, names, nil
}

// pickStepFunCookies extracts the Oasis credentials from a CDP cookie list.
func pickStepFunCookies(cookies []cdpCookie) (string, string, []string) {
	token := ""
	webid := ""
	var names []string
	seen := map[string]bool{}
	for _, cookie := range cookies {
		name := strings.ToLower(strings.TrimSpace(cookie.Name))
		if name == "" {
			continue
		}
		if !strings.Contains(strings.ToLower(cookie.Domain), "stepfun") {
			continue
		}
		if !seen[cookie.Name] {
			seen[cookie.Name] = true
			names = append(names, cookie.Name)
		}
		switch name {
		case "oasis-token":
			if strings.TrimSpace(cookie.Value) != "" {
				token = cookie.Value
			}
		case "oasis-webid":
			if strings.TrimSpace(cookie.Value) != "" {
				webid = cookie.Value
			}
		}
	}
	return token, webid, names
}

func cdpCall(ctx context.Context, conn *websocket.Conn, id int, method string, params any, result any) error {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
		_ = conn.SetReadDeadline(deadline)
	}
	message := map[string]any{"id": id, "method": method}
	if params != nil {
		message["params"] = params
	}
	if err := conn.WriteJSON(message); err != nil {
		return err
	}
	for {
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := conn.ReadJSON(&response); err != nil {
			return err
		}
		if response.ID != id {
			continue
		}
		if response.Error != nil {
			return errors.New(response.Error.Message)
		}
		if result != nil && len(response.Result) > 0 {
			return json.Unmarshal(response.Result, result)
		}
		return nil
	}
}

func cdpCloseBrowser(ctx context.Context, wsURL string) error {
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	return cdpCall(ctx, conn, 99, "Browser.close", map[string]any{}, nil)
}

// saveStepFunAccount writes the auth file used by the plugin and the CPA host.
func saveStepFunAccount(oauthDir, name, token, webid string) (AuthFilePayload, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return AuthFilePayload{}, errors.New("未检测到有效的 Oasis-Token")
	}
	if webid == "" {
		if access, _, err := stepstudio.SplitToken(token); err == nil {
			webid = stepstudio.ExtractDeviceID(access)
		}
	}
	accName := strings.TrimSpace(name)
	if accName == "" {
		short := ""
		if webid != "" {
			short = webid
		} else if dev := stepstudio.ExtractDeviceID(token); dev != "" {
			short = dev
		}
		if short != "" {
			accName = "step-" + short[:min(8, len(short))]
		} else {
			accName = fmt.Sprintf("step-%d", time.Now().Unix()%10000)
		}
	}
	payload := AuthFilePayload{
		Type:          "step2api",
		Provider:      ProviderID,
		OAuthProvider: ProviderID,
		AccountID:     accName,
		Name:          accName,
		OasisToken:    token,
		WebID:         webid,
		UpdatedAt:     time.Now().Format(time.RFC3339),
	}
	root, filePath, err := openAccountFile(oauthDir, accName)
	if err != nil {
		return AuthFilePayload{}, err
	}
	defer root.Close()
	// Re-login and re-import must keep user-owned fields such as note/tags/prefix.
	if existing, errRead := root.ReadFile(filePath); errRead == nil {
		var fields map[string]any
		if json.Unmarshal(existing, &fields) == nil && fields != nil {
			if note, _ := fields["note"].(string); strings.TrimSpace(note) != "" {
				payload.Note = note
			}
			if disabled, okDisabled := fields["disabled"].(bool); okDisabled && disabled {
				payload.Disabled = true
			}
			fields["oasis_token"] = token
			if webid != "" {
				fields["web_id"] = webid
			}
			fields["updated_at"] = payload.UpdatedAt
			if merged, errM := json.MarshalIndent(fields, "", "  "); errM == nil {
				if err := root.WriteFile(filePath, merged, 0o600); err == nil {
					return payload, nil
				}
			}
		}
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	if err := root.WriteFile(filePath, data, 0o600); err != nil {
		return AuthFilePayload{}, err
	}
	return payload, nil
}
