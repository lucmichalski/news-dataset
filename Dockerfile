FROM golang:alpine3.11
MAINTAINER michalski luc <michalski.luc@gmail.com>

RUN apk add --no-cache nano bash jq gcc musl-dev

WORKDIR /app
COPY . .

RUN go build -o ./bin/medium-scraper medium.go

CMD ["/bin/bash"]
