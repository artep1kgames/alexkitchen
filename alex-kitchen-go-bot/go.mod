module alex-kitchen-go-bot

go 1.24.0

toolchain go1.24.1

require (
	github.com/joho/godotenv v1.5.1
	github.com/max-messenger/max-bot-api-client-go v1.6.15
)

require (
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/modern-go/concurrent v0.0.0-20180228061459-e0a39a4cb421 // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
)

replace github.com/max-messenger/max-bot-api-client-go => ./max-bot-api-client-go
