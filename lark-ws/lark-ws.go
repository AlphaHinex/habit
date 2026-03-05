package main

import (
	"context"
	"encoding/json"
	"fmt"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"os"
)

type TextMsg struct {
	Text string `json:"text"`
}

type ImgMsg struct {
	ImageKey string `json:"image_key"`
}

func main() {
	appID := os.Getenv("APP_ID")
	appSecret := os.Getenv("APP_SECRET")
	if appID == "" || appSecret == "" {
		panic("APP_ID or APP_SECRET environment variable not set")
	}

	// 注册事件回调，OnP2MessageReceiveV1 为接收消息 v2.0；OnCustomizedEvent 内的 message 为接收消息 v1.0。
	eventHandler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			fmt.Printf("[ OnP2MessageReceiveV1 access ], data: %s\n", larkcore.Prettify(event))
			content := event.Event.Message.Content
			var textMsg TextMsg
			err := json.Unmarshal([]byte(*content), &textMsg)
			if err == nil && len(textMsg.Text) > 0 {
				fmt.Printf("[ OnP2MessageReceiveV1 access ], text: %s\n", textMsg.Text)
			} else {
				var imgMsg ImgMsg
				err = json.Unmarshal([]byte(*content), &imgMsg)
				if err == nil && len(imgMsg.ImageKey) > 0 {
					fmt.Printf("[ OnP2MessageReceiveV1 access ], image_key: %s\n", imgMsg.ImageKey)
				}
			}
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
