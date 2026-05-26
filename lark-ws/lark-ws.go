package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

const notificationFilePath = "fftq/notification.md"

var gitHubFileMu sync.Mutex

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

func getFilePath(chatId string) string {
	if path, ok := chatFileMapping[chatId]; ok {
		return path
	}
	return notificationFilePath
}

type TextMsg struct {
	Text string `json:"text"`
}

type ImgMsg struct {
	ImageKey string `json:"image_key"`
}

type FileMsg struct {
	FileKey  string `json:"file_key"`
	FileName string `json:"file_name"`
}

type GitHubUploadRequest struct {
	Message string `json:"message"`
	Content string `json:"content"`
	Branch  string `json:"branch"`
}

type GitHubFileResponse struct {
	Content string `json:"content"`
	SHA     string `json:"sha"`
}

const (
	processingEmojiType = "Typing"
	doneEmojiType       = "DONE"
	failedEmojiType     = "SWEAT"
)

func getFileFromMsg(client *lark.Client, msgId, key, fileType string) ([]byte, error) {
	// 创建请求对象
	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(msgId).
		FileKey(key).
		Type(fileType).
		Build()

	// 发起请求
	resp, err := client.Im.V1.MessageResource.Get(context.Background(), req)

	// 处理错误
	if err != nil {
		return nil, err
	}

	// 服务端错误处理
	if !resp.Success() {
		fmt.Printf("logId: %s, error response: \n%s", resp.RequestId(), larkcore.Prettify(resp.CodeError))
		return nil, fmt.Errorf("failed to get image, code: %d, msg: %s", resp.Code, resp.Msg)
	}

	return resp.RawBody, nil
}

func uploadFileToGitHub(fileData []byte, fileName string) error {
	// GitHub 仓库信息
	repoOwner := "alphahinex"
	repoName := "habit"
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("GITHUB_TOKEN environment variable not set")
	}

	// 构建上传路径
	uploadPath := fmt.Sprintf("fftq/res/%s/%s", time.Now().Format("20060102"), fileName)

	// 构建 GitHub API URL
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s", repoOwner, repoName, uploadPath)

	// 准备请求数据
	content := base64.StdEncoding.EncodeToString(fileData)
	reqBody := GitHubUploadRequest{
		Message: fmt.Sprintf("Add %s", fileName),
		Content: content,
		Branch:  "master",
	}

	// 编码请求体
	reqBodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	// 创建 HTTP 客户端
	client := &http.Client{}

	// 创建请求
	req, err := http.NewRequest("PUT", url, nil)
	if err != nil {
		return err
	}
	req.Body = io.NopCloser(strings.NewReader(string(reqBodyJSON)))

	// 添加认证头
	req.Header.Add("Authorization", fmt.Sprintf("token %s", token))
	req.Header.Add("Content-Type", "application/json")

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 检查响应状态码
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		// 读取错误响应
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to upload image, status code: %d, error: %s", resp.StatusCode, string(errBody))
	}

	return nil
}

func getFileFromGitHub(fileName string) (string, string, error) {
	// GitHub 仓库信息
	repoOwner := "alphahinex"
	repoName := "habit"
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return "", "", fmt.Errorf("GITHUB_TOKEN environment variable not set")
	}

	// 构建 GitHub API URL
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s", repoOwner, repoName, fileName)

	// 创建 HTTP 客户端
	client := &http.Client{}

	// 创建请求
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", "", err
	}

	// 添加认证头
	req.Header.Add("Authorization", fmt.Sprintf("token %s", token))
	req.Header.Add("Content-Type", "application/json")

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		// 读取错误响应
		errBody, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("failed to get file, status code: %d, error: %s", resp.StatusCode, string(errBody))
	}

	// 解析响应
	var fileResp GitHubFileResponse
	err = json.NewDecoder(resp.Body).Decode(&fileResp)
	if err != nil {
		return "", "", err
	}

	// 解码内容
	content, err := base64.StdEncoding.DecodeString(fileResp.Content)
	if err != nil {
		return "", "", err
	}

	return string(content), fileResp.SHA, nil
}

func updateFileOnGitHub(fileName, content, sha string) error {
	// GitHub 仓库信息
	repoOwner := "alphahinex"
	repoName := "habit"
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("GITHUB_TOKEN environment variable not set")
	}

	// 构建 GitHub API URL
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s", repoOwner, repoName, fileName)

	// 准备请求数据
	reqBody := map[string]string{
		"message": "Update",
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
		"branch":  "master",
	}
	if sha != "" {
		reqBody["sha"] = sha
	}

	// 编码请求体
	reqBodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	// 创建 HTTP 客户端
	client := &http.Client{}

	// 创建请求
	req, err := http.NewRequest("PUT", url, nil)
	if err != nil {
		return err
	}
	req.Body = io.NopCloser(strings.NewReader(string(reqBodyJSON)))

	// 添加认证头
	req.Header.Add("Authorization", fmt.Sprintf("token %s", token))
	req.Header.Add("Content-Type", "application/json")

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		// 读取错误响应
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to update, status code: %d, error: %s", resp.StatusCode, string(errBody))
	}

	fmt.Printf("[updateFileOnGitHub] updated %s successfully\n", fileName)

	return nil
}

func addMessageReaction(client *lark.Client, msgID, emojiType string) (string, error) {
	req := larkim.NewCreateMessageReactionReqBuilder().
		MessageId(msgID).
		Body(&larkim.CreateMessageReactionReqBody{
			ReactionType: larkim.NewEmojiBuilder().EmojiType(emojiType).Build(),
		}).
		Build()

	resp, err := client.Im.V1.MessageReaction.Create(context.Background(), req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("failed to add reaction %s, code: %d, msg: %s", emojiType, resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.ReactionId == nil {
		return "", fmt.Errorf("add reaction succeeded but reaction_id is empty")
	}

	return *resp.Data.ReactionId, nil
}

func deleteMessageReaction(client *lark.Client, msgID, reactionID string) error {
	req := larkim.NewDeleteMessageReactionReqBuilder().
		MessageId(msgID).
		ReactionId(reactionID).
		Build()

	resp, err := client.Im.V1.MessageReaction.Delete(context.Background(), req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("failed to delete reaction, code: %d, msg: %s", resp.Code, resp.Msg)
	}

	return nil
}

func switchMessageReaction(client *lark.Client, msgID, oldReactionID, newEmojiType string) error {
	var errs []string

	if oldReactionID != "" {
		if err := deleteMessageReaction(client, msgID, oldReactionID); err != nil {
			errs = append(errs, err.Error())
		}
	}

	if _, err := addMessageReaction(client, msgID, newEmojiType); err != nil {
		errs = append(errs, err.Error())
	}

	if len(errs) > 0 {
		return fmt.Errorf(strings.Join(errs, "; "))
	}

	return nil
}

func main() {
	appID := os.Getenv("APP_ID")
	appSecret := os.Getenv("APP_SECRET")
	if appID == "" || appSecret == "" {
		panic("APP_ID or APP_SECRET environment variable not set")
	}

	// 创建 Client
	client := lark.NewClient(appID, appSecret)

	// 注册事件回调，OnP2MessageReceiveV1 为接收消息 v2.0；OnCustomizedEvent 内的 message 为接收消息 v1.0。
	eventHandler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			fmt.Printf("[ OnP2MessageReceiveV1 access ], data: %s\n", larkcore.Prettify(event))

			if event == nil || event.Event == nil || event.Event.Message == nil || event.Event.Message.MessageId == nil || event.Event.Message.Content == nil {
				fmt.Printf("[ OnP2MessageReceiveV1 access ], invalid event payload\n")
				return nil
			}

			msgID := *event.Event.Message.MessageId
			rawContent := *event.Event.Message.Content
			var chatId string
			if event.Event.Message.ChatId != nil {
				chatId = *event.Event.Message.ChatId
			}

			go func() {
				finalEmojiType := doneEmojiType
				processingReactionID, err := addMessageReaction(client, msgID, processingEmojiType)
				if err != nil {
					fmt.Printf("[ OnP2MessageReceiveV1 access ], failed to add processing reaction: %v\n", err)
				}

				defer func() {
					if err := switchMessageReaction(client, msgID, processingReactionID, finalEmojiType); err != nil {
						fmt.Printf("[ OnP2MessageReceiveV1 access ], failed to switch reaction to final status(%s): %v\n", finalEmojiType, err)
					}
				}()

				var textMsg TextMsg
				if err := json.Unmarshal([]byte(rawContent), &textMsg); err == nil && len(textMsg.Text) > 0 {
					fmt.Printf("[ OnP2MessageReceiveV1 access ], text: %s\n", textMsg.Text)

					chatFilePath := getFilePath(chatId)
					newContent := fmt.Sprintf("```\n%s\n```", textMsg.Text)

					if err := updateFileWithNewDayCheck(chatFilePath, newContent); err != nil {
						finalEmojiType = failedEmojiType
						fmt.Printf("[ OnP2MessageReceiveV1 access ], failed to update file: %v\n", err)
						return
					}

					fmt.Printf("[ OnP2MessageReceiveV1 access ], file updated successfully\n")
					return
				}

				var imgMsg ImgMsg
				if err := json.Unmarshal([]byte(rawContent), &imgMsg); err == nil && len(imgMsg.ImageKey) > 0 {
					fmt.Printf("[ OnP2MessageReceiveV1 access ], image_key: %s\n", imgMsg.ImageKey)

					imageData, err := getFileFromMsg(client, msgID, imgMsg.ImageKey, "image")
					if err != nil {
						finalEmojiType = failedEmojiType
						fmt.Printf("[ OnP2MessageReceiveV1 access ], failed to get image: %v\n", err)
						return
					}

					fileName := fmt.Sprintf("%d.jpg", time.Now().Unix())
					if err := uploadFileToGitHub(imageData, fileName); err != nil {
						finalEmojiType = failedEmojiType
						fmt.Printf("[ OnP2MessageReceiveV1 access ], failed to upload image: %v\n", err)
						return
					}

					fmt.Printf("[ OnP2MessageReceiveV1 access ], image uploaded successfully: %s\n", fileName)

					chatFilePath := getFilePath(chatId)
					newContent := fmt.Sprintf("![](https://gh-proxy.com/https://github.com/AlphaHinex/habit/blob/master/fftq/res/%s/%s)",
						time.Now().Format("20060102"), fileName)

					if err := updateFileWithNewDayCheck(chatFilePath, newContent); err != nil {
						finalEmojiType = failedEmojiType
						fmt.Printf("[ OnP2MessageReceiveV1 access ], failed to update file: %v\n", err)
						return
					}

					fmt.Printf("[ OnP2MessageReceiveV1 access ], file updated successfully\n")
					return
				}

				var fileMsg FileMsg
				if err := json.Unmarshal([]byte(rawContent), &fileMsg); err == nil && len(fileMsg.FileKey) > 0 {
					fileData, err := getFileFromMsg(client, msgID, fileMsg.FileKey, "file")
					if err != nil {
						finalEmojiType = failedEmojiType
						fmt.Printf("[File Message] could not get file: %v\n", err)
						return
					}

					if err := uploadFileToGitHub(fileData, fileMsg.FileName); err != nil {
						finalEmojiType = failedEmojiType
						fmt.Printf("[File Message] failed to upload file: %v\n", err)
						return
					}

					chatFilePath := getFilePath(chatId)
					newContent := fmt.Sprintf("[%s](https://alphahinex.github.io/habit/pdfjs-5.4.624-legacy-dist/web/viewer.html?file=https://alphahinex.github.io/habit/fftq/res/%s/%s)",
						fileMsg.FileName,
						time.Now().Format("20060102"),
						fileMsg.FileName)

					if err := updateFileWithNewDayCheck(chatFilePath, newContent); err != nil {
						finalEmojiType = failedEmojiType
						fmt.Printf("[ OnP2MessageReceiveV1 access ], failed to update file: %v\n", err)
						return
					}

					fmt.Printf("[ OnP2MessageReceiveV1 access ], file updated successfully\n")
					return
				}

				finalEmojiType = failedEmojiType
				fmt.Printf("[ OnP2MessageReceiveV1 access ], unsupported message content\n")
			}()

			return nil
		}).
		OnCustomizedEvent("这里填入你要自定义订阅的 event 的 key，例如 out_approval", func(ctx context.Context, event *larkevent.EventReq) error {
			fmt.Printf("[ OnCustomizedEvent access ], type: message, data: %s\n", string(event.Body))
			return nil
		})

	// 创建Client
	cli := larkws.NewClient(appID, appSecret,
		larkws.WithEventHandler(eventHandler),
		larkws.WithLogLevel(larkcore.LogLevelDebug),
	)
	// 启动客户端
	err := cli.Start(context.Background())
	if err != nil {
		panic(err)
	}
}

func isNewDay(fileContent string) bool {
	lastEntryDate, ok := getLastEntryDate(fileContent)
	if !ok {
		fmt.Printf("[isNewDay] no dated entries found, treat as new day\n")
		return true
	}

	loc, _ := time.LoadLocation("Asia/Shanghai")
	currentDate := time.Now().In(loc).Format("2006年1月2日")
	lastDate := lastEntryDate.In(loc).Format("2006年1月2日")
	if lastDate == currentDate {
		fmt.Printf("[isNewDay] latest entry date %s matches %s, not a new day\n", lastDate, currentDate)
		return false
	}

	fmt.Printf("[isNewDay] latest entry date %s does not match %s, new day detected\n", lastDate, currentDate)
	return true
}

func getCurrentTimestamp() string {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	return time.Now().In(loc).Format("2006年1月2日 15:04") + " " + time.Now().In(loc).Weekday().String()
}

func getLastEntryDate(fileContent string) (time.Time, bool) {
	if strings.TrimSpace(fileContent) == "" {
		return time.Time{}, false
	}

	loc, _ := time.LoadLocation("Asia/Shanghai")
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

		entryDate, err := time.ParseInLocation("2006年1月2日", parts[0], loc)
		if err == nil {
			return entryDate, true
		}
	}

	return time.Time{}, false
}

func archiveNotificationFile(fileContent string) error {
	entryDate, ok := getLastEntryDate(fileContent)
	if !ok {
		fmt.Printf("[archiveNotificationFile] skip archive: no dated entries found\n")
		return nil
	}

	archivePath := fmt.Sprintf("fftq/notification-%s.md", entryDate.Format("20060102"))
	fmt.Printf("[archiveNotificationFile] archiving notification.md to %s\n", archivePath)
	_, archiveSha, err := getFileFromGitHub(archivePath)
	if err != nil {
		if !strings.Contains(err.Error(), "status code: 404") {
			return err
		}
		fmt.Printf("[archiveNotificationFile] %s does not exist yet, creating it\n", archivePath)
		archiveSha = ""
	}

	return updateFileOnGitHub(archivePath, fileContent, archiveSha)
}

// 更新 filePath 指定文件内容，若 filePath 不是 notificationFilePath，同时向 notificationFilePath 顶部插入最新内容。
// 更新非 notificationFilePath 时，直接在尾部追加新内容，并放置 Latest 标签；
// 更新 notificationFilePath 文件时，直接在前面插入最新内容。
// 若发现 newContent 与 notificationFilePath 中最新内容不是相同日期，则为 notificationFilePath 文件创建备份，并覆盖原 notificationFilePath 中内容。
func updateFileWithNewDayCheck(filePath, newContent string) error {
	gitHubFileMu.Lock()
	defer gitHubFileMu.Unlock()

	fmt.Printf("[updateFileWithNewDayCheck] begin update for %s\n", filePath)

	isNotificationFile := filePath == notificationFilePath
	updatedContent := ""
	if !isNotificationFile {
		fileContent, sha, err := getFileFromGitHub(filePath)
		if err != nil {
			return err
		}
		fmt.Printf("[updateFileWithNewDayCheck] appending content to %s\n", filePath)
		updatedContent = attachNewContent(fileContent, newContent)
		if err := updateFileOnGitHub(filePath, updatedContent, sha); err != nil {
			return fmt.Errorf("update %s failed: %w", filePath, err)
		}
	}
	notificationFileContent, notificationFileSha, err := getFileFromGitHub(notificationFilePath)
	if err != nil {
		return err
	}
	if isNewDay(notificationFileContent) {
		if err := archiveNotificationFile(notificationFileContent); err != nil {
			return fmt.Errorf("archive notification file: %w", err)
		}
		fmt.Printf("[updateFileWithNewDayCheck] starting a fresh daily notification file\n")
		updatedContent = fmt.Sprintf("# %s\n\n%s\n", getCurrentTimestamp(), newContent)
	} else {
		updatedContent = fmt.Sprintf("%s\n\n%s", newContent, notificationFileContent)
	}
	return updateFileOnGitHub(notificationFilePath, updatedContent, notificationFileSha)
}

func attachNewContent(originalContent string, newContent string) string {
	suffix := "# Latest"
	return fmt.Sprintf("%s\n\n# %s\n\n%s\n\n%s",
		strings.TrimSuffix(strings.TrimSpace(originalContent), suffix),
		getCurrentTimestamp(),
		newContent,
		suffix)
}
