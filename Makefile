all:
	GOOS=darwin GOARCH=amd64 go build -o dns-checker-mac
	GOOS=linux GOARCH=arm GOARM=7 go build -o dns-checker-raspberry
