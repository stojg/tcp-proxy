build:
	GOOS=linux go build -ldflags="-s -w" -o tcp-proxy && upx tcp-proxy