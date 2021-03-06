BIN=fireworqonsole
SHELL=/bin/bash -O globstar
BUILD_OUTPUT=.
TEST_OUTPUT=.
GO=go
PRERELEASE=SNAPSHOT
BUILD=$$(git describe --always)
NODE=http://127.0.0.1:8080
BIND=0.0.0.0:8888

all: clean build

build: generate
	npm install
	npm run build
	GOOS= GOARCH= go-assets-builder -p assets -s /assets -o assets/assets.go assets
	${GO} build -ldflags "-X main.Build=$(BUILD) -X main.Prerelease=DEBUG" -o ${BUILD_OUTPUT}/$(BIN) .

release: npmbuild deps credits generate
	GOOS= GOARCH= go-assets-builder -p assets -s /assets -o assets/assets.go assets
	CGO_ENABLED=0 ${GO} build -ldflags "-X main.Build=$(BUILD) -X main.Prerelease=$(PRERELEASE)" -o ${BUILD_OUTPUT}/$(BIN) .

run: build
	npm run dev & ./${BIN} --bind=${BIND} --node=${NODE} --debug & wait

deps:
	GOOS= GOARCH= glide install
	GOOS= GOARCH= ${GO} get github.com/jessevdk/go-assets-builder

npmbuild:
	npm install
	npm run build:prod
	npm prune --production

credits:
	GOOS= GOARCH= ${GO} run script/genauthors/genauthors.go > AUTHORS
	script/credits-go > CREDITS.go.json
	script/credits-npm > CREDITS.npm.json

generate: deps
	touch AUTHORS
	touch CREDITS.go.json CREDITS.npm.json
	GOOS= GOARCH= ${GO} generate -x ./...

clean:
	rm -f **/assets.go CREDITS.go.json CREDITS.npm.json
	rm -f $(BIN)
	${GO} clean

.PHONY: all build release run deps npmbuild credits generate clean

