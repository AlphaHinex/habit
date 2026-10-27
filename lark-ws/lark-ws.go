package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

const (
	notificationFilePath = "fftq/notification.md"

	githubRepoOwner = "alphahinex"
	githubRepoName  = "habit"
	githubBranch    = "master"
	githubAPIBase   = "https://api.github.com"

	stateFilePath = ".lark-ws-state.json"
	pidFilePath   = ".lark-ws.pid"

	processingEmojiType = "Typing"
	doneEmojiType       = "DONE"
	failedEmojiType     = "SWEAT"

	// 单条消息从开始处理到落库的整体超时。
	messageTimeout = 5 * time.Minute
	// 事件去重窗口，覆盖飞书重投与补偿扫描的重叠区间。
	dedupTTL = 24 * time.Hour

	defaultCatchUpInterval = 5 * time.Minute
	defaultCatchUpInitial  = 30 * time.Second
	// 补偿扫描的安全余量：水位只推进到「扫描时刻 - 该值」，
	// 避免飞书历史消息索引延迟导致刚发出的消息被跳过。
	catchUpSafetyLag = 10 * time.Minute
	// 每次补偿扫描向前回溯的重叠区间，重复部分由去重抵消。
	catchUpOverlap = 5 * time.Minute
)

var chatFileMapping = map[string]string{
	"oc_86009321961989ec141e138603f8e0ff": "fftq/notification.md",
	"oc_6e754653563a3bc4389685bbcb827ef9": "fftq/math/README.md",
	"oc_a42457d41b102a9c41037f59bca87690": "fftq/chinese/README.md",
	"oc_d97fb0698c3ab44768be8d50b50449db": "fftq/english/README.md",
	"oc_278b9f093001b2599f3538bdba90f996": "fftq/history/README.md",
	"oc_3399ab117e474a4cd2aa19e86cecc406": "fftq/geography/README.md",
	"oc_03d353dc045eff4e5f839c5a801cafa3": "fftq/politics/README.md",
	"oc_89af8d0f5c394beffe5f37cdc222b368": "fftq/biology/README.md",
	"oc_41714f8617f6deee229120fa017579ac": "fftq/physics/README.md",
	"oc_4984297ba6420c73617635e77a059843": "fftq/chemistry/README.md",
}

var (
	// 统一使用 Asia/Shanghai，避免目录日期与归档日期因时区不同而错位。
	shanghaiLoc = func() *time.Location {
		loc, err := time.LoadLocation("Asia/Shanghai")
		if err != nil {
			fmt.Printf("[init] load Asia/Shanghai failed, fallback to UTC+8: %v\n", err)
			return time.FixedZone("CST", 8*3600)
		}
		return loc
	}()

	// 全局复用的 HTTP 客户端：必须带超时，否则一个卡住的请求会拖垮整批消息。
	httpClient = &http.Client{Timeout: envDuration("GITHUB_HTTP_TIMEOUT", 60*time.Second)}

	// GitHub 官方要求同一用户/客户端的写请求不要并发，这里做串行 + 限速。
	ghWriteLimiter = newIntervalLimiter(envDuration("GITHUB_WRITE_INTERVAL", time.Second))
	ghReadLimiter  = newIntervalLimiter(envDuration("GITHUB_READ_INTERVAL", 100*time.Millisecond))

	// 保护「读-改-写」与资源上传，即使调大 worker 数也不会丢更新。
	gitHubOpMu sync.Mutex

	// 全进程复用一个 lark.Client，token 缓存才有效。
	larkClient *lark.Client

	appState *syncState

	processed = newSeenSet(dedupTTL)

	// 事件队列 -> dispatcher（加处理中表情）-> 工作队列 -> worker。
	eventQueue = make(chan *messageJob, envInt("LARK_WS_QUEUE_SIZE", 1024))
	workQueue  = make(chan *messageJob, envInt("LARK_WS_QUEUE_SIZE", 1024))
	workSlots  = envInt("LARK_WS_WORKERS", 1)
)

func getFilePath(chatId string) string {
	if path, ok := chatFileMapping[chatId]; ok {
		return path
	}
	return notificationFilePath
}

type TextMsg struct {
	Text string `json:"text"`
}

type MediaMsg struct {
	ImageKey string `json:"image_key"`
	FileKey  string `json:"file_key"`
	FileName string `json:"file_name"`
}

type ImgMsg struct {
	ImageKey string `json:"image_key"`
}

type FileMsg struct {
	FileKey  string `json:"file_key"`
	FileName string `json:"file_name"`
}

type githubPutRequest struct {
	Message string `json:"message"`
	Content string `json:"content"`
	Branch  string `json:"branch"`
	SHA     string `json:"sha,omitempty"`
}

type GitHubFileResponse struct {
	Content string `json:"content"`
	SHA     string `json:"sha"`
}

var (
	errGitHubNotFound  = errors.New("github: file not found")
	errUnsupportedType = errors.New("unsupported message type")
	errInvalidContent  = errors.New("invalid message content")
)

// githubError 保留状态码与响应体，便于区分文件已存在、限流、权限不足。
type githubError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *githubError) Error() string {
	return fmt.Sprintf("github %s %s: status %d: %s", e.Method, e.Path, e.Status, truncate(e.Body, 500))
}

// ---------------------------------------------------------------------------
// 基础工具
// ---------------------------------------------------------------------------

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return def
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func shortID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

// imageResourceName 生成秒级时间戳 + 随机后缀的资源名，
// 从根源上避免「同一秒发出的多张图片撞名」。
func imageResourceName(now time.Time) string {
	return fmt.Sprintf("%s_%s.jpg", now.Format("20060102150405"), shortID())
}

func splitFileName(name string) (string, string) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if base == "" {
		base = "file"
	}
	return base, ext
}

// sanitizeFileName 去掉会破坏 URL / Markdown 链接的字符，保留中文。
func sanitizeFileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(name)

	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '-' || r == '_' || r == '(' || r == ')' || r == '+' || r == '&':
			b.WriteRune(r)
		case r > 127:
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}

	out := strings.Trim(b.String(), ".")
	if out == "" {
		out = "file"
	}
	return out
}

// intervalLimiter 保证两次调用之间至少间隔 interval。
type intervalLimiter struct {
	interval time.Duration
	mu       sync.Mutex
	next     time.Time
}

func newIntervalLimiter(interval time.Duration) *intervalLimiter {
	return &intervalLimiter{interval: interval}
}

func (l *intervalLimiter) Wait(ctx context.Context) error {
	if l.interval <= 0 {
		return nil
	}

	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	delay := l.next.Sub(now)
	l.next = l.next.Add(l.interval)
	l.mu.Unlock()

	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// seenSet 用于事件去重：飞书可能重投，补偿扫描也会与实时事件重叠。
type seenSet struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]time.Time
}

func newSeenSet(ttl time.Duration) *seenSet {
	return &seenSet{ttl: ttl, entries: make(map[string]time.Time)}
}

func (s *seenSet) MarkIfNew(key string) bool {
	if key == "" {
		return true
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	if seenAt, ok := s.entries[key]; ok && now.Sub(seenAt) < s.ttl {
		return false
	}
	s.entries[key] = now
	if len(s.entries) > 4096 {
		s.pruneLocked(now)
	}
	return true
}

func (s *seenSet) pruneLocked(now time.Time) {
	for k, seenAt := range s.entries {
		if now.Sub(seenAt) >= s.ttl {
			delete(s.entries, k)
		}
	}
}

// ---------------------------------------------------------------------------
// 补偿扫描水位持久化
// ---------------------------------------------------------------------------

type syncState struct {
	mu         sync.Mutex
	path       string
	watermarks map[string]int64
}

type persistedState struct {
	Watermarks map[string]int64 `json:"watermarks"`
}

func loadSyncState(path string) *syncState {
	s := &syncState{path: path, watermarks: make(map[string]int64)}

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("[state] read %s failed: %v\n", path, err)
		}
		return s
	}

	var persisted persistedState
	if err := json.Unmarshal(data, &persisted); err != nil {
		fmt.Printf("[state] parse %s failed: %v\n", path, err)
		return s
	}
	if persisted.Watermarks != nil {
		s.watermarks = persisted.Watermarks
	}
	return s
}

func (s *syncState) watermark(chatID string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.watermarks[chatID]
}

func (s *syncState) advance(chatID string, ts int64) {
	s.mu.Lock()
	if ts <= s.watermarks[chatID] {
		s.mu.Unlock()
		return
	}
	s.watermarks[chatID] = ts
	s.mu.Unlock()

	if err := s.save(); err != nil {
		fmt.Printf("[state] save %s failed: %v\n", s.path, err)
	}
}

func (s *syncState) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(persistedState{Watermarks: s.watermarks}, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".lark-ws-state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, s.path)
}

// acquireInstanceLock 通过 flock 保证同一目录只有一个实例，
// 这是内存锁生效的前提。
func acquireInstanceLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another lark-ws instance is already running (lock %s): %w", path, err)
	}

	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Sync()

	return f, nil
}

// ---------------------------------------------------------------------------
// GitHub Contents API
// ---------------------------------------------------------------------------

func githubToken() (string, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return "", errors.New("GITHUB_TOKEN environment variable not set")
	}
	return token, nil
}

func encodeRepoPath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func githubContentsURL(p string) string {
	return fmt.Sprintf("%s/repos/%s/%s/contents/%s", githubAPIBase, githubRepoOwner, githubRepoName, encodeRepoPath(p))
}

func githubBlobURL(dateDir, fileName string) string {
	return fmt.Sprintf("https://github.com/%s/%s/blob/%s/fftq/res/%s/%s",
		githubRepoOwner, githubRepoName, githubBranch, dateDir, encodeRepoPath(fileName))
}

func proxiedBlobURL(dateDir, fileName string) string {
	return "https://gh-proxy.com/" + githubBlobURL(dateDir, fileName)
}

// githubDo 是唯一的 GitHub 请求出口：统一限速、超时与重试。
// 返回 2xx/404 的原样状态码；其余状态码以 *githubError 返回。
func githubDo(ctx context.Context, method, repoPath string, payload interface{}, write bool) (int, []byte, error) {
	token, err := githubToken()
	if err != nil {
		return 0, nil, err
	}

	var body []byte
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
	}

	limiter := ghReadLimiter
	if write {
		limiter = ghWriteLimiter
	}

	attempts := envInt("GITHUB_MAX_ATTEMPTS", 6)
	var lastErr error

	for attempt := 0; attempt < attempts; attempt++ {
		if err := limiter.Wait(ctx); err != nil {
			return 0, nil, err
		}

		req, err := http.NewRequestWithContext(ctx, method, githubContentsURL(repoPath), bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Authorization", "token "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return 0, nil, ctx.Err()
			}
			lastErr = err
			fmt.Printf("[github] %s %s network error (attempt %d/%d): %v\n", method, repoPath, attempt+1, attempts, err)
			sleepBackoff(ctx, attempt, 0)
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			sleepBackoff(ctx, attempt, 0)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp.StatusCode, respBody, nil
		}
		if resp.StatusCode == http.StatusNotFound {
			return resp.StatusCode, respBody, nil
		}

		if shouldRetryGitHub(resp, respBody) {
			lastErr = &githubError{Method: method, Path: repoPath, Status: resp.StatusCode, Body: string(respBody)}
			wait := retryAfter(resp)
			fmt.Printf("[github] %s %s got %d, retrying in %s (attempt %d/%d)\n",
				method, repoPath, resp.StatusCode, wait, attempt+1, attempts)
			sleepBackoff(ctx, attempt, wait)
			continue
		}

		return resp.StatusCode, respBody, &githubError{Method: method, Path: repoPath, Status: resp.StatusCode, Body: string(respBody)}
	}

	return 0, nil, fmt.Errorf("github %s %s: giving up after %d attempts: %w", method, repoPath, attempts, lastErr)
}

func shouldRetryGitHub(resp *http.Response, body []byte) bool {
	switch {
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		return true
	case resp.StatusCode == http.StatusForbidden:
		if resp.Header.Get("Retry-After") != "" {
			return true
		}
		low := strings.ToLower(string(body))
		return strings.Contains(low, "rate limit") || strings.Contains(low, "secondary rate")
	}
	return false
}

func retryAfter(resp *http.Response) time.Duration {
	if v := strings.TrimSpace(resp.Header.Get("Retry-After")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	if v := strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")); v != "" {
		if epoch, err := strconv.ParseInt(v, 10, 64); err == nil {
			if d := time.Until(time.Unix(epoch, 0)); d > 0 {
				return d
			}
		}
	}
	return 0
}

func sleepBackoff(ctx context.Context, attempt int, wait time.Duration) {
	if wait <= 0 {
		wait = 500 * time.Millisecond * time.Duration(1<<uint(minInt(attempt, 6)))
	}
	if wait > 30*time.Second {
		wait = 30 * time.Second
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// getFileContent 读取仓库文件内容与 sha；文件不存在时返回 errGitHubNotFound。
func getFileContent(ctx context.Context, repoPath string) (string, string, error) {
	status, body, err := githubDo(ctx, http.MethodGet, repoPath, nil, false)
	if err != nil {
		return "", "", err
	}
	if status == http.StatusNotFound {
		return "", "", errGitHubNotFound
	}

	var fileResp GitHubFileResponse
	if err := json.Unmarshal(body, &fileResp); err != nil {
		return "", "", fmt.Errorf("decode %s response: %w", repoPath, err)
	}

	// StdEncoding 会自动忽略换行符，无需额外处理。
	decoded, err := base64.StdEncoding.DecodeString(fileResp.Content)
	if err != nil {
		return "", "", fmt.Errorf("decode %s content: %w", repoPath, err)
	}

	return string(decoded), fileResp.SHA, nil
}

// putFileContent 创建或更新文件；sha 为空表示新建。
func putFileContent(ctx context.Context, repoPath string, content []byte, sha, commitMsg string) error {
	payload := githubPutRequest{
		Message: commitMsg,
		Content: base64.StdEncoding.EncodeToString(content),
		Branch:  githubBranch,
		SHA:     sha,
	}

	status, body, err := githubDo(ctx, http.MethodPut, repoPath, payload, true)
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusCreated {
		fmt.Printf("[github] wrote %s (%s)\n", repoPath, commitMsg)
		return nil
	}

	return &githubError{Method: http.MethodPut, Path: repoPath, Status: status, Body: string(body)}
}

func fileExists(ctx context.Context, repoPath string) (bool, error) {
	_, _, err := getFileContent(ctx, repoPath)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, errGitHubNotFound) {
		return false, nil
	}
	return false, err
}

// storeResource 把资源写入 fftq/res/<date>/ 并返回实际使用的文件名。
// 目标名已存在时自动追加 -1/-2… 后缀，绝不覆盖此前上传的文件。
func storeResource(ctx context.Context, now time.Time, preferred string, data []byte) (string, error) {
	// GitHub Contents API 单文件上限约 100MB，提前拦截以便给出明确错误。
	const maxContentsAPIFileSize = 95 * 1024 * 1024
	if len(data) > maxContentsAPIFileSize {
		return "", fmt.Errorf("resource %q is too large for the GitHub Contents API: %d bytes (limit ~100MB)",
			preferred, len(data))
	}

	dateDir := now.Format("20060102")
	base, ext := splitFileName(preferred)

	for i := 0; i < 100; i++ {
		name := preferred
		if i > 0 {
			name = fmt.Sprintf("%s-%d%s", base, i, ext)
		}

		repoPath := fmt.Sprintf("fftq/res/%s/%s", dateDir, name)
		err := putFileContent(ctx, repoPath, data, "", "Add "+name)
		if err == nil {
			return name, nil
		}

		// 409/422 通常意味着该路径已存在，确认后换个后缀重试。
		var ghErr *githubError
		if errors.As(err, &ghErr) && (ghErr.Status == http.StatusConflict || ghErr.Status == http.StatusUnprocessableEntity) {
			exists, checkErr := fileExists(ctx, repoPath)
			if checkErr == nil && exists {
				continue
			}
		}
		return "", err
	}

	return "", fmt.Errorf("no free resource name for %q", preferred)
}

// ---------------------------------------------------------------------------
// 飞书 IM 调用（统一带超时）
// ---------------------------------------------------------------------------

func downloadMessageResource(ctx context.Context, msgID, key, fileType string) ([]byte, error) {
	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(msgID).
		FileKey(key).
		Type(fileType).
		Build()

	resp, err := larkClient.Im.V1.MessageResource.Get(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.Success() {
		return nil, fmt.Errorf("get %s resource: code %d, msg %s, logId %s",
			fileType, resp.Code, resp.Msg, resp.RequestId())
	}
	if len(resp.RawBody) == 0 {
		return nil, fmt.Errorf("get %s resource: empty body", fileType)
	}

	return resp.RawBody, nil
}

func addMessageReaction(ctx context.Context, msgID, emojiType string) (string, error) {
	req := larkim.NewCreateMessageReactionReqBuilder().
		MessageId(msgID).
		Body(&larkim.CreateMessageReactionReqBody{
			ReactionType: larkim.NewEmojiBuilder().EmojiType(emojiType).Build(),
		}).
		Build()

	resp, err := larkClient.Im.V1.MessageReaction.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("failed to add reaction %s, code: %d, msg: %s", emojiType, resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.ReactionId == nil {
		return "", errors.New("add reaction succeeded but reaction_id is empty")
	}

	return *resp.Data.ReactionId, nil
}

func deleteMessageReaction(ctx context.Context, msgID, reactionID string) error {
	req := larkim.NewDeleteMessageReactionReqBuilder().
		MessageId(msgID).
		ReactionId(reactionID).
		Build()

	resp, err := larkClient.Im.V1.MessageReaction.Delete(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("failed to delete reaction, code: %d, msg: %s", resp.Code, resp.Msg)
	}

	return nil
}

func switchMessageReaction(ctx context.Context, msgID, oldReactionID, newEmojiType string) error {
	var errs []string

	if oldReactionID != "" {
		if err := deleteMessageReaction(ctx, msgID, oldReactionID); err != nil {
			errs = append(errs, err.Error())
		}
	}

	if _, err := addMessageReaction(ctx, msgID, newEmojiType); err != nil {
		errs = append(errs, err.Error())
	}

	if len(errs) > 0 {
		// 必须用 errors.New：fmt.Errorf 会把 err 文本里的 % 当成格式串。
		return errors.New(strings.Join(errs, "; "))
	}

	return nil
}

// ---------------------------------------------------------------------------
// 事件入口
// ---------------------------------------------------------------------------

type messageJob struct {
	msgID      string
	msgType    string
	chatID     string
	content    string
	createTime int64 // 毫秒
	source     string
	reactionID string
}

func (j *messageJob) dedupKey() string {
	if j.msgID != "" {
		return j.msgID
	}
	return fmt.Sprintf("%s-%s-%d", j.chatID, j.msgType, j.createTime)
}

func main() {
	appID := os.Getenv("APP_ID")
	appSecret := os.Getenv("APP_SECRET")
	if appID == "" || appSecret == "" {
		panic("APP_ID or APP_SECRET environment variable not set")
	}
	if _, err := githubToken(); err != nil {
		panic(err)
	}

	// 单实例：避免多进程各自持有一把内存锁，导致读-改-写互相覆盖。
	lock, err := acquireInstanceLock(pidFilePath)
	if err != nil {
		panic(err)
	}
	defer lock.Close()

	appState = loadSyncState(stateFilePath)

	larkClient = lark.NewClient(appID, appSecret,
		lark.WithReqTimeout(envDuration("LARK_HTTP_TIMEOUT", 30*time.Second)),
		lark.WithEnableTokenCache(true),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go dispatchLoop(ctx)
	for i := 0; i < workSlots; i++ {
		go messageWorker(ctx, i)
	}
	startCatchUp(ctx)

	// 注册事件回调，OnP2MessageReceiveV1 为接收消息 v2.0；OnCustomizedEvent 内的 message 为接收消息 v1.0。
	eventHandler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			if event == nil || event.Event == nil || event.Event.Message == nil {
				fmt.Printf("[event] invalid event payload\n")
				return nil
			}

			msg := event.Event.Message
			if msg.MessageId == nil || msg.MessageType == nil || msg.Content == nil {
				fmt.Printf("[event] invalid message payload\n")
				return nil
			}

			job := &messageJob{
				msgID:   *msg.MessageId,
				msgType: *msg.MessageType,
				content: *msg.Content,
				source:  "event",
			}
			if msg.ChatId != nil {
				job.chatID = *msg.ChatId
			}
			if msg.CreateTime != nil {
				job.createTime, _ = strconv.ParseInt(*msg.CreateTime, 10, 64)
			}

			// 必须快速返回，否则会阻塞长连接读循环。
			if !processed.MarkIfNew(job.dedupKey()) {
				fmt.Printf("[event] duplicate message %s ignored\n", job.msgID)
				return nil
			}
			select {
			case eventQueue <- job:
			default:
				fmt.Printf("[event] queue full, dropping message %s\n", job.msgID)
			}
			return nil
		}).
		OnCustomizedEvent("这里填入你要自定义订阅的 event 的 key，例如 out_approval", func(ctx context.Context, event *larkevent.EventReq) error {
			fmt.Printf("[custom event] type: message, data: %s\n", string(event.Body))
			return nil
		})

	cli := larkws.NewClient(appID, appSecret,
		larkws.WithEventHandler(eventHandler),
		larkws.WithLogLevel(larkcore.LogLevelDebug),
	)

	fatalCh := make(chan error, 1)
	go func() {
		// Start 在正常情况下永不返回；返回即代表连接失败或上下文结束。
		fatalCh <- cli.Start(ctx)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	exitCode := 0
	select {
	case sig := <-sigCh:
		fmt.Printf("[main] received %s, shutting down\n", sig)
	case err := <-fatalCh:
		fmt.Printf("[ws] client stopped: %v\n", err)
		exitCode = 1
	}

	// 先停投递，再把水位落盘；未处理的作业会在下次启动的补偿扫描中重放。
	cancel()
	if appState != nil {
		_ = appState.save()
	}
	_ = lock.Close()
	os.Exit(exitCode)
}

func dispatchLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-eventQueue:
			// 一进队列就加「处理中」表情，用户能立即看到反馈。
			rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			reactionID, err := addMessageReaction(rctx, job.msgID, processingEmojiType)
			cancel()
			if err != nil {
				fmt.Printf("[reaction] add processing reaction for %s failed: %v\n", job.msgID, err)
			}
			job.reactionID = reactionID

			select {
			case workQueue <- job:
			case <-ctx.Done():
				return
			}
		}
	}
}

func messageWorker(ctx context.Context, id int) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-workQueue:
			processJob(ctx, id, job)
		}
	}
}

func processJob(parent context.Context, workerID int, job *messageJob) {
	ctx, cancel := context.WithTimeout(parent, messageTimeout)
	defer cancel()

	fmt.Printf("[worker-%d] processing %s (%s, source=%s)\n", workerID, job.msgID, job.msgType, job.source)

	err := handleMessage(ctx, job)

	finalEmojiType := doneEmojiType
	if err != nil {
		finalEmojiType = failedEmojiType
		fmt.Printf("[worker-%d] message %s failed: %v\n", workerID, job.msgID, err)
	}

	rctx, rcancel := context.WithTimeout(parent, 20*time.Second)
	defer rcancel()

	if job.reactionID != "" {
		if err := switchMessageReaction(rctx, job.msgID, job.reactionID, finalEmojiType); err != nil {
			fmt.Printf("[worker-%d] switch reaction for %s failed: %v\n", workerID, job.msgID, err)
		}
		return
	}
	if _, err := addMessageReaction(rctx, job.msgID, finalEmojiType); err != nil {
		fmt.Printf("[worker-%d] add final reaction for %s failed: %v\n", workerID, job.msgID, err)
	}
}

// contentPart 是一条消息按内容格式拆分后的最小单元。
type contentPart struct {
	kind     string // text | code | image | file | media
	text     string
	language string
	fileKey  string
	imageKey string
	fileName string
}

const (
	partText  = "text"
	partCode  = "code"
	partImage = "image"
	partFile  = "file"
	partMedia = "media"
)

type postPayload struct {
	Title   string          `json:"title"`
	Content [][]postElement `json:"content"`
}

type postElement struct {
	Tag      string `json:"tag"`
	Text     string `json:"text"`
	Href     string `json:"href"`
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	ImageKey string `json:"image_key"`
	FileKey  string `json:"file_key"`
	FileName string `json:"file_name"`
	Language string `json:"language"`
}

// splitMessageContent 把一条飞书消息按内容格式拆成有序的多个片段。
// 飞书的富文本（post）消息可以把文字、图片、视频、文件混排在同一条消息里，
// 因此不能再假设「一条消息 = 一种内容类型」。
func splitMessageContent(msgType, raw string) ([]contentPart, error) {
	switch msgType {
	case "text":
		var msg TextMsg
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			return nil, fmt.Errorf("%w: text: %v", errInvalidContent, err)
		}
		if strings.TrimSpace(msg.Text) == "" {
			return nil, fmt.Errorf("%w: empty text", errInvalidContent)
		}
		return []contentPart{{kind: partText, text: msg.Text}}, nil

	case "image":
		var msg ImgMsg
		if err := json.Unmarshal([]byte(raw), &msg); err != nil || msg.ImageKey == "" {
			return nil, fmt.Errorf("%w: image missing image_key", errInvalidContent)
		}
		return []contentPart{{kind: partImage, imageKey: msg.ImageKey}}, nil

	case "file":
		var msg FileMsg
		if err := json.Unmarshal([]byte(raw), &msg); err != nil || msg.FileKey == "" || msg.FileName == "" {
			return nil, fmt.Errorf("%w: file missing file_key/file_name", errInvalidContent)
		}
		return []contentPart{{kind: partFile, fileKey: msg.FileKey, fileName: msg.FileName}}, nil

	case "media":
		var msg MediaMsg
		if err := json.Unmarshal([]byte(raw), &msg); err != nil || msg.FileKey == "" {
			return nil, fmt.Errorf("%w: media missing file_key", errInvalidContent)
		}
		return []contentPart{{
			kind:     partMedia,
			fileKey:  msg.FileKey,
			imageKey: msg.ImageKey,
			fileName: msg.FileName,
		}}, nil

	case "post":
		return splitPostContent(raw)

	default:
		return nil, fmt.Errorf("%w: %s", errUnsupportedType, msgType)
	}
}

func splitPostContent(raw string) ([]contentPart, error) {
	var post postPayload
	if err := json.Unmarshal([]byte(unwrapPostLocale(raw)), &post); err != nil {
		return nil, fmt.Errorf("%w: post: %v", errInvalidContent, err)
	}

	var parts []contentPart

	if title := strings.TrimSpace(post.Title); title != "" {
		parts = append(parts, contentPart{kind: partText, text: title})
	}

	for _, line := range post.Content {
		var buf strings.Builder

		flush := func() {
			if s := strings.TrimSpace(buf.String()); s != "" {
				parts = append(parts, contentPart{kind: partText, text: s})
			}
			buf.Reset()
		}

		for _, el := range line {
			switch el.Tag {
			case "text":
				buf.WriteString(el.Text)
			case "a":
				if el.Href != "" {
					buf.WriteString(fmt.Sprintf("[%s](%s)", el.Text, el.Href))
				} else {
					buf.WriteString(el.Text)
				}
			case "at":
				if el.UserName != "" {
					buf.WriteString("@" + el.UserName)
				} else {
					buf.WriteString("@用户")
				}
			case "code_block":
				flush()
				parts = append(parts, contentPart{kind: partCode, text: el.Text, language: el.Language})
			case "hr":
				flush()
				parts = append(parts, contentPart{kind: partText, text: "---"})
			case "img":
				flush()
				if el.ImageKey != "" {
					parts = append(parts, contentPart{kind: partImage, imageKey: el.ImageKey})
				}
			case "media":
				flush()
				if el.FileKey != "" {
					parts = append(parts, contentPart{
						kind:     partMedia,
						fileKey:  el.FileKey,
						imageKey: el.ImageKey,
						fileName: el.FileName,
					})
				}
			case "file":
				flush()
				if el.FileKey != "" {
					parts = append(parts, contentPart{kind: partFile, fileKey: el.FileKey, fileName: el.FileName})
				}
			}
		}
		flush()
	}

	if len(parts) == 0 {
		return nil, fmt.Errorf("%w: post has no supported element", errInvalidContent)
	}
	return parts, nil
}

// unwrapPostLocale 兼容 `{"zh_cn":{...}}` 这种带语言包装的格式（发送侧用）。
func unwrapPostLocale(raw string) string {
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		return raw
	}
	if _, ok := wrapper["content"]; ok {
		return raw
	}
	for _, key := range []string{"zh_cn", "en_us", "ja_jp"} {
		if sub, ok := wrapper[key]; ok {
			return string(sub)
		}
	}
	return raw
}

func handleMessage(ctx context.Context, job *messageJob) error {
	parts, err := splitMessageContent(job.msgType, job.content)
	if err != nil {
		return err
	}

	// 一条消息只取一次时间，保证目录名、链接、标题在跨零点时也一致。
	now := time.Now().In(shanghaiLoc)

	gitHubOpMu.Lock()
	defer gitHubOpMu.Unlock()

	var (
		blocks []string
		errs   []string
	)

	for _, part := range parts {
		block, err := renderPart(ctx, job, part, now)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", part.kind, err))
			fmt.Printf("[handle] message %s part %s failed: %v\n", job.msgID, part.kind, err)
			continue
		}
		blocks = append(blocks, block)
	}

	if len(blocks) == 0 {
		return fmt.Errorf("all %d part(s) failed: %s", len(parts), strings.Join(errs, "; "))
	}

	// 一条消息只写一次文件：既保持片段顺序，也把 GitHub 写请求降到最低。
	if err := updateFileWithNewDayCheck(ctx, getFilePath(job.chatID), strings.Join(blocks, "\n\n"), now); err != nil {
		return fmt.Errorf("update %s: %w", getFilePath(job.chatID), err)
	}

	fmt.Printf("[handle] message %s synced: %d/%d part(s)\n", job.msgID, len(blocks), len(parts))

	if len(errs) > 0 {
		return fmt.Errorf("%d/%d part(s) failed but the rest were synced: %s",
			len(errs), len(parts), strings.Join(errs, "; "))
	}
	return nil
}

func renderPart(ctx context.Context, job *messageJob, part contentPart, now time.Time) (string, error) {
	switch part.kind {
	case partText:
		// 纯文本消息沿用原来的代码块样式；富文本里的文字按原样输出。
		if job.msgType == "text" {
			return fmt.Sprintf("```\n%s\n```", part.text), nil
		}
		return part.text, nil

	case partCode:
		return fmt.Sprintf("```%s\n%s\n```", part.language, part.text), nil

	case partImage:
		return renderImagePart(ctx, job, part, now)

	case partFile:
		return renderFilePart(ctx, job, part, now)

	case partMedia:
		return renderMediaPart(ctx, job, part, now)
	}

	return "", fmt.Errorf("%w: part %s", errUnsupportedType, part.kind)
}

func renderImagePart(ctx context.Context, job *messageJob, part contentPart, now time.Time) (string, error) {
	data, err := downloadMessageResource(ctx, job.msgID, part.imageKey, "image")
	if err != nil {
		return "", fmt.Errorf("download image: %w", err)
	}

	name, err := storeResource(ctx, now, imageResourceName(now), data)
	if err != nil {
		return "", fmt.Errorf("upload image: %w", err)
	}

	return fmt.Sprintf("![](%s)", proxiedBlobURL(now.Format("20060102"), name)), nil
}

func renderFilePart(ctx context.Context, job *messageJob, part contentPart, now time.Time) (string, error) {
	if part.fileName == "" {
		return "", fmt.Errorf("file key %s has no file_name", part.fileKey)
	}

	data, err := downloadMessageResource(ctx, job.msgID, part.fileKey, "file")
	if err != nil {
		return "", fmt.Errorf("download file: %w", err)
	}

	storedName, err := storeResource(ctx, now, sanitizeFileName(part.fileName), data)
	if err != nil {
		return "", fmt.Errorf("upload file: %w", err)
	}

	// 链接里使用实际落库的文件名，重名时可能是 xxx-1.pdf。
	return fmt.Sprintf("[%s](%s)", part.fileName, pdfViewerURL(now.Format("20060102"), storedName)), nil
}

func renderMediaPart(ctx context.Context, job *messageJob, part contentPart, now time.Time) (string, error) {
	dateDir := now.Format("20060102")

	data, err := downloadMessageResource(ctx, job.msgID, part.fileKey, "file")
	if err != nil {
		return "", fmt.Errorf("download media: %w", err)
	}

	preferred := part.fileName
	if preferred == "" {
		preferred = fmt.Sprintf("video_%s.mp4", now.Format("20060102150405"))
	}

	storedName, err := storeResource(ctx, now, sanitizeFileName(preferred), data)
	if err != nil {
		return "", fmt.Errorf("upload media: %w", err)
	}

	src := proxiedBlobURL(dateDir, storedName)

	// 封面图只是锦上添花，失败不影响视频本身。
	poster := ""
	if part.imageKey != "" {
		imageData, err := downloadMessageResource(ctx, job.msgID, part.imageKey, "image")
		if err != nil {
			fmt.Printf("[media] download poster for %s failed: %v\n", job.msgID, err)
		} else if posterName, err := storeResource(ctx, now, imageResourceName(now), imageData); err != nil {
			fmt.Printf("[media] upload poster for %s failed: %v\n", job.msgID, err)
		} else {
			poster = proxiedBlobURL(dateDir, posterName)
		}
	}

	if poster != "" {
		return fmt.Sprintf("<video controls width=\"100%%\" src=\"%s\" poster=\"%s\"></video>", src, poster), nil
	}
	return fmt.Sprintf("<video controls width=\"100%%\" src=\"%s\"></video>", src), nil
}

func pdfViewerURL(dateDir, fileName string) string {
	return fmt.Sprintf("https://alphahinex.github.io/habit/pdfjs-5.4.624-legacy-dist/web/viewer.html?file=https://alphahinex.github.io/habit/fftq/res/%s/%s",
		dateDir, fileName)
}

func isNewDay(fileContent string, now time.Time) bool {
	lastEntryDate, ok := getLastEntryDate(fileContent)
	if !ok {
		fmt.Printf("[isNewDay] no dated entries found, treat as new day\n")
		return true
	}

	currentDate := now.In(shanghaiLoc).Format("2006年1月2日")
	lastDate := lastEntryDate.In(shanghaiLoc).Format("2006年1月2日")
	if lastDate == currentDate {
		fmt.Printf("[isNewDay] latest entry date %s matches %s, not a new day\n", lastDate, currentDate)
		return false
	}

	fmt.Printf("[isNewDay] latest entry date %s does not match %s, new day detected\n", lastDate, currentDate)
	return true
}

func getCurrentTimestamp(now time.Time) string {
	local := now.In(shanghaiLoc)
	return local.Format("2006年1月2日 15:04") + " " + local.Weekday().String()
}

func getLastEntryDate(fileContent string) (time.Time, bool) {
	if strings.TrimSpace(fileContent) == "" {
		return time.Time{}, false
	}

	lines := strings.Split(fileContent, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "# ") {
			continue
		}

		title := strings.TrimPrefix(line, "# ")
		parts := strings.Fields(title)
		if len(parts) == 0 {
			continue
		}

		entryDate, err := time.ParseInLocation("2006年1月2日", parts[0], shanghaiLoc)
		if err == nil {
			return entryDate, true
		}
	}

	return time.Time{}, false
}

func archiveNotificationFile(ctx context.Context, fileContent string, now time.Time) error {
	entryDate, ok := getLastEntryDate(fileContent)
	if !ok {
		fmt.Printf("[archive] skip archive: no dated entries found\n")
		return nil
	}

	archivePath := fmt.Sprintf("fftq/notification-%s.md", entryDate.Format("20060102"))
	fmt.Printf("[archive] archiving notification.md to %s\n", archivePath)

	_, sha, err := getFileContent(ctx, archivePath)
	if err != nil {
		if !errors.Is(err, errGitHubNotFound) {
			return err
		}
		fmt.Printf("[archive] %s does not exist yet, creating it\n", archivePath)
		sha = ""
	}

	return putFileContent(ctx, archivePath, []byte(fileContent), sha, "Archive "+archivePath)
}

// updateFileWithNewDayCheck 更新指定文件内容，并同步维护 notification.md。
// 调用方必须持有 gitHubOpMu，以保证「读-改-写」的原子性。
func updateFileWithNewDayCheck(ctx context.Context, filePath, newContent string, now time.Time) error {
	fmt.Printf("[sync] begin update for %s\n", filePath)

	if filePath != notificationFilePath {
		fileContent, sha, err := getFileContent(ctx, filePath)
		if err != nil && !errors.Is(err, errGitHubNotFound) {
			return err
		}
		if errors.Is(err, errGitHubNotFound) {
			fileContent, sha = "", ""
		}

		fmt.Printf("[sync] appending content to %s\n", filePath)
		updated := attachNewContent(fileContent, newContent, now)
		if err := putFileContent(ctx, filePath, []byte(updated), sha, "Update "+filePath); err != nil {
			return fmt.Errorf("update %s failed: %w", filePath, err)
		}
	}

	notificationFileContent, notificationFileSha, err := getFileContent(ctx, notificationFilePath)
	if err != nil && !errors.Is(err, errGitHubNotFound) {
		return err
	}
	if errors.Is(err, errGitHubNotFound) {
		notificationFileContent, notificationFileSha = "", ""
	}

	var updatedContent string
	if isNewDay(notificationFileContent, now) {
		if err := archiveNotificationFile(ctx, notificationFileContent, now); err != nil {
			return fmt.Errorf("archive notification file: %w", err)
		}
		fmt.Printf("[sync] starting a fresh daily notification file\n")
		updatedContent = fmt.Sprintf("# %s\n\n%s\n", getCurrentTimestamp(now), newContent)
	} else {
		updatedContent = fmt.Sprintf("## %s\n\n%s\n\n%s", getCurrentTimestamp(now), newContent, notificationFileContent)
	}

	return putFileContent(ctx, notificationFilePath, []byte(updatedContent), notificationFileSha, "Update "+notificationFilePath)
}

func attachNewContent(originalContent, newContent string, now time.Time) string {
	suffix := "# Latest"
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(originalContent), suffix))
	trimmed = strings.TrimSpace(trimmed)

	if trimmed == "" {
		return fmt.Sprintf("# %s\n\n%s\n\n%s", getCurrentTimestamp(now), newContent, suffix)
	}

	return fmt.Sprintf("%s\n\n# %s\n\n%s\n\n%s",
		trimmed,
		getCurrentTimestamp(now),
		newContent,
		suffix)
}

// ---------------------------------------------------------------------------
// 断线补偿：回放长连接期间丢失的事件
// ---------------------------------------------------------------------------

func startCatchUp(ctx context.Context) {
	interval := envDuration("LARK_WS_CATCHUP_INTERVAL", defaultCatchUpInterval)
	if interval <= 0 {
		fmt.Printf("[catchup] disabled\n")
		return
	}
	initial := envDuration("LARK_WS_CATCHUP_INITIAL_DELAY", defaultCatchUpInitial)

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(initial):
		}

		for {
			catchUpAll(ctx)

			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
		}
	}()
}

func catchUpAll(ctx context.Context) {
	for _, chatID := range mappedChatIDs() {
		if err := catchUpChat(ctx, chatID); err != nil {
			// 缺少「获取会话历史消息」权限时这里会持续失败，只记录不退出。
			fmt.Printf("[catchup] chat %s failed: %v\n", chatID, err)
		}
	}
}

func mappedChatIDs() []string {
	ids := make([]string, 0, len(chatFileMapping))
	for id := range chatFileMapping {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func catchUpChat(ctx context.Context, chatID string) error {
	nowMS := time.Now().UnixNano() / int64(time.Millisecond)

	from := appState.watermark(chatID)
	if from == 0 {
		// 首次运行不回溯历史，避免把整段历史重放一遍。
		appState.advance(chatID, nowMS)
		fmt.Printf("[catchup] initialised watermark for %s\n", chatID)
		return nil
	}

	maxBackfill := int64(envDuration("LARK_WS_MAX_BACKFILL", 24*time.Hour) / time.Millisecond)
	if nowMS-from > maxBackfill {
		fmt.Printf("[catchup] chat %s watermark too old, backfilling at most %s\n",
			chatID, time.Duration(maxBackfill)*time.Millisecond)
		from = nowMS - maxBackfill
	}

	startMS := from - int64(catchUpOverlap/time.Millisecond)
	if startMS < 0 {
		startMS = 0
	}

	messages, err := listChatMessages(ctx, chatID, startMS/1000, nowMS/1000)
	if err != nil {
		return err
	}

	recovered := 0
	for _, m := range messages {
		job := jobFromHistoryMessage(chatID, m)
		if job == nil {
			continue
		}
		if !processed.MarkIfNew(job.dedupKey()) {
			continue
		}
		select {
		case eventQueue <- job:
			recovered++
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if recovered > 0 {
		fmt.Printf("[catchup] chat %s: recovered %d missed message(s)\n", chatID, recovered)
	}

	// 只推进到「扫描时刻 - 安全余量」，给飞书的历史索引留出延迟空间。
	safe := nowMS - int64(catchUpSafetyLag/time.Millisecond)
	if safe > 0 {
		appState.advance(chatID, safe)
	}
	return nil
}

func listChatMessages(ctx context.Context, chatID string, startSec, endSec int64) ([]*larkim.Message, error) {
	var (
		all       []*larkim.Message
		pageToken string
	)

	for page := 0; page < 50; page++ {
		builder := larkim.NewListMessageReqBuilder().
			ContainerIdType("chat").
			ContainerId(chatID).
			StartTime(strconv.FormatInt(startSec, 10)).
			EndTime(strconv.FormatInt(endSec, 10)).
			SortType("ByCreateTimeAsc").
			PageSize(50)
		if pageToken != "" {
			builder.PageToken(pageToken)
		}

		resp, err := larkClient.Im.V1.Message.List(ctx, builder.Build())
		if err != nil {
			return nil, err
		}
		if !resp.Success() {
			return nil, fmt.Errorf("list messages: code %d, msg %s", resp.Code, resp.Msg)
		}
		if resp.Data == nil {
			break
		}

		all = append(all, resp.Data.Items...)

		if resp.Data.HasMore == nil || !*resp.Data.HasMore ||
			resp.Data.PageToken == nil || *resp.Data.PageToken == "" {
			break
		}
		pageToken = *resp.Data.PageToken
	}

	return all, nil
}

func jobFromHistoryMessage(chatID string, m *larkim.Message) *messageJob {
	if m == nil || m.MessageId == nil || m.MsgType == nil || m.Body == nil || m.Body.Content == nil {
		return nil
	}
	if m.Deleted != nil && *m.Deleted {
		return nil
	}
	// 跳过机器人自己发的消息，避免补偿扫描自触发。
	if m.Sender != nil && m.Sender.SenderType != nil && *m.Sender.SenderType == "app" {
		return nil
	}

	job := &messageJob{
		msgID:   *m.MessageId,
		msgType: *m.MsgType,
		chatID:  chatID,
		content: *m.Body.Content,
		source:  "catchup",
	}
	if m.CreateTime != nil {
		job.createTime, _ = strconv.ParseInt(*m.CreateTime, 10, 64)
	}
	return job
}
