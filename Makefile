# tvctl build helpers.
#   make        build ./tvctl and install it to $(go env GOPATH)/bin (what `tv` runs)
#   make apk    build the companion app and refresh the copy embedded in `tv setup`
#   make test   run Go tests
JAVA_HOME ?= /opt/homebrew/opt/openjdk@17

.PHONY: all apk test
all:
	go build -o tvctl ./cmd/tvctl
	go install ./cmd/tvctl

apk:
	cd receiver && JAVA_HOME=$(JAVA_HOME) PATH=$(JAVA_HOME)/bin:$$PATH ./gradlew assembleDebug -q
	cp receiver/app/build/outputs/apk/debug/app-debug.apk internal/setup/receiver.apk
	go build -o tvctl ./cmd/tvctl
	go install ./cmd/tvctl

test:
	go test ./...
