BUILD_TIME := $(shell date --rfc-3339=seconds)
COMMIT_ID := $(shell git rev-parse HEAD)

LDFLAGS = -X "github.com/Oumainory/DDBOT-AI/lsp.BuildTime='"$(BUILD_TIME)"'" -X "github.com/Oumainory/DDBOT-AI/lsp.CommitId='"$(COMMIT_ID)"'" -X "github.com/Oumainory/DDBOT-AI/internal/buildinfo.BuildTime='"$(BUILD_TIME)"'" -X "github.com/Oumainory/DDBOT-AI/internal/buildinfo.Commit='"$(COMMIT_ID)"'"

SRC := $(shell find . -type f -name '*.go') lsp/template/default/*
PROTO := $(shell find . -type f -name '*.proto')
COV := .coverage.out
TARGET := ddbot-ai

$(COV): $(SRC)
	CGO_ENABLED=0 go test ./... -coverprofile=$(COV)


$(TARGET): $(SRC) go.mod go.sum
	CGO_ENABLED=0 go build -pgo=auto -ldflags '$(LDFLAGS)' -o $(TARGET) github.com/Oumainory/DDBOT-AI/cmd

build: $(TARGET)

compat-verify:
	CGO_ENABLED=0 go run ./compat/cmd verify --report compat-report.json

proto: $(PROTO)
	protoc --go_out=. $(PROTO)

test: $(COV)

coverage: $(COV)
	go tool cover -func=$(COV) | grep -v 'pb.go'

report: $(COV)
	go tool cover -html=$(COV)

clean:
	- rm -rf $(TARGET) $(COV)
