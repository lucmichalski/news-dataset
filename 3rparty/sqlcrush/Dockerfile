FROM python:3.8-alpine

RUN apk add --no-cache nano bash jq postgresql-dev gcc musl-dev

WORKDIR /app
COPY . .

RUN pip install -e .

ENTRYPOINT ["sqlcrush"]
#  -t sqlite -d demo.sqlite3
