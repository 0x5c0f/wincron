module wincron

// Go 1.20 是最后一个支持 Windows Server 2012 / 2012 R2 的工具链。
// 保持 go 1.20 才能用 Go 1.20.14 编译 legacy 产物；更高版本工具链也可编译本模块。
go 1.20

require (
	github.com/gin-gonic/gin v1.9.1
	github.com/kardianos/service v1.2.2
	github.com/nikoksr/notify v0.38.1
	github.com/robfig/cron/v3 v3.0.1
	golang.org/x/sys v0.8.0
	golang.org/x/text v0.9.0
)

require (
	github.com/atc0005/go-teams-notify/v2 v2.6.1 // indirect
	github.com/blinkbean/dingtalk v0.0.0-20210905093040-7d935c0f7e19 // indirect
	github.com/bwmarrin/discordgo v0.27.1 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/go-lark/lark v1.7.4 // indirect
	github.com/go-telegram-bot-api/telegram-bot-api v4.6.4+incompatible // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/gregdel/pushover v1.1.0 // indirect
	github.com/jordan-wright/email v4.0.1-0.20210109023952-943e75fe5223+incompatible // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/slack-go/slack v0.12.1 // indirect
	github.com/stretchr/objx v0.5.0 // indirect
	github.com/stretchr/testify v1.8.3 // indirect
	github.com/technoweenie/multipartstreamer v1.0.1 // indirect
	golang.org/x/sync v0.1.0 // indirect
)

require (
	github.com/bytedance/sonic v1.9.1 // indirect
	github.com/chenzhuoyu/base64x v0.0.0-20221115062448-fe3a3abad311 // indirect
	github.com/gabriel-vasile/mimetype v1.4.2 // indirect
	github.com/gin-contrib/sse v0.1.0 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.14.0 // indirect
	github.com/goccy/go-json v0.10.2 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/cpuid/v2 v2.2.4 // indirect
	github.com/leodido/go-urn v1.2.4 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/pelletier/go-toml/v2 v2.0.8 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/ugorji/go/codec v1.2.11 // indirect
	golang.org/x/arch v0.3.0 // indirect
	golang.org/x/crypto v0.9.0 // indirect
	golang.org/x/net v0.10.0 // indirect
	google.golang.org/protobuf v1.30.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
