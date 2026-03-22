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
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

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
	reqBody := struct {
		Message string `json:"message"`
		Content string `json:"content"`
		SHA     string `json:"sha"`
		Branch  string `json:"branch"`
	}{
		Message: "Update",
		Content: base64.StdEncoding.EncodeToString([]byte(content)),
		SHA:     sha,
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
	if resp.StatusCode != http.StatusOK {
		// 读取错误响应
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to update, status code: %d, error: %s", resp.StatusCode, string(errBody))
	}

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

					fileContent, sha, err := getFileFromGitHub("fftq/notification.md")
					if err != nil {
						finalEmojiType = failedEmojiType
						fmt.Printf("[ OnP2MessageReceiveV1 access ], failed to get file: %v\n", err)
						return
					}

					addToHead := fmt.Sprintf("```\n%s\n```", textMsg.Text)
					newContent := fmt.Sprintf("%s\n%s\n\n%s",
						time.Now().UTC().Format("2006-01-02 15:04 UTC"),
						addToHead,
						fileContent)

					if err := updateFileOnGitHub("fftq/notification.md", newContent, sha); err != nil {
						finalEmojiType = failedEmojiType
						fmt.Printf("[ OnP2MessageReceiveV1 access ], failed to update file: %v\n", err)
					} else {
						fmt.Printf("[ OnP2MessageReceiveV1 access ], file updated successfully\n")
					}
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

					fileContent, sha, err := getFileFromGitHub("fftq/notification.md")
					if err != nil {
						finalEmojiType = failedEmojiType
						fmt.Printf("[ OnP2MessageReceiveV1 access ], failed to get file: %v\n", err)
						return
					}

					imageURL := fmt.Sprintf("https://gh-proxy.com/https://github.com/AlphaHinex/habit/blob/master/fftq/res/%s/%s",
						time.Now().Format("20060102"), fileName)
					newContent := fmt.Sprintf("%s\n![](%s)\n\n%s", time.Now().UTC().Format("2006-01-02 15:04 UTC"), imageURL, fileContent)

					if err := updateFileOnGitHub("fftq/notification.md", newContent, sha); err != nil {
						finalEmojiType = failedEmojiType
						fmt.Printf("[ OnP2MessageReceiveV1 access ], failed to update file: %v\n", err)
					} else {
						fmt.Printf("[ OnP2MessageReceiveV1 access ], file updated successfully\n")
					}
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

					fileContent, sha, err := getFileFromGitHub("fftq/notification.md")
					if err != nil {
						finalEmojiType = failedEmojiType
						fmt.Printf("[ OnP2MessageReceiveV1 access ], failed to get file: %v\n", err)
						return
					}

					addToHead := fmt.Sprintf("[%s](https://alphahinex.github.io/habit/pdfjs-5.4.624-legacy-dist/web/viewer.html?file=https://alphahinex.github.io/habit/fftq/res/%s/%s)",
						fileMsg.FileName,
						time.Now().Format("20060102"),
						fileMsg.FileName)
					newContent := fmt.Sprintf("%s\n%s\n\n%s",
						time.Now().UTC().Format("2006-01-02 15:04 UTC"),
						addToHead,
						fileContent)

					if err := updateFileOnGitHub("fftq/notification.md", newContent, sha); err != nil {
						finalEmojiType = failedEmojiType
						fmt.Printf("[ OnP2MessageReceiveV1 access ], failed to update file: %v\n", err)
					} else {
						fmt.Printf("[ OnP2MessageReceiveV1 access ], file updated successfully\n")
					}
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
